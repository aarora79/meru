// This file tests the chat model without a terminal or a socket. Each test
// feeds Update the same messages Bubble Tea would, using a fake merud (an
// askFunc that replays scripted events) and a fake program (a sender that
// queues messages on a channel).

package tui

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// fakeSender stands in for *tea.Program. Stream commands run in their own
// goroutine in some tests, so it hands messages over on a channel.
type fakeSender struct {
	ch chan tea.Msg
}

// newFakeSender makes a fakeSender with room for 100 queued messages, more
// than any test sends, so Send never waits.
func newFakeSender() *fakeSender {
	return &fakeSender{ch: make(chan tea.Msg, 100)}
}

// Send queues msg for the test to read.
func (s *fakeSender) Send(msg tea.Msg) { s.ch <- msg }

// fakeMerud records each request and replies with scripted events.
type fakeMerud struct {
	reqs   []rpc.Request
	events []rpc.Event
	err    error // yielded after the events, like a dropped connection
	// block makes the reply wait for cancellation after the events, like a
	// model that is still generating.
	block bool
}

// ask is the fake askFunc. Tests call the stream command in one goroutine at
// a time, so reqs needs no lock.
func (f *fakeMerud) ask(ctx context.Context, req rpc.Request) iter.Seq2[rpc.Event, error] {
	f.reqs = append(f.reqs, req)
	return func(yield func(rpc.Event, error) bool) {
		for _, ev := range f.events {
			if !yield(ev, nil) {
				return
			}
		}
		if f.block {
			<-ctx.Done()
			yield(rpc.Event{}, ctx.Err())
			return
		}
		if f.err != nil {
			yield(rpc.Event{}, f.err)
		}
	}
}

// update runs msgs through Update in order and returns the final model and
// the last command.
func update(t *testing.T, m Model, msgs ...tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	return m, cmd
}

// typeText returns the key message for typing s.
func typeText(s string) tea.Msg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// key returns the message for one special key.
func key(k tea.KeyType) tea.Msg {
	return tea.KeyMsg{Type: k}
}

// ask types q, presses Enter, runs the whole turn against merud, and feeds
// every resulting message back into the model.
func ask(t *testing.T, m Model, merud *fakeMerud, snd *fakeSender, q string) Model {
	t.Helper()
	m, cmd := update(t, m, typeText(q), key(tea.KeyEnter))
	done := runStream(t, cmd)
	m = drain(t, m, snd)
	m, _ = update(t, m, done)
	return m
}

// runStream runs the batch that Enter returns and gives back the stream
// command's turnDoneMsg, or fails the test if there is none. The stream's
// events land on the fake sender.
func runStream(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := finishTurn(cmd)
	if msg == nil {
		t.Fatal("no stream command in the batch after Enter")
	}
	return msg
}

// finishTurn runs each command in the Enter batch and returns the
// turnDoneMsg, or nil if none returned one. The spinner's tick command
// returns at once; the stream command returns when the turn ends. It takes no
// *testing.T, so a test may call it from its own goroutine.
func finishTurn(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return nil
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		if msg := c(); isDone(msg) {
			return msg
		}
	}
	return nil
}

// isDone reports whether msg ends a turn.
func isDone(msg tea.Msg) bool {
	_, ok := msg.(turnDoneMsg)
	return ok
}

// drain feeds every queued event from the fake sender into the model.
func drain(t *testing.T, m Model, snd *fakeSender) Model {
	t.Helper()
	for {
		select {
		case msg := <-snd.ch:
			m, _ = update(t, m, msg)
		default:
			return m
		}
	}
}

// reply is the scripted answer most tests use.
var reply = []rpc.Event{
	{Type: rpc.EventSession, Session: "s1"},
	{Type: rpc.EventRoute, Route: "direct", Confidence: 0.93},
	{Type: rpc.EventToken, Text: "Hel"},
	{Type: rpc.EventToken, Text: "lo."},
	{Type: rpc.EventDone},
}

func TestTypingFillsInput(t *testing.T) {
	m := newModel(nil, nil)
	m, _ = update(t, m, typeText("hi"), typeText(" there"))
	if got := m.input.Value(); got != "hi there" {
		t.Errorf("input = %q, want %q", got, "hi there")
	}
}

