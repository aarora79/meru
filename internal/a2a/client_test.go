// This file tests the Client against the test agent: card fetch and tool
// names, allow filtering, calls in both modes, failures, timeouts, headers,
// the loopback guard and the retry wait.

package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/aarora79/meru/internal/dispatch"
)

// newClient builds a Client for one agent named "research" at url and
// closes it when the test ends.
func newClient(t *testing.T, cfg AgentConfig) *Client {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "research"
	}
	c, err := New(context.Background(), []AgentConfig{cfg}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// message builds the arguments the model sends.
func message(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"message": text})
	return b
}

func TestNewDoesNoIO(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	if hits, _, _, _ := ta.snapshot(); hits != 0 {
		t.Errorf("New fetched the card %d times, want 0", hits)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	_, err := New(context.Background(), []AgentConfig{{Name: "x", URL: "https://agent.example.com"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "remote = true") {
		t.Fatalf("New() = %v, want a loopback error", err)
	}
}

func TestToolsFromCard(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	c := newClient(t, AgentConfig{
		URL:     ta.srv.URL,
		Allow:   []string{"translate", "summarize", "missing"},
		Confirm: []string{"translate"},
	})

	tools := c.Tools()
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	want := []string{"a2a.research.summarize", "a2a.research.translate"}
	if !slices.Equal(names, want) {
		t.Fatalf("Tools() names = %v, want %v", names, want)
	}
	if d := tools[0].Description; !strings.Contains(d, "Summarize a document.") || !strings.Contains(d, "research (Research Agent)") {
		t.Errorf("description = %q, want the skill's description and the agent's name", d)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tools[0].Parameters, &schema); err != nil {
		t.Fatalf("schema isn't JSON: %v", err)
	}
	if schema.Type != "object" || schema.Properties["message"] == nil || !slices.Equal(schema.Required, []string{"message"}) {
		t.Errorf("schema = %s, want an object with a required message", tools[0].Parameters)
	}

	st := c.Status()
	if len(st) != 1 {
		t.Fatalf("Status() has %d entries, want 1", len(st))
	}
	s := st[0]
	if s.Name != "research" || s.Kind != "a2a" || s.Transport != "http" || !s.Connected || s.LastError != "" {
		t.Errorf("Status() = %+v", s)
	}
	if s.Offered != 3 || len(s.Tools) != 2 || !slices.Equal(s.Unknown, []string{"missing"}) {
		t.Errorf("Status() offered %d, tools %d, unknown %v; want 3, 2, [missing]", s.Offered, len(s.Tools), s.Unknown)
	}
	if s.Tools[0].Confirm || !s.Tools[1].Confirm {
		t.Errorf("Status() confirm flags = %v, %v; want false, true", s.Tools[0].Confirm, s.Tools[1].Confirm)
	}

	// One fetch serves every later Tools and Status call.
	if hits, _, _, _ := ta.snapshot(); hits != 1 {
		t.Errorf("card fetched %d times, want 1", hits)
	}
}

func TestEmptyAllowGivesNothing(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	c := newClient(t, AgentConfig{URL: ta.srv.URL})
	if tools := c.Tools(); len(tools) != 0 {
		t.Errorf("Tools() = %v, want none", tools)
	}
	if st := c.Status(); st[0].Offered != 3 || !st[0].Connected {
		t.Errorf("Status() = %+v, want 3 offered and connected", st[0])
	}
}

func TestConfirmAndLocate(t *testing.T) {
	c := newClient(t, AgentConfig{URL: "http://127.0.0.1:1", Allow: []string{"summarize", "translate"}, Confirm: []string{"translate"}})
	if c.Kind() != dispatch.KindA2A {
		t.Errorf("Kind() = %q", c.Kind())
	}
	confirms := []struct {
		name string
		want dispatch.Confirm
	}{
		{"a2a.research.translate", dispatch.ConfirmAsk},
		{"a2a.research.summarize", dispatch.ConfirmNever},
		{"a2a.other.translate", dispatch.ConfirmNever},
		{"translate", dispatch.ConfirmNever},
	}
	for _, tt := range confirms {
		if got := c.Confirm(tt.name); got != tt.want {
			t.Errorf("Confirm(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
	locates := []struct{ name, server, tool string }{
		{"a2a.research.summarize", "research", "summarize"},
		{"a2a.research.docs.summarize", "research", "docs.summarize"},
		{"a2a.research", "", "a2a.research"},
		{"files.read", "", "files.read"},
	}
	for _, tt := range locates {
		server, tool := c.Locate(tt.name)
		if server != tt.server || tool != tt.tool {
			t.Errorf("Locate(%q) = (%q, %q), want (%q, %q)", tt.name, server, tool, tt.server, tt.tool)
		}
	}
}

func TestBlockingCall(t *testing.T) {
	ta := startAgent(t, agentOptions{streaming: false, execute: completeWith("a short summary")})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})

	res, err := c.Call(context.Background(), "a2a.research.summarize", message("summarize this"))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError || res.Text != "a short summary" {
		t.Errorf("Call() = %+v, want the artifact text", res)
	}
	if _, calls, streams, _ := ta.snapshot(); calls != 1 || streams != 0 {
		t.Errorf("agent saw %d calls, %d streams; want 1 blocking call", calls, streams)
	}
}

func TestMessageReply(t *testing.T) {
	reply := func(_ context.Context, _ *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool) {
		yield(sdk.NewMessage(sdk.MessageRoleAgent, sdk.NewTextPart("hello back")), nil)
	}
	for _, streaming := range []bool{false, true} {
		ta := startAgent(t, agentOptions{streaming: streaming, execute: reply})
		c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
		res, err := c.Call(context.Background(), "a2a.research.summarize", message("hi"))
		if err != nil || res.IsError || res.Text != "hello back" {
			t.Errorf("streaming=%v: Call() = %+v, %v; want the message text", streaming, res, err)
		}
	}
}

func TestStreamingCall(t *testing.T) {
	chunks := func(_ context.Context, ec *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool) {
		if !yield(sdk.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		if !yield(sdk.NewStatusUpdateEvent(ec, sdk.TaskStateWorking, nil), nil) {
			return
		}
		first := sdk.NewArtifactEvent(ec, sdk.NewTextPart("part one, "))
		if !yield(first, nil) {
			return
		}
		more := sdk.NewArtifactUpdateEvent(ec, first.Artifact.ID, sdk.NewTextPart("part two"))
		more.Append = true
		if !yield(more, nil) {
			return
		}
		if !yield(sdk.NewArtifactEvent(ec, sdk.NewDataPart(map[string]any{"words": 4})), nil) {
			return
		}
		yield(sdk.NewStatusUpdateEvent(ec, sdk.TaskStateCompleted, nil), nil)
	}
	ta := startAgent(t, agentOptions{streaming: true, execute: chunks})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})

	res, err := c.Call(context.Background(), "a2a.research.summarize", message("summarize this"))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	want := "part one, part two\n\n{\"words\":4}"
	if res.IsError || res.Text != want {
		t.Errorf("Call() = %+v, want text %q", res, want)
	}
	if _, calls, streams, _ := ta.snapshot(); calls != 1 || streams != 1 {
		t.Errorf("agent saw %d calls, %d streams; want 1 streaming call", calls, streams)
	}
}

func TestTaskThatEndsBadly(t *testing.T) {
	tests := []struct {
		state sdk.TaskState
		want  string
	}{
		{sdk.TaskStateFailed, "ended failed: quota exceeded"},
		{sdk.TaskStateRejected, "ended rejected: quota exceeded"},
		{sdk.TaskStateCanceled, "ended canceled: quota exceeded"},
		{sdk.TaskStateInputRequired, "asked for more input"},
	}
	for _, tt := range tests {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s streaming=%v", stateWord(tt.state), streaming), func(t *testing.T) {
				ta := startAgent(t, agentOptions{streaming: streaming, execute: endWith(tt.state, "quota exceeded")})
				c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
				res, err := c.Call(context.Background(), "a2a.research.summarize", message("go"))
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				if !res.IsError || !strings.Contains(res.Text, tt.want) {
					t.Errorf("Call() = %+v, want IsError and %q", res, tt.want)
				}
			})
		}
	}
}

