// This file tests Handle with a fake engine and a fake router: the order of
// events, the transcript lines, history, errors and cancellation.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// fakeEngine streams a fixed answer and remembers what it was asked. Only
// Stream does real work; the other methods satisfy the Engine interface.
type fakeEngine struct {
	pieces    []string
	usage     engine.Usage
	streamErr error // returned by Stream itself
	midErr    error // yielded after the first piece
	// block, when true, makes the stream wait for ctx to end after the
	// first piece, to test cancellation.
	block bool
	// gen is what Generate returns, for tests that run the real router.
	gen engine.Completion

	mu    sync.Mutex // guards calls
	calls []streamCall
}

// streamCall records one Stream call.
type streamCall struct {
	msgs []engine.Message
	opts engine.Options
}

func (f *fakeEngine) Stream(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (iter.Seq2[engine.Delta, error], error) {
	f.mu.Lock()
	f.calls = append(f.calls, streamCall{msgs: slices.Clone(msgs), opts: opts})
	f.mu.Unlock()
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return func(yield func(engine.Delta, error) bool) {
		for i, p := range f.pieces {
			if !yield(engine.Delta{Text: p}, nil) {
				return
			}
			if i == 0 && f.midErr != nil {
				yield(engine.Delta{}, f.midErr)
				return
			}
			if i == 0 && f.block {
				<-ctx.Done()
				yield(engine.Delta{}, ctx.Err())
				return
			}
		}
		yield(engine.Delta{Done: true, DoneReason: "stop", Usage: f.usage}, nil)
	}, nil
}

// Generate returns f.gen, the canned completion the real router reads, or
// an error when the test set none.
func (f *fakeEngine) Generate(context.Context, []engine.Message, []engine.ToolSpec, engine.Options) (engine.Completion, error) {
	if f.gen.LogProbs == nil {
		return engine.Completion{}, errors.New("not used")
	}
	return f.gen, nil
}

func (f *fakeEngine) Embed(context.Context, []string) ([]engine.Vector, error) {
	return nil, errors.New("not used")
}

func (f *fakeEngine) Info(context.Context) (engine.ModelInfo, error) {
	return engine.ModelInfo{Runtime: "fake"}, nil
}

// lastCall returns the most recent Stream call.
func (f *fakeEngine) lastCall() streamCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// fakeRouter returns a fixed decision and remembers the history it saw.
type fakeRouter struct {
	dec Decision
	err error

	mu      sync.Mutex // guards history
	history []engine.Message
}

func (r *fakeRouter) Decide(ctx context.Context, question string, history []engine.Message) (Decision, error) {
	r.mu.Lock()
	r.history = slices.Clone(history)
	r.mu.Unlock()
	return r.dec, r.err
}

// testConfig returns a valid config whose home is a fresh temp directory.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// quietLog returns a logger that discards everything.
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// run calls Handle and collects the events it emits.
func run(ctx context.Context, a *Agent, req rpc.Request) ([]rpc.Event, error) {
	var evs []rpc.Event
	err := a.Handle(ctx, req, func(ev rpc.Event) error {
		evs = append(evs, ev)
		return nil
	}, nil)
	return evs, err
}

// readLines parses a session file into transcript lines.
func readLines(t *testing.T, cfg config.Config, id string) []transcript.Line {
	t.Helper()
	s, err := transcript.Open(filepath.Join(cfg.Dir, "sessions"), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var lines []transcript.Line
	for _, row := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var l transcript.Line
		if err := json.Unmarshal([]byte(row), &l); err != nil {
			t.Fatalf("bad transcript line %q: %v", row, err)
		}
		lines = append(lines, l)
	}
	return lines
}

func TestTurnEventsAndTranscript(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{pieces: []string{"Hel", "lo", "!"}, usage: engine.Usage{PromptTokens: 42, OutputTokens: 3, EvalDuration: 250 * time.Millisecond}}
	router := &fakeRouter{dec: Decision{Route: "search", Confidence: 0.8, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "  hi  ", Source: rpc.SourceCLI})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(evs) != 6 {
		t.Fatalf("got %d events, want 6: %+v", len(evs), evs)
	}
	id := evs[0].Session
	want := []rpc.Event{
		{Type: rpc.EventSession, Session: id},
		{Type: rpc.EventRoute, Route: "search", Confidence: 0.8},
		{Type: rpc.EventToken, Text: "Hel"},
		{Type: rpc.EventToken, Text: "lo"},
		{Type: rpc.EventToken, Text: "!"},
	}
	if id == "" || !reflect.DeepEqual(evs[:5], want) {
		t.Errorf("events = %+v\nwant %+v", evs[:5], want)
	}
	// The last event is "done" with the turn's stats. The times depend on
	// the clock, so the test checks only that they make sense.
	done := evs[5]
	if done.Type != rpc.EventDone || done.TokensIn != 42 || done.TokensOut != 3 || done.EvalMillis != 250 {
		t.Errorf("last event = %+v, want done with 42 tokens in, 3 out and 250ms writing", done)
	}
	if done.TTFTMillis < 0 || done.DurationMillis < done.TTFTMillis {
		t.Errorf("done times = ttft %dms, duration %dms; want 0 <= ttft <= duration", done.TTFTMillis, done.DurationMillis)
	}

	lines := readLines(t, cfg, id)
	if len(lines) != 2 {
		t.Fatalf("transcript has %d lines, want 2", len(lines))
	}
	if l := lines[0]; l.Type != "user" || l.Text != "hi" {
		t.Errorf("line 1 = %+v, want the user question", l)
	}
	if l := lines[1]; l.Type != "assistant" || l.Text != "Hello!" || l.TokensIn != 42 || l.TokensOut != 3 {
		t.Errorf("line 2 = %+v, want the answer with token counts", l)
	}

	call := eng.lastCall()
	if call.opts.Model != cfg.Models.Main {
		t.Errorf("model = %q, want the main tier %q", call.opts.Model, cfg.Models.Main)
	}
	wantMsgs := []engine.Message{
		{Role: engine.RoleSystem, Content: DefaultSystemPrompt + "\n\n" + filesNote(nil)},
		{Role: engine.RoleUser, Content: "hi"},
	}
	if !slices.EqualFunc(call.msgs, wantMsgs, sameMessage) {
		t.Errorf("prompt = %+v\nwant %+v", call.msgs, wantMsgs)
	}
}

// sameMessage compares the fields the agent sets.
func sameMessage(a, b engine.Message) bool { return a.Role == b.Role && a.Content == b.Content }

func TestTurnContinuesSession(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agent.SystemPrompt = "Be brief."
	eng := &fakeEngine{pieces: []string{"one"}}
	router := &fakeRouter{dec: Decision{Route: "direct", Confidence: 1, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	id := evs[0].Session

	eng.pieces = []string{"two"}
	evs, err = run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Session: id, Text: "second", Source: rpc.SourceTUI})
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].Session != id {
		t.Errorf("second turn went to session %s, want %s", evs[0].Session, id)
	}

	history := []engine.Message{
		{Role: engine.RoleUser, Content: "first"},
		{Role: engine.RoleAssistant, Content: "one"},
	}
	if !slices.EqualFunc(router.history, history, sameMessage) {
		t.Errorf("router saw history %+v, want %+v", router.history, history)
	}
	wantMsgs := append([]engine.Message{{Role: engine.RoleSystem, Content: "Be brief.\n\n" + filesNote(nil)}}, history...)
	wantMsgs = append(wantMsgs, engine.Message{Role: engine.RoleUser, Content: "second"})
	if got := eng.lastCall().msgs; !slices.EqualFunc(got, wantMsgs, sameMessage) {
		t.Errorf("prompt = %+v\nwant %+v", got, wantMsgs)
	}
	if n := len(readLines(t, cfg, id)); n != 4 {
		t.Errorf("transcript has %d lines, want 4", n)
	}
}

