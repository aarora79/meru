// This file tests Dispatch with fake backends, a fake recorder and an
// in-memory transcript: each outcome, the approval rules, redaction, the
// cap on results, duplicate tool names, Replace, calls running side by
// side, and the caller and hint the agent sets.

package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// fakeBackend is a Backend whose tools, confirm rules and call results the
// test sets.
type fakeBackend struct {
	kind    string
	tools   []string                                  // full names
	confirm map[string]Confirm                        // missing means ConfirmNever
	call    func(ctx context.Context) (Result, error) // nil answers "ok"

	mu    sync.Mutex // guards calls
	calls int
}

// Kind returns the kind the test set.
func (b *fakeBackend) Kind() string { return b.kind }

// Tools returns one ToolSpec per name in b.tools.
func (b *fakeBackend) Tools() []engine.ToolSpec {
	var out []engine.ToolSpec
	for _, n := range b.tools {
		out = append(out, engine.ToolSpec{Name: n, Description: b.kind + " tool"})
	}
	return out
}

// Confirm returns the rule the test set for name; a missing entry reads as
// ConfirmNever, the zero value.
func (b *fakeBackend) Confirm(name string) Confirm { return b.confirm[name] }

// Locate splits at the first '.', or returns "meru" for a built-in.
func (b *fakeBackend) Locate(name string) (string, string) {
	if b.kind == KindBuiltin {
		return "meru", name
	}
	server, tool, _ := strings.Cut(name, ".")
	return server, tool
}

// Call counts the call and answers with b.call, or "ok from <name>".
func (b *fakeBackend) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	if b.call == nil {
		return Result{Text: "ok from " + name}, nil
	}
	return b.call(ctx)
}

// Status reports one connected server named after the kind.
func (b *fakeBackend) Status() []rpc.ServerInfo {
	return []rpc.ServerInfo{{Name: b.kind + "-server", Kind: b.kind, Connected: true}}
}

// callCount returns how many times Call ran.
func (b *fakeBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// fakeRecorder keeps the rows it gets.
type fakeRecorder struct {
	mu   sync.Mutex // guards rows
	rows []store.ToolCall
	err  error
}

// InsertToolCall keeps row, then returns r.err.
func (r *fakeRecorder) InsertToolCall(ctx context.Context, row store.ToolCall) error {
	if ctx.Err() != nil {
		return ctx.Err() // a real store fails on an ended ctx too
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, row)
	return r.err
}

// sink collects transcript lines in memory.
type sink struct {
	mu    sync.Mutex // guards lines
	lines []transcript.Line
	err   error
}

// append keeps l, or fails with s.err.
func (s *sink) append(l transcript.Line) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.lines = append(s.lines, l)
	return nil
}

// types returns the Type of each line, in order.
func (s *sink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.lines {
		out = append(out, l.Type)
	}
	return out
}

// approver answers approval prompts with a fixed choice and keeps what it
// was asked.
type approver struct {
	mu     sync.Mutex // guards asked
	choice rpc.Choice
	err    error
	asked  []rpc.Approval
}

// approve keeps ap and answers a.choice, or fails with a.err.
func (a *approver) approve(ctx context.Context, ap rpc.Approval) (rpc.Choice, error) {
	a.mu.Lock()
	a.asked = append(a.asked, ap)
	a.mu.Unlock()
	if a.err != nil {
		return "", a.err
	}
	return a.choice, nil
}

// askCount returns how many prompts the approver saw.
func (a *approver) askCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.asked)
}

// newCall builds a Call to name in session "s1" that writes to tr and asks
// ap (nil for nobody).
func newCall(name string, tr *sink, ap *approver) Call {
	c := Call{ID: "c1", Name: name, Args: json.RawMessage(`{"q": "x"}`), Session: "s1",
		Source: rpc.SourceTUI, Append: tr.append, TraceID: "trace-1"}
	if ap != nil {
		c.Approve = ap.approve
	}
	return c
}

func TestDispatchOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		confirm     Confirm
		call        func(ctx context.Context) (Result, error)
		ap          *approver
		edit        func(c *Call)
		ctxTimeout  time.Duration // > 0 gives the call's ctx a deadline
		cancel      bool          // cancel ctx before dispatching
		wantOutcome string
		wantRan     bool
		wantAsks    int
		wantLines   []string
		wantApprove string
		wantText    string // a piece of the result text
	}{
		{name: "allowed", tool: "web.search", wantOutcome: OutcomeOK, wantRan: true,
			wantLines: []string{"tool_call", "tool_result"}, wantText: "ok from web.search"},
		{name: "unknown tool is denied", tool: "web.delete_everything", wantOutcome: OutcomeDenied,
			wantLines: []string{"tool_call", "tool_result"}, wantText: "isn't available"},
		{name: "ask once", tool: "web.search", confirm: ConfirmAsk, ap: &approver{choice: rpc.ChoiceOnce},
			wantOutcome: OutcomeOK, wantRan: true, wantAsks: 1, wantApprove: "once",
			wantLines: []string{"tool_call", "approval", "tool_result"}},
		{name: "deny", tool: "web.search", confirm: ConfirmAsk, ap: &approver{choice: rpc.ChoiceDeny},
			wantOutcome: OutcomeDeclined, wantAsks: 1, wantApprove: "deny",
			wantLines: []string{"tool_call", "approval", "tool_result"}, wantText: "said no"},
		{name: "a choice not offered counts as deny", tool: "web.search", confirm: ConfirmAlways,
			ap: &approver{choice: rpc.ChoiceSession}, wantOutcome: OutcomeDeclined, wantAsks: 1, wantApprove: "deny",
			wantLines: []string{"tool_call", "approval", "tool_result"}},
		{name: "nobody to ask", tool: "web.search", confirm: ConfirmAsk, wantOutcome: OutcomeDeclined,
			wantLines: []string{"tool_call", "tool_result"}, wantText: "nobody can give it"},
		{name: "a job never asks", tool: "web.search", confirm: ConfirmAsk, ap: &approver{choice: rpc.ChoiceOnce},
			edit: func(c *Call) { c.Source = rpc.SourceJob }, wantOutcome: OutcomeDeclined,
			wantLines: []string{"tool_call", "tool_result"}},
		{name: "client gone while asking", tool: "web.search", confirm: ConfirmAsk,
			ap: &approver{err: errors.New("connection reset")}, wantOutcome: OutcomeDeclined, wantAsks: 1,
			wantLines: []string{"tool_call", "tool_result"}},
		{name: "cancelled while asking", tool: "web.search", confirm: ConfirmAsk, cancel: true,
			ap: &approver{err: context.Canceled}, wantOutcome: OutcomeCancelled, wantAsks: 1,
			wantLines: []string{"tool_call", "tool_result"}},
		{name: "tool reports an error", tool: "web.search",
			call:        func(context.Context) (Result, error) { return Result{Text: "no such page", IsError: true}, nil },
			wantOutcome: OutcomeError, wantRan: true, wantLines: []string{"tool_call", "tool_result"}, wantText: "no such page"},
		{name: "call fails", tool: "web.search",
			call:        func(context.Context) (Result, error) { return Result{}, errors.New("server went away") },
			wantOutcome: OutcomeError, wantRan: true, wantLines: []string{"tool_call", "tool_result"}, wantText: "server went away"},
		{name: "backend timeout", tool: "web.search",
			call: func(ctx context.Context) (Result, error) {
				return Result{}, errors.Join(errors.New("call web.search"), context.DeadlineExceeded)
			},
			wantOutcome: OutcomeTimeout, wantRan: true, wantLines: []string{"tool_call", "tool_result"}, wantText: "in time"},
		{name: "turn deadline counts as cancelled", tool: "web.search", ctxTimeout: time.Millisecond,
			call: func(ctx context.Context) (Result, error) {
				<-ctx.Done()
				return Result{}, ctx.Err()
			},
			wantOutcome: OutcomeCancelled, wantRan: true, wantLines: []string{"tool_call", "tool_result"}},
		{name: "cancelled mid-call", tool: "web.search", cancel: true,
			call:        func(ctx context.Context) (Result, error) { return Result{}, ctx.Err() },
			wantOutcome: OutcomeCancelled, wantRan: true, wantLines: []string{"tool_call", "tool_result"}},
		{name: "transcript write fails", tool: "web.search", edit: func(c *Call) {
			c.Append = func(transcript.Line) error { return errors.New("disk full") }
		}, wantOutcome: OutcomeError, wantText: "couldn't record"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{kind: KindMCP, tools: []string{"web.search"},
				confirm: map[string]Confirm{"web.search": tt.confirm}, call: tt.call}
			rec := &fakeRecorder{}
			d := New([]Backend{b}, rec, Options{})
			tr := &sink{}
			c := newCall(tt.tool, tr, tt.ap)
			if tt.edit != nil {
				tt.edit(&c)
			}
			ctx := context.Background()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			res, out := d.Dispatch(ctx, c)

			if out.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q (result %q)", out.Outcome, tt.wantOutcome, res.Text)
			}
			if ran := b.callCount() > 0; ran != tt.wantRan {
				t.Errorf("backend ran = %v, want %v", ran, tt.wantRan)
			}
			if !tt.wantRan && out.Duration != 0 {
				t.Errorf("duration = %v for a call that never ran, want 0", out.Duration)
			}
			if tt.wantOutcome != OutcomeOK && !res.IsError {
				t.Errorf("IsError = false for outcome %q", tt.wantOutcome)
			}
			if tt.ap != nil && tt.ap.askCount() != tt.wantAsks {
				t.Errorf("asked %d times, want %d", tt.ap.askCount(), tt.wantAsks)
			}
			if got := strings.Join(tr.types(), ","); got != strings.Join(tt.wantLines, ",") {
				t.Errorf("transcript lines = %s, want %s", got, strings.Join(tt.wantLines, ","))
			}
			if tt.wantText != "" && !strings.Contains(res.Text, tt.wantText) {
				t.Errorf("result %q doesn't contain %q", res.Text, tt.wantText)
			}

			if len(rec.rows) != 1 {
				t.Fatalf("recorder got %d rows, want 1", len(rec.rows))
			}
			row := rec.rows[0]
			if row.Outcome != tt.wantOutcome || row.Approval != tt.wantApprove || row.Session != "s1" ||
				row.CallID != "c1" || row.TraceID != "trace-1" || row.Kind != KindMCP || row.Server != "web" {
				t.Errorf("row = %+v", row)
			}
			if len(tr.lines) > 0 {
				last := tr.lines[len(tr.lines)-1]
				if last.Outcome != tt.wantOutcome || last.OK != (tt.wantOutcome == OutcomeOK) || last.CallID != "c1" {
					t.Errorf("tool_result line = %+v", last)
				}
			}
		})
	}
}

