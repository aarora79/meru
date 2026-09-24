// This file tests tool calls in the chat model: the tool lines, and the
// approval box from the moment merud asks until the user answers. The turn
// runs in its own goroutine, as Bubble Tea runs it, so the test also covers
// the hand-off between that goroutine and Update.

package tui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// allChoices is what merud offers for an ordinary tool in a confirm list.
var allChoices = []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}

// toolReply scripts a turn that asks about one mail.send call offering
// choices, then reports the call and answers.
func toolReply(choices []rpc.Choice) []rpc.Event {
	args := json.RawMessage(`{"to":"sam@example.com"}`)
	return []rpc.Event{
		{Type: rpc.EventSession, Session: "s1"},
		{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: "1", Name: "mail.send", Kind: "mcp", Args: args}},
		{Type: rpc.EventApproval, Approval: &rpc.Approval{ID: "a1", Name: "mail.send", Kind: "mcp", Args: args, Choices: choices}},
		{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: "1", Name: "mail.send", Kind: "mcp", Outcome: "ok", DurationMillis: 80}},
		{Type: rpc.EventToken, Text: "Sent."},
		{Type: rpc.EventDone},
	}
}

// startTurn types q, presses Enter and runs the turn in its own goroutine.
// The returned channel gets the turn's final message when it ends.
func startTurn(t *testing.T, m Model, q string) (Model, chan tea.Msg) {
	t.Helper()
	m, cmd := update(t, m, typeText(q), press(tea.KeyEnter))
	result := make(chan tea.Msg, 1)
	go func() { result <- finishTurn(cmd) }()
	return m, result
}

// untilApproval feeds messages from the fake sender into the model until
// an approvalRequestMsg arrives, which it also feeds in. It fails the test
// after five seconds.
func untilApproval(t *testing.T, m Model, snd *fakeSender) Model {
	t.Helper()
	for {
		select {
		case msg := <-snd.ch:
			m, _ = update(t, m, msg)
			if _, ok := msg.(approvalRequestMsg); ok {
				return m
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a message")
		}
	}
}

// waitDone waits for the turn's final message and feeds the events still
// queued, then the final message, into the model.
func waitDone(t *testing.T, m Model, snd *fakeSender, result chan tea.Msg) Model {
	t.Helper()
	select {
	case done := <-result:
		m = drain(t, m, snd)
		m, _ = update(t, m, done)
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not end")
	}
	return m
}

func TestApprovalKeys(t *testing.T) {
	onceDeny := []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}
	tests := []struct {
		name    string
		choices []rpc.Choice
		keys    []tea.Msg
		want    rpc.Choice
	}{
		{"o approves once", allChoices, []tea.Msg{typeText("o")}, rpc.ChoiceOnce},
		{"s approves for the session", allChoices, []tea.Msg{typeText("s")}, rpc.ChoiceSession},
		{"d denies", allChoices, []tea.Msg{typeText("d")}, rpc.ChoiceDeny},
		{"capital letters work", allChoices, []tea.Msg{typeText("O")}, rpc.ChoiceOnce},
		{"enter picks deny first", allChoices, []tea.Msg{press(tea.KeyEnter)}, rpc.ChoiceDeny},
		{"left then enter", allChoices, []tea.Msg{press(tea.KeyLeft), press(tea.KeyEnter)}, rpc.ChoiceSession},
		{"left stops at the first", allChoices, []tea.Msg{press(tea.KeyLeft), press(tea.KeyLeft), press(tea.KeyLeft), press(tea.KeyEnter)}, rpc.ChoiceOnce},
		{"right stops at the last", allChoices, []tea.Msg{press(tea.KeyRight), press(tea.KeyEnter)}, rpc.ChoiceDeny},
		{"a choice not offered does nothing", onceDeny, []tea.Msg{typeText("s"), typeText("x"), typeText("o")}, rpc.ChoiceOnce},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merud := &fakeMerud{events: toolReply(tt.choices)}
			snd := newFakeSender()
			m, result := startTurn(t, testModel(merud.ask, snd), "email sam")
			m = untilApproval(t, m, snd)
			if m.approval == nil {
				t.Fatal("no approval box after merud asked")
			}
			if view := m.View(); !strings.Contains(view, "Run mail.send?") {
				t.Errorf("view lacks the approval box:\n%s", view)
			}

			m, _ = update(t, m, tt.keys...)
			if m.approval != nil {
				t.Fatal("approval box still open after the answer")
			}
			m = waitDone(t, m, snd, result)

			if !slices.Equal(merud.choices, []rpc.Choice{tt.want}) {
				t.Errorf("merud got %v, want [%s]", merud.choices, tt.want)
			}
			if m.input.Value() != "" {
				t.Errorf("input = %q, want the keys kept out of it", m.input.Value())
			}
			want := []toolCall{{id: "1", name: "mail.send", outcome: "ok", millis: 80}}
			if got := m.turns[0].tools; !reflect.DeepEqual(got, want) {
				t.Errorf("tools = %+v, want %+v", got, want)
			}
			if m.turns[0].state != stateDone || m.turns[0].answer != "Sent." {
				t.Errorf("turn = %+v, want done with the answer", bare(m.turns[0]))
			}
		})
	}
}