func TestTurnErrors(t *testing.T) {
	boom := errors.New("boom")
	ok := Decision{Route: "direct", Confidence: 1, Outcome: "ok"}
	tests := []struct {
		name   string
		req    rpc.Request
		eng    *fakeEngine
		router *fakeRouter
		want   string
	}{
		{"empty question", rpc.Request{Text: "   "}, &fakeEngine{}, &fakeRouter{dec: ok}, "empty question"},
		{"unknown source", rpc.Request{Text: "q", Source: "web"}, &fakeEngine{}, &fakeRouter{dec: ok}, "unknown source"},
		{"bad session", rpc.Request{Text: "q", Session: "../x"}, &fakeEngine{}, &fakeRouter{dec: ok}, "isn't valid"},
		{"missing session", rpc.Request{Text: "q", Session: "2026-01-01T000000-abcd"}, &fakeEngine{}, &fakeRouter{dec: ok}, "open session"},
		{"router fails", rpc.Request{Text: "q"}, &fakeEngine{}, &fakeRouter{err: boom}, "route: boom"},
		{"stream fails", rpc.Request{Text: "q"}, &fakeEngine{streamErr: boom}, &fakeRouter{dec: ok}, "boom"},
		{"stream breaks", rpc.Request{Text: "q"}, &fakeEngine{pieces: []string{"a", "b"}, midErr: boom}, &fakeRouter{dec: ok}, "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(testConfig(t), tt.eng, tt.router, nil, quietLog())
			_, err := run(context.Background(), a, tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Handle error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestTurnStopsWhenEmitFails(t *testing.T) {
	a := New(testConfig(t), &fakeEngine{pieces: []string{"a", "b"}}, &fakeRouter{dec: Decision{Route: "direct"}}, nil, quietLog())
	gone := errors.New("client gone")
	n := 0
	err := a.Handle(context.Background(), rpc.Request{Text: "q"}, func(ev rpc.Event) error {
		n++
		if ev.Type == rpc.EventToken {
			return gone
		}
		return nil
	}, nil)
	if !errors.Is(err, gone) {
		t.Errorf("Handle error = %v, want %v", err, gone)
	}
	if n != 3 { // session, route, first token
		t.Errorf("emit called %d times, want 3", n)
	}
}

func TestTurnCancelled(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{pieces: []string{"partial", "never"}, block: true}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 1, Outcome: "ok"}}, nil, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var id string
	err := a.Handle(ctx, rpc.Request{Text: "q"}, func(ev rpc.Event) error {
		switch ev.Type {
		case rpc.EventSession:
			id = ev.Session
		case rpc.EventToken:
			cancel() // as if the client hung up after the first token
		}
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle error = %v, want context.Canceled", err)
	}
	lines := readLines(t, cfg, id)
	if len(lines) != 1 || lines[0].Type != "user" {
		t.Errorf("transcript = %+v, want only the user line", lines)
	}
}

// TestRouteFallbackFlag checks that a route event says when the router fell
// back, so the chat screen can mark it.
func TestRouteFallbackFlag(t *testing.T) {
	tests := []struct {
		outcome string
		want    bool
	}{
		{"ok", false},
		{"low_confidence", true},
		{"degraded", true},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"x"}}
			router := &fakeRouter{dec: Decision{Route: "search+tools", Confidence: 0.3, Outcome: tt.outcome}}
			a := New(testConfig(t), eng, router, nil, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "hi"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(evs) < 2 || evs[1].Type != rpc.EventRoute || evs[1].Fallback != tt.want {
				t.Errorf("route event = %+v, want fallback %v", evs[1], tt.want)
			}
		})
	}
}

func TestOutcomeOf(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"ok", context.Background(), nil, "ok"},
		{"error", context.Background(), errors.New("x"), "error"},
		{"cancelled ctx", cancelled, errors.New("x"), "cancelled"},
		{"cancel error", context.Background(), context.Canceled, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeOf(tt.ctx, tt.err); got != tt.want {
				t.Errorf("outcomeOf = %q, want %q", got, tt.want)
			}
		})
	}
}