// TestSessionApproval walks the approval rules across several calls:
// "once" asks again, "session" stops asking in that session only, and
// ConfirmAlways ignores an earlier session approval.
func TestSessionApproval(t *testing.T) {
	b := &fakeBackend{kind: KindMCP, tools: []string{"web.search", "web.fetch", "web.post"},
		confirm: map[string]Confirm{"web.search": ConfirmAsk, "web.fetch": ConfirmAsk, "web.post": ConfirmAlways}}
	d := New([]Backend{b}, &fakeRecorder{}, Options{})
	ctx := context.Background()

	steps := []struct {
		tool     string
		session  string
		choice   rpc.Choice
		wantAsk  bool
		wantOffr string // the choices offered, when asked
		wantOut  string
	}{
		{"web.search", "s1", rpc.ChoiceOnce, true, "once,session,deny", OutcomeOK},
		{"web.search", "s1", rpc.ChoiceSession, true, "once,session,deny", OutcomeOK}, // once didn't stick
		{"web.search", "s1", rpc.ChoiceDeny, false, "", OutcomeOK},                    // session did
		{"web.search", "s2", rpc.ChoiceDeny, true, "once,session,deny", OutcomeDeclined},
		{"web.fetch", "s1", rpc.ChoiceDeny, true, "once,session,deny", OutcomeDeclined}, // per tool
		{"web.post", "s1", rpc.ChoiceOnce, true, "once,deny", OutcomeOK},
		{"web.post", "s1", rpc.ChoiceOnce, true, "once,deny", OutcomeOK}, // always asks
	}
	for i, s := range steps {
		ap := &approver{choice: s.choice}
		tr := &sink{}
		c := newCall(s.tool, tr, ap)
		c.Session = s.session
		_, out := d.Dispatch(ctx, c)
		if out.Outcome != s.wantOut {
			t.Errorf("step %d (%s in %s): outcome %q, want %q", i, s.tool, s.session, out.Outcome, s.wantOut)
		}
		if asked := ap.askCount() == 1; asked != s.wantAsk {
			t.Errorf("step %d (%s in %s): asked = %v, want %v", i, s.tool, s.session, asked, s.wantAsk)
			continue
		}
		if s.wantAsk {
			var offered []string
			for _, ch := range ap.asked[0].Choices {
				offered = append(offered, string(ch))
			}
			if got := strings.Join(offered, ","); got != s.wantOffr {
				t.Errorf("step %d: offered %s, want %s", i, got, s.wantOffr)
			}
		}
	}

	// A session approval of a ConfirmAlways tool can't happen through the
	// prompt, but even one recorded by hand doesn't skip the question.
	d.approveForSession("s1", "web.post")
	ap := &approver{choice: rpc.ChoiceDeny}
	if _, out := d.Dispatch(ctx, newCall("web.post", &sink{}, ap)); out.Outcome != OutcomeDeclined || ap.askCount() != 1 {
		t.Errorf("ConfirmAlways after a session approval: outcome %q, asked %d; want declined after one ask",
			out.Outcome, ap.askCount())
	}
}

