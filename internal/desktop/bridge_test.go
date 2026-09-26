// This file tests the Bridge against an in-process rpc server, over a real
// Unix socket: a turn's updates in order, the approval round trip, the
// queue, and Stop.

package desktop

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// startServer serves h on a short socket path until the test ends and
// returns the path. It avoids t.TempDir because macOS caps socket paths at
// 104 bytes.
func startServer(t *testing.T, h rpc.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := rpc.Listen(ctx, sock)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rpc.Serve(ctx, ln, h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return sock
}

// recorder collects the Updates a Bridge emits, and lets a test wait for
// one.
type recorder struct {
	mu      sync.Mutex // guards updates
	updates []Update
	changed chan struct{} // gets a value after each update, if nobody waits it is dropped
}

// newRecorder returns an empty recorder.
func newRecorder() *recorder {
	return &recorder{changed: make(chan struct{}, 1)}
}

// emit is the Bridge's EmitFunc.
func (r *recorder) emit(name string, data any) {
	if name != UpdateEvent {
		panic("unexpected event " + name)
	}
	r.mu.Lock()
	r.updates = append(r.updates, data.(Update))
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// all returns a copy of the updates so far.
func (r *recorder) all() []Update {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Update(nil), r.updates...)
}

// waitFor waits up to five seconds for an update that ok accepts, and
// returns it.
func (r *recorder) waitFor(t *testing.T, what string, ok func(Update) bool) Update {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, u := range r.all() {
			if ok(u) {
				return u
			}
		}
		select {
		case <-r.changed:
		case <-deadline:
			t.Fatalf("no %s update; got %+v", what, r.all())
		}
	}
}

// newBridge returns a Bridge on sock that records its updates and opens
// nothing.
func newBridge(sock string) (*Bridge, *recorder) {
	r := newRecorder()
	b := New(Options{Socket: sock, Model: "main-model", Home: "/Users/dana", Emit: r.emit,
		Open: func(string) error { return nil }})
	return b, r
}

// isEnd matches the end of turn n.
func isEnd(n int) func(Update) bool {
	return func(u Update) bool { return u.Kind == KindEnd && u.Turn == n }
}

// summary writes an update as a short string, so a test can compare the
// order of a whole turn at once.
func summary(u Update) string {
	s := u.Kind
	if u.Event != nil {
		s += ":" + string(u.Event.Type)
	}
	if u.Step != nil {
		s += ":" + u.Step.Label
		if u.Step.Outcome != "" {
			s += "=" + u.Step.Outcome
		}
	}
	return s
}

// TestTurnUpdatesInOrder checks that each event of a turn reaches the page
// in merud's order, with the friendly step on the tool events, the session
// kept for the next question, and the servers the turn contacted.
func TestTurnUpdatesInOrder(t *testing.T) {
	var gotReq rpc.Request
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		gotReq = req
		for _, ev := range []rpc.Event{
			{Type: rpc.EventSession, Session: "2026-09-25T100000-e5f6"},
			{Type: rpc.EventRoute, Route: "search+tools", Confidence: 0.8},
			{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: "c1", Name: "google.search_gmail_messages", Kind: "mcp"}},
			{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: "c1", Name: "google.search_gmail_messages", Kind: "mcp", Outcome: "ok", DurationMillis: 800}},
			{Type: rpc.EventSources, Sources: []rpc.Citation{{N: 1, Path: "~/Notes/lisbon.md"}, {N: 2, Path: "~/Notes/garden.md"}}},
			{Type: rpc.EventToken, Text: "The Casa "},
			{Type: rpc.EventToken, Text: "do Rio [1]."},
			{Type: rpc.EventDone, TTFTMillis: 420, TokensOut: 8, EvalMillis: 200},
		} {
			if err := emit(ev); err != nil {
				return err
			}
		}
		return nil
	})
	b, r := newBridge(sock)
	defer b.ServiceShutdown()

	if err := b.Send("", "  Which hotel did I book in Lisbon?  ", ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	end := r.waitFor(t, "end", isEnd(1))
	if end.Error != "" || end.Stopped {
		t.Errorf("end = %+v, want a clean end", end)
	}
	if !reflect.DeepEqual(end.Contacted, []string{"google"}) {
		t.Errorf("Contacted = %v, want [google]", end.Contacted)
	}
	// The search found two files and the answer cites one: only that one
	// shows under the answer.
	if !reflect.DeepEqual(end.Cited, []rpc.Citation{{N: 1, Path: "~/Notes/lisbon.md"}}) {
		t.Errorf("Cited = %+v, want lisbon.md alone", end.Cited)
	}
	if gotReq.Text != "Which hotel did I book in Lisbon?" || gotReq.Source != rpc.SourceDesktop || gotReq.Session != "" {
		t.Errorf("request = %+v, want the trimmed question, from desktop, in a new session", gotReq)
	}

	var got []string
	for _, u := range r.all() {
		got = append(got, summary(u))
	}
	want := []string{
		"start",
		"event:session",
		"event:route",
		"event:tool_call:Searched mail",
		"event:tool_result:Searched mail=ok",
		"event:sources",
		"event:token",
		"event:token",
		"event:done",
		"end",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("updates =\n%v\nwant\n%v", got, want)
	}

	// The session update carries the ID merud started, and the page sends
	// it back with the next question to continue the chat.
	sess := r.waitFor(t, "session", func(u Update) bool { return u.Session != "" })
	if err := b.Send(sess.Session, "And the check-in time?", ""); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "second end", isEnd(2))
	if gotReq.Session != "2026-09-25T100000-e5f6" {
		t.Errorf("second request's session = %q, want the first turn's", gotReq.Session)
	}
}