func TestEnterSendsQuestion(t *testing.T) {
	merud := &fakeMerud{events: reply}
	m := newModel(merud.ask, newFakeSender())
	m, cmd := update(t, m, typeText("  hello  "), key(tea.KeyEnter))

	if !m.streaming {
		t.Error("not streaming after Enter")
	}
	if m.input.Value() != "" {
		t.Errorf("input = %q after Enter, want empty", m.input.Value())
	}
	if len(m.entries) != 1 || m.entries[0] != (entry{entryQuestion, "hello"}) {
		t.Errorf("entries = %+v, want the question alone", m.entries)
	}

	runStream(t, cmd)
	want := rpc.Request{Op: rpc.OpAsk, Text: "hello", Source: rpc.SourceTUI}
	if len(merud.reqs) != 1 || merud.reqs[0] != want {
		t.Errorf("requests = %+v, want [%+v]", merud.reqs, want)
	}
}

func TestEnterIgnored(t *testing.T) {
	tests := []struct {
		name  string
		setup func(Model) Model
	}{
		{"blank line", func(m Model) Model {
			m, _ = update(t, m, typeText("   "))
			return m
		}},
		{"answer still streaming", func(m Model) Model {
			m.streaming = true
			m, _ = update(t, m, typeText("second"))
			return m
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup(newModel(nil, nil))
			before := len(m.entries)
			m, cmd := update(t, m, key(tea.KeyEnter))
			if cmd != nil {
				t.Error("Enter returned a command, want none")
			}
			if len(m.entries) != before {
				t.Errorf("entries grew from %d to %d", before, len(m.entries))
			}
		})
	}
}

func TestStreamedAnswer(t *testing.T) {
	merud := &fakeMerud{events: reply}
	snd := newFakeSender()
	m := ask(t, newModel(merud.ask, snd), merud, snd, "hello")

	want := []entry{
		{entryQuestion, "hello"},
		{entryRoute, "route: direct (0.93)"},
		{entryAnswer, "Hello."},
	}
	if len(m.entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", m.entries, want)
	}
	for i := range want {
		if m.entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, m.entries[i], want[i])
		}
	}
	if m.streaming {
		t.Error("still streaming after the turn ended")
	}
	if m.cancel != nil {
		t.Error("cancel kept after the turn ended")
	}
	view := m.View()
	for _, s := range []string{"> hello", "route: direct (0.93)", "Hello."} {
		if !strings.Contains(view, s) {
			t.Errorf("view lacks %q:\n%s", s, view)
		}
	}
}

func TestSessionReused(t *testing.T) {
	merud := &fakeMerud{events: reply}
	snd := newFakeSender()
	m := ask(t, newModel(merud.ask, snd), merud, snd, "first")
	if m.session != "s1" {
		t.Fatalf("session = %q, want s1", m.session)
	}
	m = ask(t, m, merud, snd, "second")

	if len(merud.reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(merud.reqs))
	}
	if merud.reqs[0].Session != "" {
		t.Errorf("first request session = %q, want empty", merud.reqs[0].Session)
	}
	if merud.reqs[1].Session != "s1" {
		t.Errorf("second request session = %q, want s1", merud.reqs[1].Session)
	}
	// The second answer must be its own entry, not glued to the first.
	last := m.entries[len(m.entries)-1]
	if last != (entry{entryAnswer, "Hello."}) {
		t.Errorf("last entry = %+v, want a fresh answer", last)
	}
}

func TestErrorsShowInline(t *testing.T) {
	tests := []struct {
		name  string
		merud *fakeMerud
		want  string
	}{
		{
			name: "error event from merud",
			merud: &fakeMerud{events: []rpc.Event{
				{Type: rpc.EventSession, Session: "s1"},
				{Type: rpc.EventError, Error: "model not loaded"},
			}},
			want: "error: model not loaded",
		},
		{
			name:  "connection fails",
			merud: &fakeMerud{err: errors.New("connect to merud: no such file")},
			want:  "error: connect to merud: no such file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snd := newFakeSender()
			m := ask(t, newModel(tt.merud.ask, snd), tt.merud, snd, "hello")
			last := m.entries[len(m.entries)-1]
			if last != (entry{entryError, tt.want}) {
				t.Errorf("last entry = %+v, want error %q", last, tt.want)
			}
			if m.streaming {
				t.Error("still streaming after an error")
			}
			if !strings.Contains(m.View(), tt.want) {
				t.Errorf("view lacks %q", tt.want)
			}
		})
	}
}