func TestRedaction(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "hunter2", "[redacted]") }
	b := &fakeBackend{kind: KindMCP, tools: []string{"web.search"},
		confirm: map[string]Confirm{"web.search": ConfirmAsk},
		call: func(context.Context) (Result, error) {
			return Result{}, errors.New("auth failed for key hunter2")
		}}
	rec := &fakeRecorder{}
	d := New([]Backend{b}, rec, Options{Redact: redact})
	tr := &sink{}
	ap := &approver{choice: rpc.ChoiceOnce}
	c := newCall("web.search", tr, ap)
	c.Args = json.RawMessage(`{"key": "hunter2"}`)

	res, _ := d.Dispatch(context.Background(), c)

	if strings.Contains(res.Text, "hunter2") {
		t.Errorf("result for the model holds the secret: %q", res.Text)
	}
	for _, l := range tr.lines {
		j, _ := json.Marshal(l)
		if strings.Contains(string(j), "hunter2") {
			t.Errorf("transcript line holds the secret: %s", j)
		}
	}
	if string(tr.lines[0].Args) != `{"key":"[redacted]"}` {
		t.Errorf("tool_call args = %s, want the redacted, compact JSON", tr.lines[0].Args)
	}
	if strings.Contains(string(ap.asked[0].Args), "hunter2") {
		t.Errorf("approval prompt holds the secret: %s", ap.asked[0].Args)
	}
	row := rec.rows[0]
	if strings.Contains(string(row.Args), "hunter2") || strings.Contains(row.Result, "hunter2") {
		t.Errorf("row holds the secret: %+v", row)
	}
}

// TestSessionOnContext checks that the backend can read the call's session
// from its ctx, and that a ctx outside a call carries none.
func TestSessionOnContext(t *testing.T) {
	var got string
	b := &fakeBackend{kind: KindBuiltin, tools: []string{"remember"},
		call: func(ctx context.Context) (Result, error) {
			got = SessionFrom(ctx)
			return Result{Text: "saved"}, nil
		}}
	d := New([]Backend{b}, nil, Options{})
	d.Dispatch(context.Background(), newCall("remember", &sink{}, nil))
	if got != "s1" {
		t.Errorf("SessionFrom in the backend = %q, want s1", got)
	}
	if s := SessionFrom(context.Background()); s != "" {
		t.Errorf("SessionFrom outside a call = %q, want empty", s)
	}
}

// TestCiteNumbersAndSources checks that a backend can reserve citation
// numbers through the counter the caller put on ctx, that Dispatch hands
// the backend's Sources back unchanged, and that CiteNumbers outside a
// turn reserves nothing and returns 1.
func TestCiteNumbersAndSources(t *testing.T) {
	b := &fakeBackend{kind: KindBuiltin, tools: []string{"search_files"},
		call: func(ctx context.Context) (Result, error) {
			first := CiteNumbers(ctx, 2)
			return Result{Text: "two excerpts", Sources: []rpc.Citation{
				{N: first, Path: "~/a.md"}, {N: first + 1, Path: "~/b.md"},
			}}, nil
		}}
	d := New([]Backend{b}, nil, Options{})
	used := 10 // ten numbers already went to excerpts in the prompt
	ctx := WithCiteNumbers(context.Background(), func(n int) int {
		first := used + 1
		used += n
		return first
	})
	res, out := d.Dispatch(ctx, newCall("search_files", &sink{}, nil))
	if out.Outcome != OutcomeOK {
		t.Fatalf("outcome = %s, want ok", out.Outcome)
	}
	if len(res.Sources) != 2 || res.Sources[0].N != 11 || res.Sources[1].N != 12 || res.Sources[1].Path != "~/b.md" {
		t.Errorf("sources = %+v, want [11] ~/a.md and [12] ~/b.md", res.Sources)
	}
	if used != 12 {
		t.Errorf("counter at %d after the call, want 12", used)
	}
	if n := CiteNumbers(context.Background(), 3); n != 1 {
		t.Errorf("CiteNumbers outside a turn = %d, want 1", n)
	}
}