// TestSendBlank checks that a blank question never reaches merud.
func TestSendBlank(t *testing.T) {
	b, r := newBridge("/nonexistent/merud.sock")
	if err := b.Send("", " \n ", ""); err == nil {
		t.Error("Send of a blank question = nil, want an error")
	}
	if len(r.all()) != 0 {
		t.Errorf("updates = %+v, want none", r.all())
	}
}

// TestConnectionFailure checks that a merud that isn't running ends the
// turn with an error the page can show.
func TestConnectionFailure(t *testing.T) {
	b, r := newBridge(filepath.Join(t.TempDir(), "none.sock"))
	defer b.ServiceShutdown()
	if err := b.Send("", "hello", ""); err != nil {
		t.Fatal(err)
	}
	end := r.waitFor(t, "end", isEnd(1))
	if end.Error == "" {
		t.Errorf("end = %+v, want the connection error", end)
	}
}

// approvalServer asks about one mail call and reports the choice it got
// back as the answer's text.
func approvalServer(t *testing.T) string {
	return startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
		args := json.RawMessage(`{"to":["dana@example.com"],"subject":"Lisbon","body":"See you at the hotel.","draft":false}`)
		c, err := approve(ctx, rpc.Approval{Name: "google.send_gmail_message", Kind: "mcp", Args: args,
			Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}})
		if err != nil {
			return err
		}
		return emit(rpc.Event{Type: rpc.EventToken, Text: "choice=" + string(c)})
	})
}

// TestApprovalRoundTrip checks each answer the page can give: the card
// arrives, Approve sends the choice to merud, and the turn goes on.
func TestApprovalRoundTrip(t *testing.T) {
	for _, choice := range []string{"once", "session", "deny"} {
		t.Run(choice, func(t *testing.T) {
			b, r := newBridge(approvalServer(t))
			defer b.ServiceShutdown()
			if err := b.Send("", "Tell Dana I booked the hotel", ""); err != nil {
				t.Fatal(err)
			}
			card := r.waitFor(t, "approval", func(u Update) bool { return u.Kind == KindApproval })
			v := card.Approval
			wantFields := []Field{{"To", "dana@example.com"}, {"Subject", "Lisbon"}, {"Body", "See you at the hotel."}}
			if v.Label != "Sent mail" || !reflect.DeepEqual(v.Fields, wantFields) || v.JSON != "{\n  \"draft\": false\n}" {
				t.Errorf("card = %+v, want the mail laid out with the rest as JSON", v)
			}
			if len(v.Choices) != 3 || v.Choices[2].Label != "Don't allow" {
				t.Errorf("choices = %+v, want three with plain labels", v.Choices)
			}

			if err := b.Approve(v.ID, choice); err != nil {
				t.Fatalf("Approve: %v", err)
			}
			tok := r.waitFor(t, "token", func(u Update) bool { return u.Event != nil && u.Event.Type == rpc.EventToken })
			if tok.Event.Text != "choice="+choice {
				t.Errorf("merud got %q, want choice=%s", tok.Event.Text, choice)
			}
			r.waitFor(t, "end", isEnd(1))

			// The approval is answered, and the turn is over.
			if err := b.Approve(v.ID, choice); err == nil {
				t.Error("a second Approve = nil, want an error")
			}
		})
	}
}

// TestApproveRefusesBadAnswers checks Approve's own checks.
func TestApproveRefusesBadAnswers(t *testing.T) {
	b, r := newBridge(approvalServer(t))
	defer b.ServiceShutdown()
	if err := b.Send("", "Tell Dana", ""); err != nil {
		t.Fatal(err)
	}
	card := r.waitFor(t, "approval", func(u Update) bool { return u.Kind == KindApproval })
	tests := []struct {
		name   string
		id     string
		choice string
	}{
		{"unknown choice", card.Approval.ID, "always"},
		{"merud's own ID", "1", "once"},
		{"unknown approval", "t1-99", "once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := b.Approve(tt.id, tt.choice); err == nil {
				t.Errorf("Approve(%q, %q) = nil, want an error", tt.id, tt.choice)
			}
		})
	}
	// Stop ends the turn while the card waits; merud sees the hang-up.
	b.Stop()
	end := r.waitFor(t, "end", isEnd(1))
	if !end.Stopped {
		t.Errorf("end = %+v, want Stopped", end)
	}
}