func TestCallTimeout(t *testing.T) {
	// release lets the stuck agent finish when the test ends, so the server
	// can shut down. Cleanups run last-added first, so this runs before the
	// server's Close.
	release := make(chan struct{})
	stuck := func(ctx context.Context, ec *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool) {
		if !yield(sdk.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		if !yield(sdk.NewStatusUpdateEvent(ec, sdk.TaskStateWorking, nil), nil) {
			return
		}
		select {
		case <-ctx.Done():
		case <-release:
		}
	}
	ta := startAgent(t, agentOptions{streaming: true, execute: stuck})
	t.Cleanup(func() { close(release) })
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}, Timeout: 300 * time.Millisecond})

	start := time.Now()
	_, err := c.Call(context.Background(), "a2a.research.summarize", message("go"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want context.DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Call took %v, want about the 300ms timeout", took)
	}
	// Meru asks the agent to stop the task it started.
	select {
	case <-ta.cancelled:
	case <-time.After(5 * time.Second):
		t.Error("the agent never got a cancel request")
	}
	// A timeout doesn't mark the agent unreachable.
	if st := c.Status(); !st[0].Connected {
		t.Errorf("Status() = %+v, want still connected", st[0])
	}
}

func TestCallCancelled(t *testing.T) {
	release := make(chan struct{})
	stuck := func(ctx context.Context, ec *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool) {
		if !yield(sdk.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		select {
		case <-ctx.Done():
		case <-release:
		}
	}
	ta := startAgent(t, agentOptions{streaming: true, execute: stuck})
	t.Cleanup(func() { close(release) })
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The user presses Esc 200ms into the call.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := c.Call(ctx, "a2a.research.summarize", message("go"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() error = %v, want context.Canceled", err)
	}
}

func TestCallNotAllowed(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	for _, name := range []string{"a2a.research.delete_all", "a2a.other.summarize", "summarize"} {
		if _, err := c.Call(context.Background(), name, message("go")); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Call(%q) error = %v, want ErrNotAllowed", name, err)
		}
	}
	if hits, calls, _, _ := ta.snapshot(); hits != 0 || calls != 0 {
		t.Errorf("agent saw %d card fetches and %d calls, want none", hits, calls)
	}
}

func TestCallBadArguments(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	for _, args := range []string{``, `"text"`, `{}`, `{"message": "  "}`, `{"message": 3}`} {
		if _, err := c.Call(context.Background(), "a2a.research.summarize", json.RawMessage(args)); err == nil {
			t.Errorf("Call(%s) = nil error, want one", args)
		}
	}
}

func TestHeadersOnEveryRequest(t *testing.T) {
	ta := startAgent(t, agentOptions{streaming: true})
	c := newClient(t, AgentConfig{
		URL:     ta.srv.URL,
		Allow:   []string{"summarize"},
		Headers: map[string]string{"Authorization": "Bearer s3cret", "X-Team": "home"},
	})

	c.Tools()
	_, _, _, h := ta.snapshot()
	if h.Get("Authorization") != "Bearer s3cret" || h.Get("X-Team") != "home" {
		t.Errorf("card request headers = %v, want the configured ones", h)
	}
	if _, err := c.Call(context.Background(), "a2a.research.summarize", message("go")); err != nil {
		t.Fatalf("Call: %v", err)
	}
	_, _, _, h = ta.snapshot()
	if h.Get("Authorization") != "Bearer s3cret" || h.Get("X-Team") != "home" {
		t.Errorf("call request headers = %v, want the configured ones", h)
	}
}

func TestCardCannotPointOffMachine(t *testing.T) {
	// The card sits on loopback, but it names an agent URL on another
	// machine (192.0.2.1 is TEST-NET-1, reserved for documentation). Without
	// remote = true the dialer must refuse it before any packet leaves.
	ta := startAgent(t, agentOptions{interfaceURL: "http://192.0.2.1:9/"})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})

	_, err := c.Call(context.Background(), "a2a.research.summarize", message("go"))
	if !errors.Is(err, errNotLoopback) {
		t.Fatalf("Call() error = %v, want errNotLoopback", err)
	}
}