func TestBadArgsStillEncode(t *testing.T) {
	b := &fakeBackend{kind: KindMCP, tools: []string{"web.search"}}
	d := New([]Backend{b}, &fakeRecorder{}, Options{})
	tr := &sink{}
	c := newCall("web.search", tr, nil)
	c.Args = json.RawMessage(`{"q": oops`)
	d.Dispatch(context.Background(), c)
	if _, err := json.Marshal(tr.lines[0]); err != nil {
		t.Errorf("tool_call line with broken args doesn't encode: %v", err)
	}
	if string(tr.lines[0].Args) != `"{\"q\": oops"` {
		t.Errorf("args = %s, want the text as one JSON string", tr.lines[0].Args)
	}
}

func TestResultCaps(t *testing.T) {
	long := strings.Repeat("ü", MaxModelResult+500)
	b := &fakeBackend{kind: KindMCP, tools: []string{"web.fetch"},
		call: func(context.Context) (Result, error) { return Result{Text: long}, nil }}
	rec := &fakeRecorder{}
	d := New([]Backend{b}, rec, Options{})
	tr := &sink{}

	res, _ := d.Dispatch(context.Background(), newCall("web.fetch", tr, nil))

	if !strings.HasPrefix(res.Text, strings.Repeat("ü", MaxModelResult)+"\n[Meru cut") {
		t.Errorf("model result isn't cut at %d characters with a note", MaxModelResult)
	}
	if !strings.Contains(res.Text, "16000 of 16500") {
		t.Errorf("cut note = %q, want it to give both lengths", res.Text[len(res.Text)-80:])
	}
	if n := len([]rune(tr.lines[1].Result)); n != maxLoggedResult {
		t.Errorf("transcript result has %d characters, want %d", n, maxLoggedResult)
	}
	if n := len([]rune(rec.rows[0].Result)); n != maxLoggedResult {
		t.Errorf("row result has %d characters, want %d", n, maxLoggedResult)
	}

	// A short result passes through untouched.
	b.call = func(context.Context) (Result, error) { return Result{Text: "short"}, nil }
	if res, _ := d.Dispatch(context.Background(), newCall("web.fetch", &sink{}, nil)); res.Text != "short" {
		t.Errorf("short result = %q", res.Text)
	}
}

// TestAttachments checks that Options.Attachments adds its text to MCP and
// A2A results that succeed, and to nothing else, and that the transcript
// and the row record the result with the text added and secrets removed.
func TestAttachments(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		tool  string
		fail  bool // the tool reports an error
		added bool
	}{
		{"mcp", KindMCP, "google.get_gmail_attachment_content", false, true},
		{"a2a", KindA2A, "a2a.mail.fetch", false, true},
		{"built-in", KindBuiltin, "read_file", false, false},
		{"command", KindCommand, "cmd.git_log", false, false},
		{"mcp error", KindMCP, "google.get_gmail_attachment_content", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{kind: tt.kind, tools: []string{tt.tool},
				call: func(context.Context) (Result, error) {
					return Result{Text: "Saved filename: folio.pdf", IsError: tt.fail}, nil
				}}
			var got time.Time
			attach := func(text string, since time.Time) string {
				got = since
				return "\n\nSaved at ~/meru-output/attachments/folio.pdf. Room 214, key hunter2."
			}
			redact := func(s string) string { return strings.ReplaceAll(s, "hunter2", "[secret]") }
			rec := &fakeRecorder{}
			d := New([]Backend{b}, rec, Options{Attachments: attach, Redact: redact})
			tr := &sink{}
			before := time.Now()
			res, _ := d.Dispatch(context.Background(), newCall(tt.tool, tr, nil))

			has := strings.Contains(res.Text, "Room 214")
			if has != tt.added {
				t.Fatalf("result = %q; attachment added = %v, want %v", res.Text, has, tt.added)
			}
			if !tt.added {
				return
			}
			if got.Before(before.Add(-time.Second)) || got.After(time.Now()) {
				t.Errorf("since = %v, want the time the call began", got)
			}
			for where, text := range map[string]string{"model": res.Text, "transcript": tr.lines[1].Result, "row": rec.rows[0].Result} {
				if !strings.Contains(text, "Room 214, key [secret].") {
					t.Errorf("%s result = %q, want the attachment text with the secret removed", where, text)
				}
			}
		})
	}
}