// gate is a handler whose turns wait until the test opens them, one per
// value sent on release. It reports each question it gets on asked.
type gate struct {
	asked   chan string
	release chan struct{}
}

// handler is the rpc.Handler for g.
func (g gate) handler(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	g.asked <- req.Text
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return emit(rpc.Event{Type: rpc.EventToken, Text: "answer to " + req.Text})
}

// lastQueue returns the queue as the latest queue update left it, with its
// notice.
func lastQueue(r *recorder) ([]string, string) {
	var q []string
	notice := ""
	for _, u := range r.all() {
		if u.Kind == KindQueue {
			q, notice = u.Queue, u.Notice
		}
	}
	return q, notice
}

// TestQueue checks the queue's rules: questions sent while a turn runs
// wait, at most five, in order; Unqueue takes one out; each goes when the
// turn before it ends.
func TestQueue(t *testing.T) {
	g := gate{asked: make(chan string, 10), release: make(chan struct{})}
	b, r := newBridge(startServer(t, g.handler))
	defer b.ServiceShutdown()

	if err := b.Send("", "first", ""); err != nil {
		t.Fatal(err)
	}
	if got := <-g.asked; got != "first" {
		t.Fatalf("merud got %q first", got)
	}
	for _, q := range []string{"q1", "q2", "q3", "q4", "q5"} {
		if err := b.Send("", q, ""); err != nil {
			t.Fatalf("Send(%s): %v", q, err)
		}
	}
	if err := b.Send("", "q6", ""); err == nil {
		t.Error("a sixth queued question = nil, want the queue full")
	}
	if err := b.Unqueue(1); err != nil {
		t.Fatalf("Unqueue: %v", err)
	}
	if err := b.Unqueue(9); err == nil {
		t.Error("Unqueue(9) = nil, want an error")
	}
	if q, _ := lastQueue(r); !reflect.DeepEqual(q, []string{"q1", "q3", "q4", "q5"}) {
		t.Errorf("queue = %v, want q1 q3 q4 q5", q)
	}

	// Each release ends one turn, and the oldest queued question goes next.
	for _, want := range []string{"q1", "q3", "q4", "q5"} {
		g.release <- struct{}{}
		if got := <-g.asked; got != want {
			t.Fatalf("merud got %q, want %q", got, want)
		}
	}
	g.release <- struct{}{}
	r.waitFor(t, "end of the last turn", isEnd(5))
	if q, _ := lastQueue(r); len(q) != 0 {
		t.Errorf("queue = %v, want it empty", q)
	}
}

// TestStopDropsQueue checks that Stop ends the running turn and drops the
// queue with a notice, and that the stopped turn sends nothing after.
func TestStopDropsQueue(t *testing.T) {
	g := gate{asked: make(chan string, 10), release: make(chan struct{})}
	b, r := newBridge(startServer(t, g.handler))
	defer b.ServiceShutdown()

	if err := b.Send("", "first", ""); err != nil {
		t.Fatal(err)
	}
	<-g.asked
	for _, q := range []string{"q1", "q2"} {
		if err := b.Send("", q, ""); err != nil {
			t.Fatal(err)
		}
	}
	b.Stop()
	end := r.waitFor(t, "end", isEnd(1))
	if !end.Stopped {
		t.Errorf("end = %+v, want Stopped", end)
	}
	q, notice := lastQueue(r)
	if len(q) != 0 || notice != "Dropped 2 queued questions." {
		t.Errorf("queue = %v with notice %q, want it empty and the drop noted", q, notice)
	}

	// The next question starts at once, as turn 2, and nothing of turn 1
	// follows its end.
	if err := b.Send("", "again", ""); err != nil {
		t.Fatal(err)
	}
	if got := <-g.asked; got != "again" {
		t.Fatalf("merud got %q, want again", got)
	}
	g.release <- struct{}{}
	r.waitFor(t, "end", isEnd(2))
	ended := false
	for _, u := range r.all() {
		if u.Turn == 1 && u.Kind == KindEnd {
			ended = true
		} else if ended && u.Turn == 1 {
			t.Errorf("update %+v came after turn 1 ended", u)
		}
	}
}

// TestDropNotice checks the notice's wording.
func TestDropNotice(t *testing.T) {
	for n, want := range map[int]string{0: "", 1: "Dropped 1 queued question.", 3: "Dropped 3 queued questions."} {
		if got := dropNotice(n); got != want {
			t.Errorf("dropNotice(%d) = %q, want %q", n, got, want)
		}
	}
}