func TestRefuseNonLoopback(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:80", true},
		{"127.3.4.5:80", true},
		{"[::1]:80", true},
		{"[::ffff:127.0.0.1]:80", true},
		{"192.0.2.1:80", false},
		{"10.0.0.1:80", false},
		{"[2001:db8::1]:80", false},
		{"0.0.0.0:80", false},
		{"not-an-address", false},
	}
	for _, tt := range tests {
		err := refuseNonLoopback("tcp", tt.addr, nil)
		if (err == nil) != tt.ok {
			t.Errorf("refuseNonLoopback(%q) = %v, want ok=%v", tt.addr, err, tt.ok)
		}
	}
}

func TestRetryWait(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	ta.setCardFails(true)
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	c.retryAfter = time.Hour

	if tools := c.Tools(); len(tools) != 0 {
		t.Fatalf("Tools() = %v, want none while the card fails", tools)
	}
	st := c.Status()
	if st[0].Connected || !strings.Contains(st[0].LastError, "503") {
		t.Errorf("Status() = %+v, want disconnected with the 503", st[0])
	}
	_, err := c.Call(context.Background(), "a2a.research.summarize", message("go"))
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "next try in") {
		t.Errorf("Call() error = %v, want ErrUnavailable with the wait", err)
	}
	// Tools, Status and Call inside the wait don't fetch again.
	if hits, _, _, _ := ta.snapshot(); hits != 1 {
		t.Errorf("card fetched %d times inside the wait, want 1", hits)
	}

	// Once the wait has passed, the next use fetches again and succeeds.
	ta.setCardFails(false)
	c.retryAfter = 0
	if tools := c.Tools(); len(tools) != 1 {
		t.Fatalf("Tools() = %v, want the summarize tool after the agent recovered", tools)
	}
	if st := c.Status(); !st[0].Connected || st[0].LastError != "" {
		t.Errorf("Status() = %+v, want connected with no error", st[0])
	}
}

func TestUnreachableAgentRefetchesCard(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	if _, err := c.Call(context.Background(), "a2a.research.summarize", message("go")); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	ta.srv.Close()

	_, err := c.Call(context.Background(), "a2a.research.summarize", message("go"))
	if err == nil {
		t.Fatal("Call to a stopped agent succeeded")
	}
	st := c.Status()
	if st[0].Connected || st[0].LastError == "" {
		t.Errorf("Status() = %+v, want disconnected with an error", st[0])
	}
}

func TestCloseStopsCalls(t *testing.T) {
	ta := startAgent(t, agentOptions{})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	c.Tools()
	c.Close()
	if _, err := c.Call(context.Background(), "a2a.research.summarize", message("go")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Call after Close error = %v, want ErrUnavailable", err)
	}
}