func TestDuplicateToolNames(t *testing.T) {
	first := &fakeBackend{kind: KindMCP, tools: []string{"notes.read", "web.search"}}
	second := &fakeBackend{kind: KindBuiltin, tools: []string{"notes.read", "remember"}}
	d := New([]Backend{first, second}, nil, Options{})

	var names []string
	for _, t := range d.Tools() {
		names = append(names, t.Name)
	}
	if got := strings.Join(names, ","); got != "notes.read,web.search,remember" {
		t.Errorf("Tools() = %s, want the first backend to keep notes.read", got)
	}
	d.Dispatch(context.Background(), newCall("notes.read", &sink{}, nil))
	if first.callCount() != 1 || second.callCount() != 0 {
		t.Errorf("calls: first %d, second %d; want the first backend to run it", first.callCount(), second.callCount())
	}
}

func TestReplaceAndServers(t *testing.T) {
	old := &fakeBackend{kind: KindMCP, tools: []string{"web.search"}}
	builtin := &fakeBackend{kind: KindBuiltin, tools: []string{"remember"}}
	d := New([]Backend{old, builtin}, nil, Options{})

	fresh := &fakeBackend{kind: KindMCP, tools: []string{"mail.send"}}
	d.Replace(KindMCP, fresh)

	if _, out := d.Dispatch(context.Background(), newCall("web.search", &sink{}, nil)); out.Outcome != OutcomeDenied {
		t.Errorf("old tool after Replace: outcome %q, want denied", out.Outcome)
	}
	if _, out := d.Dispatch(context.Background(), newCall("mail.send", &sink{}, nil)); out.Outcome != OutcomeOK || fresh.callCount() != 1 {
		t.Errorf("new tool after Replace: outcome %q, calls %d", out.Outcome, fresh.callCount())
	}

	// Replace with a kind that isn't there adds it at the end.
	d.Replace(KindA2A, &fakeBackend{kind: KindA2A, tools: []string{"a2a.helper.plan"}})
	var kinds []string
	for _, s := range d.Servers() {
		kinds = append(kinds, s.Kind)
	}
	if got := strings.Join(kinds, ","); got != "mcp,builtin,a2a" {
		t.Errorf("Servers() kinds = %s, want mcp,builtin,a2a", got)
	}
}

func TestDeniedCallLocation(t *testing.T) {
	tests := []struct {
		name               string
		kind, server, tool string
	}{
		{"a2a.helper.plan", KindA2A, "helper", "plan"},
		{"web.search.deep", KindMCP, "web", "search.deep"},
		{"launch_missiles", KindBuiltin, "meru", "launch_missiles"},
		{"cmd.rm-rf", KindCommand, "meru", "cmd.rm-rf"},
		{strings.Repeat("x", 500), KindBuiltin, "meru", strings.Repeat("x", maxDeniedName)},
	}
	for _, tt := range tests {
		kind, server, tool := guessLocation(tt.name)
		if kind != tt.kind || server != tt.server || tool != tt.tool {
			t.Errorf("guessLocation(%.20q) = %s, %s, %.20s; want %s, %s, %.20s",
				tt.name, kind, server, tool, tt.kind, tt.server, tt.tool)
		}
	}
}

// auditBackend is a fakeBackend that is also an Auditor: it records
// "audited" arguments in place of the model's, except for "{}".
type auditBackend struct {
	fakeBackend
}

// AuditArgs returns a fixed object, or nil for empty arguments. The
// struct embeds fakeBackend, so auditBackend gets all of its methods and
// adds this one.
func (b *auditBackend) AuditArgs(name string, args json.RawMessage) json.RawMessage {
	if string(args) == "{}" {
		return nil
	}
	return json.RawMessage(`{"argv": ["prog", "sk-secret-value-123"]}`)
}

// TestAuditor checks that dispatch records an Auditor's arguments in the
// tool_call line, the approval prompt and the row, redacted and compacted,
// and keeps the model's arguments when AuditArgs returns nil.
func TestAuditor(t *testing.T) {
	b := &auditBackend{fakeBackend{kind: KindCommand, tools: []string{"cmd.prog"}, confirm: map[string]Confirm{"cmd.prog": ConfirmAsk}}}
	rec := &fakeRecorder{}
	redact := func(s string) string { return strings.ReplaceAll(s, "sk-secret-value-123", "[secret:key]") }
	d := New([]Backend{b}, rec, Options{Redact: redact})
	s := &sink{}
	a := &approver{choice: rpc.ChoiceOnce}
	c := newCall("cmd.prog", s, a)
	c.Args = json.RawMessage(`{"x":1}`)
	if _, out := d.Dispatch(context.Background(), c); out.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q", out.Outcome)
	}
	const want = `{"argv":["prog","[secret:key]"]}`
	if got := string(rec.rows[0].Args); got != want {
		t.Errorf("row args = %s, want %s", got, want)
	}
	if got := string(s.lines[0].Args); got != want {
		t.Errorf("tool_call args = %s, want %s", got, want)
	}
	if got := string(a.asked[0].Args); got != want {
		t.Errorf("approval args = %s, want %s", got, want)
	}

	c = newCall("cmd.prog", &sink{}, a)
	c.Args = json.RawMessage(`{}`)
	d.Dispatch(context.Background(), c)
	if got := string(rec.rows[1].Args); got != "{}" {
		t.Errorf("with no audit, row args = %s, want the model's", got)
	}
}