// TestApprovalIgnoresTyping checks that letters that aren't choices, and
// other keys, leave the box open and the input empty.
func TestApprovalIgnoresTyping(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.turns = []exchange{{question: "q", state: stateActive}}
	m.streaming, m.turn = true, 1
	reply := make(chan rpc.Choice, 1)
	m, _ = update(t, m, approvalRequestMsg{turn: 1, approval: rpc.Approval{Name: "mail.send", Choices: allChoices}, reply: reply})

	m, _ = update(t, m, typeText("hello"), press(tea.KeySpace), press(tea.KeyUp), press(tea.KeyCtrlJ))
	if m.approval == nil {
		t.Fatal("typing closed the approval box")
	}
	if m.input.Value() != "" {
		t.Errorf("input = %q, want empty while the box is open", m.input.Value())
	}
	select {
	case c := <-reply:
		t.Errorf("merud got %q before the user answered", c)
	default:
	}
}

// TestApprovalCtrlC checks that Ctrl-C with the box open stops the turn:
// the box closes, and the call gets no approval.
func TestApprovalCtrlC(t *testing.T) {
	merud := &fakeMerud{events: toolReply(allChoices)}
	snd := newFakeSender()
	m, result := startTurn(t, testModel(merud.ask, snd), "email sam")
	m = untilApproval(t, m, snd)

	m, cmd := update(t, m, press(tea.KeyCtrlC))
	if cmd != nil {
		t.Error("Ctrl-C returned a command, want none: the chat stays open")
	}
	if m.approval != nil || m.streaming {
		t.Errorf("approval = %v, streaming = %v; want both cleared", m.approval, m.streaming)
	}

	var done tea.Msg
	select {
	case done = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not stop after Ctrl-C")
	}
	if d, ok := done.(turnDoneMsg); !ok {
		t.Errorf("turn ended with %T, want turnDoneMsg", done)
	} else if !errors.Is(d.err, context.Canceled) && len(merud.choices) != 1 {
		// Either the goroutine saw the cancel first, or it read the deny
		// stopTurn sent; both stop the call.
		t.Errorf("turn ended with %v, want context.Canceled or a deny", d.err)
	}
	if len(merud.choices) == 1 && merud.choices[0] != rpc.ChoiceDeny {
		t.Errorf("merud got %v after Ctrl-C, want deny", merud.choices)
	}
	m, _ = update(t, m, done)
	if m.turns[0].state != stateStopped {
		t.Errorf("turn state = %v, want stopped", m.turns[0].state)
	}
}

// TestStaleApprovalDenied checks that a request from a turn that isn't
// streaming gets "deny" at once and opens no box.
func TestStaleApprovalDenied(t *testing.T) {
	tests := []struct {
		name      string
		streaming bool
		turn      int
		choices   []rpc.Choice
	}{
		{"idle", false, 1, allChoices},
		{"an older turn", true, 1, allChoices},
		{"no choices", true, 2, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(nil, newFakeSender())
			m.turns = []exchange{{question: "q", state: stateActive}}
			m.streaming, m.turn = tt.streaming, 2
			reply := make(chan rpc.Choice, 1)
			m, _ = update(t, m, approvalRequestMsg{turn: tt.turn, approval: rpc.Approval{Name: "mail.send", Choices: tt.choices}, reply: reply})
			if m.approval != nil {
				t.Error("a stale request opened the box")
			}
			select {
			case c := <-reply:
				if c != rpc.ChoiceDeny {
					t.Errorf("reply = %q, want deny", c)
				}
			default:
				t.Error("no reply; the goroutine would wait until Ctrl-C")
			}
		})
	}
}

// TestToolLines checks the tool lines through a call's life: running, then
// done with its outcome, and a result whose call never showed.
func TestToolLines(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.turns = []exchange{{question: "q", state: stateActive}}
	m.streaming, m.turn = true, 1
	call := func(id, name string) tea.Msg {
		return eventMsg{turn: 1, ev: rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: id, Name: name}}}
	}
	result := func(id, name, outcome string, ms int64) tea.Msg {
		return eventMsg{turn: 1, ev: rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: id, Name: name, Outcome: outcome, DurationMillis: ms}}}
	}

	m, _ = update(t, m, call("1", "notes.search"))
	if view := m.View(); !strings.Contains(view, "→ notes.search") {
		t.Errorf("view lacks the running call:\n%s", view)
	}
	m, _ = update(t, m, result("1", "notes.search", "ok", 1500), call("2", "mail.send"), result("2", "mail.send", "declined", 0), result("3", "notes.read", "timeout", 0))
	view := m.View()
	for _, s := range []string{"✓ notes.search · 1.5s", "✗ mail.send · declined", "✗ notes.read · timeout"} {
		if !strings.Contains(view, s) {
			t.Errorf("view lacks %q:\n%s", s, view)
		}
	}
	if strings.Contains(view, "→") {
		t.Errorf("view still shows a running call:\n%s", view)
	}
}