func TestQuitKeys(t *testing.T) {
	tests := []struct {
		name      string
		streaming bool
		key       tea.KeyType
		wantQuit  bool
	}{
		{"ctrl-c when idle quits", false, tea.KeyCtrlC, true},
		{"ctrl-d when idle quits", false, tea.KeyCtrlD, true},
		{"ctrl-d while streaming quits", true, tea.KeyCtrlD, true},
		{"ctrl-c while streaming cancels the turn", true, tea.KeyCtrlC, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.streaming {
				m.streaming = true
				m.cancel = cancel
			}

			m, cmd := update(t, m, key(tt.key))

			quit := cmd != nil && cmd() == tea.Quit()
			if quit != tt.wantQuit {
				t.Errorf("quit = %v, want %v", quit, tt.wantQuit)
			}
			if m.streaming {
				t.Error("still streaming")
			}
			if tt.streaming && ctx.Err() == nil {
				t.Error("turn context not cancelled")
			}
		})
	}
}

// TestCancelMidStream runs a turn in its own goroutine, as Bubble Tea does,
// presses Ctrl-C while it waits for tokens, and checks that the turn stops
// and its late messages change nothing.
func TestCancelMidStream(t *testing.T) {
	merud := &fakeMerud{
		events: []rpc.Event{
			{Type: rpc.EventSession, Session: "s1"},
			{Type: rpc.EventToken, Text: "Part"},
		},
		block: true,
	}
	snd := newFakeSender()
	m, cmd := update(t, newModel(merud.ask, snd), typeText("long question"), key(tea.KeyEnter))

	// A channel of size 1 lets the goroutine hand back its result without
	// waiting for the test to read it.
	result := make(chan tea.Msg, 1)
	go func() { result <- finishTurn(cmd) }()

	// Read the two events the fake sends before it blocks.
	for range 2 {
		select {
		case msg := <-snd.ch:
			m, _ = update(t, m, msg)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for an event")
		}
	}

	m, cmd = update(t, m, key(tea.KeyCtrlC))
	if cmd != nil {
		t.Error("Ctrl-C mid-stream returned a command, want none")
	}

	var done tea.Msg
	select {
	case done = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not stop after Ctrl-C")
	}
	d, ok := done.(turnDoneMsg)
	if !ok {
		t.Fatalf("turn ended with %T, want turnDoneMsg", done)
	}
	if !errors.Is(d.err, context.Canceled) {
		t.Errorf("turn ended with %v, want context.Canceled", d.err)
	}

	// Messages from the cancelled turn still arrive; Update must drop them.
	m, _ = update(t, m, eventMsg{turn: 1, ev: rpc.Event{Type: rpc.EventToken, Text: "late"}}, done)

	want := []entry{
		{entryQuestion, "long question"},
		{entryAnswer, "Part"},
		{entryNote, "(cancelled)"},
	}
	if len(m.entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", m.entries, want)
	}
	for i := range want {
		if m.entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, m.entries[i], want[i])
		}
	}
	if m.session != "s1" {
		t.Errorf("session = %q, want s1 kept from the cancelled turn", m.session)
	}
}

func TestUpRecallsLastQuestion(t *testing.T) {
	merud := &fakeMerud{events: reply}
	snd := newFakeSender()
	m := newModel(merud.ask, snd)

	m, _ = update(t, m, key(tea.KeyUp))
	if m.input.Value() != "" {
		t.Errorf("Up with no history set input to %q", m.input.Value())
	}

	m = ask(t, m, merud, snd, "what time is it")
	m, _ = update(t, m, key(tea.KeyUp))
	if got := m.input.Value(); got != "what time is it" {
		t.Errorf("input after Up = %q, want the last question", got)
	}
}

func TestResizeReflows(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{
		{Type: rpc.EventToken, Text: strings.Repeat("word ", 30)},
		{Type: rpc.EventDone},
	}}
	snd := newFakeSender()
	m := ask(t, newModel(merud.ask, snd), merud, snd, "talk")

	m, _ = update(t, m, tea.WindowSizeMsg{Width: 20, Height: 10})

	if m.transcript.Width != 20 || m.transcript.Height != 10-footerLines {
		t.Errorf("transcript = %dx%d, want 20x%d", m.transcript.Width, m.transcript.Height, 10-footerLines)
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 10 {
		t.Errorf("view has %d lines, want 10", len(lines))
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("line %q is %d wide, want at most 20", line, w)
		}
	}
	// The transcript follows the newest text, so the answer's end shows.
	if !strings.Contains(m.transcript.View(), "word") {
		t.Errorf("transcript lost the answer after resize:\n%s", m.transcript.View())
	}
}