// callConfirmBackend is a fakeBackend that is also a CallConfirmer: a
// call whose arguments say "ask" asks every time, and any other call
// leaves the choice to Confirm.
type callConfirmBackend struct {
	fakeBackend
	seen []Call // the calls ConfirmCall got
}

// ConfirmCall returns ConfirmAlways for arguments holding "ask", and ok =
// false otherwise.
func (b *callConfirmBackend) ConfirmCall(c Call) (Confirm, bool) {
	b.seen = append(b.seen, c)
	if strings.Contains(string(c.Args), "ask") {
		return ConfirmAlways, true
	}
	return ConfirmNever, false
}

// TestCallConfirmer checks that dispatch prefers a backend's per-call
// answer, falls back to Confirm when there is none, passes the question
// through, and declines a job's call that asks.
func TestCallConfirmer(t *testing.T) {
	b := &callConfirmBackend{fakeBackend: fakeBackend{kind: KindBuiltin, tools: []string{"fetch"},
		confirm: map[string]Confirm{"fetch": ConfirmNever}}}
	d := New([]Backend{b}, nil, Options{})
	a := &approver{choice: rpc.ChoiceOnce}

	c := newCall("fetch", &sink{}, a)
	c.Args = json.RawMessage(`{"url":"known"}`)
	c.Question = "what does https://go.dev say?"
	if _, out := d.Dispatch(context.Background(), c); out.Outcome != OutcomeOK || a.askCount() != 0 {
		t.Errorf("known call: outcome %q after %d prompts; want ok with none", out.Outcome, a.askCount())
	}
	if b.seen[0].Question != c.Question {
		t.Errorf("ConfirmCall got question %q, want %q", b.seen[0].Question, c.Question)
	}

	c.Args = json.RawMessage(`{"url":"ask"}`)
	if _, out := d.Dispatch(context.Background(), c); out.Outcome != OutcomeOK || a.askCount() != 1 {
		t.Fatalf("asking call: outcome %q after %d prompts; want ok after one", out.Outcome, a.askCount())
	}
	if got := a.asked[0].Choices; len(got) != 2 || got[0] != rpc.ChoiceOnce || got[1] != rpc.ChoiceDeny {
		t.Errorf("choices = %v, want once and deny", got)
	}

	c.Source = rpc.SourceJob
	if _, out := d.Dispatch(context.Background(), c); out.Outcome != OutcomeDeclined || a.askCount() != 1 {
		t.Errorf("job call: outcome %q after %d prompts; want declined with no new prompt", out.Outcome, a.askCount())
	}
}

func TestAsks(t *testing.T) {
	b := &fakeBackend{kind: KindCommand, tools: []string{"cmd.a", "cmd.b"}, confirm: map[string]Confirm{"cmd.b": ConfirmAsk}}
	d := New([]Backend{b}, nil, Options{})
	if d.Asks("cmd.a") || !d.Asks("cmd.b") || !d.Asks("cmd.gone") {
		t.Errorf("Asks = %v, %v, %v; want false, true, true", d.Asks("cmd.a"), d.Asks("cmd.b"), d.Asks("cmd.gone"))
	}
}

func TestRecorderFailureDoesNotFailCall(t *testing.T) {
	b := &fakeBackend{kind: KindMCP, tools: []string{"web.search"}}
	d := New([]Backend{b}, &fakeRecorder{err: errors.New("database is locked")}, Options{})
	if res, out := d.Dispatch(context.Background(), newCall("web.search", &sink{}, nil)); out.Outcome != OutcomeOK || res.IsError {
		t.Errorf("outcome %q, IsError %v; want ok", out.Outcome, res.IsError)
	}
}

// TestSpan checks the meru.dispatch span's attributes, that a backend's own
// span nests under it, and that arguments and results stay out of it while
// capture_content is off.
func TestSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter)))
	t.Cleanup(func() { otel.SetTracerProvider(tracenoop.NewTracerProvider()) })

	b := &fakeBackend{kind: KindMCP, tools: []string{"web.search"},
		confirm: map[string]Confirm{"web.search": ConfirmAsk},
		call: func(ctx context.Context) (Result, error) {
			_, child := obs.Tracer().Start(ctx, "tools/call search")
			child.End()
			return Result{Text: "private result"}, nil
		}}
	d := New([]Backend{b}, nil, Options{})
	d.Dispatch(context.Background(), newCall("web.search", &sink{}, &approver{choice: rpc.ChoiceOnce}))

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	child, parent := spans[0], spans[1]
	if parent.Name != "meru.dispatch" || child.Parent.SpanID() != parent.SpanContext.SpanID() {
		t.Errorf("spans %q and %q: want tools/call under meru.dispatch", child.Name, parent.Name)
	}
	got := map[string]string{}
	for _, kv := range parent.Attributes {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	want := map[string]string{
		"gen_ai.tool.name":   "web.search",
		"meru.tool.kind":     "mcp",
		"meru.tool.server":   "web",
		"meru.tool.outcome":  "ok",
		"meru.tool.approval": "once",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attribute %s = %q, want %q", k, got[k], v)
		}
	}
	for k := range got {
		if strings.Contains(k, "arguments") || strings.Contains(k, "result") {
			t.Errorf("span carries %s with capture_content off", k)
		}
	}
}

// TestConcurrentDispatch runs calls side by side while another goroutine
// replaces the backend, for the race detector.
func TestConcurrentDispatch(t *testing.T) {
	b := &fakeBackend{kind: KindMCP, tools: []string{"web.search"},
		confirm: map[string]Confirm{"web.search": ConfirmAsk}}
	rec := &fakeRecorder{}
	d := New([]Backend{b}, rec, Options{})
	ap := &approver{choice: rpc.ChoiceSession}

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := newCall("web.search", &sink{}, ap)
			c.Session = []string{"s1", "s2"}[i%2]
			d.Dispatch(context.Background(), c)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.Replace(KindMCP, b)
		_ = d.Tools()
		_ = d.Servers()
	}()
	wg.Wait()
	if len(rec.rows) != 20 {
		t.Errorf("rows = %d, want 20", len(rec.rows))
	}
}

// TestCallerAndHint checks the two fields the agent's web-first step and
// skill hint use: Caller reaches the tool_call line and the row, and Hint
// replaces the refusal of a call no backend offers, which is still denied
// and recorded. A hint on a call that runs changes nothing.
func TestCallerAndHint(t *testing.T) {
	b := &fakeBackend{kind: KindBuiltin, tools: []string{"web_search"}}
	rec := &fakeRecorder{}
	d := New([]Backend{b}, rec, Options{})
	hint := "web-research is a skill, not a tool. Call web_search or web_fetch."

	s := &sink{}
	c := newCall("web_search", s, nil)
	c.Caller, c.Hint = CallerMeru, hint
	res, out := d.Dispatch(context.Background(), c)
	if out.Outcome != OutcomeOK || res.Text != "ok from web_search" {
		t.Errorf("merud's call: outcome %q, text %q; want ok from the tool", out.Outcome, res.Text)
	}
	if got := s.lines[0]; got.Type != transcript.TypeToolCall || got.Caller != CallerMeru {
		t.Errorf("tool_call line = %+v, want caller %q", got, CallerMeru)
	}

	s = &sink{}
	c = newCall("web-research", s, nil)
	c.Hint = hint
	res, out = d.Dispatch(context.Background(), c)
	if out.Outcome != OutcomeDenied || res.Text != hint || !res.IsError {
		t.Errorf("skill call: outcome %q, text %q; want denied with the hint", out.Outcome, res.Text)
	}
	if got := s.types(); !slices.Equal(got, []string{transcript.TypeToolCall, transcript.TypeToolResult}) {
		t.Errorf("skill call lines = %v, want a tool_call and a tool_result", got)
	}

	rows := rec.rows
	if len(rows) != 2 || rows[0].Caller != CallerMeru || rows[1].Caller != "" || rows[1].Outcome != OutcomeDenied {
		t.Errorf("rows = %+v, want merud's call, then the model's denied one", rows)
	}
}

// TestProgress checks that a backend's progress line reaches the function
// the agent put on the context, and that outside a turn it goes nowhere.
func TestProgress(t *testing.T) {
	var got []string
	ctx := WithProgress(context.Background(), func(s string) { got = append(got, s) })
	Progress(ctx, "Installing the page reader")
	Progress(context.Background(), "nobody hears this")
	if len(got) != 1 || got[0] != "Installing the page reader" {
		t.Errorf("progress = %q", got)
	}
}
