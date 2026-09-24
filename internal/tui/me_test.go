// This file tests the profile parts of the chat screen: the /me box and its
// keys, the nudge while Meru knows nothing about the user, and the memory
// count in the header.

package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// meReply is merud's answer to OpMemoryList in these tests: the profile
// plus a memory of another kind, which /me leaves out.
var meReply = []rpc.Event{
	{Type: rpc.EventMemories, Memories: append([]rpc.MemoryInfo{
		{ID: "project/garden.md", Kind: "project", Text: "The garden budget is 4,200 dollars."},
	}, profileFixture...)},
	{Type: rpc.EventDone},
}

// openMeBox types /me, presses Enter, runs the request against merud and
// feeds its answer back, as Bubble Tea would.
func openMeBox(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(t, m, typeText("/me"), press(tea.KeyEnter))
	if m.meBox == nil || !m.meBox.loading {
		t.Fatalf("me box = %+v after /me, want it open and waiting", m.meBox)
	}
	if cmd == nil {
		t.Fatal("/me returned no command, want the memory request")
	}
	m, _ = update(t, m, cmd())
	return m
}

func TestSlashMeOpensBox(t *testing.T) {
	merud := &fakeMerud{events: meReply}
	m := openMeBox(t, testModel(merud.ask, newFakeSender()))

	if len(merud.reqs) != 1 || merud.reqs[0].Op != rpc.OpMemoryList {
		t.Errorf("requests = %+v, want one memory_list", merud.reqs)
	}
	if len(m.turns) != 0 || m.input.Value() != "" {
		t.Errorf("turns = %d, input = %q; /me must not ask the model", len(m.turns), m.input.Value())
	}
	b := m.meBox
	if b == nil || b.loading || b.err != "" || len(b.memories) != len(profileFixture) {
		t.Fatalf("me box = %+v, want the %d profile memories", b, len(profileFixture))
	}
	view := m.View()
	for _, want := range []string{"About you", "Name: Dana Reyes", "Answers: short", "remember that", "meru setup user", "esc/q close"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "garden") {
		t.Errorf("the /me box shows a memory outside the profile:\n%s", view)
	}
}

func TestMeBoxStates(t *testing.T) {
	tests := []struct {
		name  string
		merud *fakeMerud
		want  string
	}{
		{"nothing known", &fakeMerud{events: []rpc.Event{{Type: rpc.EventMemories}, {Type: rpc.EventDone}}}, "Meru doesn't know you yet."},
		{"older merud", &fakeMerud{events: []rpc.Event{{Type: rpc.EventError, Error: `unknown op "memory_list"`}}}, `merud gave no memories: unknown op "memory_list"`},
		{"merud down", &fakeMerud{err: errors.New("no socket")}, "merud gave no memories: no socket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := openMeBox(t, testModel(tt.merud.ask, newFakeSender()))
			if view := m.View(); !strings.Contains(view, tt.want) {
				t.Errorf("view lacks %q:\n%s", tt.want, view)
			}
		})
	}
}

func TestMeBoxKeys(t *testing.T) {
	tests := []struct {
		name     string
		key      tea.Msg
		wantOpen bool
	}{
		{"esc closes", press(tea.KeyEsc), false},
		{"q closes", typeText("q"), false},
		{"other letters do nothing", typeText("x"), true},
		{"enter does nothing", press(tea.KeyEnter), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merud := &fakeMerud{events: meReply}
			m := openMeBox(t, testModel(merud.ask, newFakeSender()))
			m, _ = update(t, m, tt.key)
			if open := m.meBox != nil; open != tt.wantOpen {
				t.Errorf("box open = %v, want %v", open, tt.wantOpen)
			}
			if m.input.Value() != "" {
				t.Errorf("input = %q, want the key kept out of it", m.input.Value())
			}
			if !tt.wantOpen && !m.input.Focused() {
				t.Error("the input has no cursor after the box closed")
			}
		})
	}
}

// TestMeReplyAfterClose checks that a late reply can't reopen a closed box.
func TestMeReplyAfterClose(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, meMsg{memories: profileFixture})
	if m.meBox != nil {
		t.Errorf("me box = %+v, want it still closed", m.meBox)
	}
}

func TestMeBoxClosedByApproval(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.turn = 1
	m.turns = []exchange{{question: "q", state: stateActive}}
	m.streaming = true
	m.meBox = &meBox{memories: profileFixture}
	m, _ = update(t, m, approvalRequestMsg{turn: 1, approval: rpc.Approval{Name: "mail.send", Choices: allChoices}, reply: make(chan rpc.Choice, 1)})
	if m.meBox != nil || m.approval == nil {
		t.Errorf("me box = %v, approval = %v; want the approval box alone", m.meBox, m.approval)
	}
}

// TestProfileNudge checks the nudge and the header marker through the
// profile counts merud may send: none yet, unknown, zero, and some.
func TestProfileNudge(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	steps := []struct {
		name  string
		ix    *rpc.IndexStatus
		nudge bool
	}{
		{"before any status", nil, false},
		{"merud couldn't read the folder", &rpc.IndexStatus{Documents: 5, Memories: -1, Profile: -1}, false},
		{"empty profile", &rpc.IndexStatus{Documents: 5}, true},
		{"after setup user", &rpc.IndexStatus{Documents: 5, Memories: 3, Profile: 3}, false},
	}
	for _, s := range steps {
		if s.ix != nil {
			m, _ = update(t, m, pingMsg{index: s.ix})
		}
		view := m.View()
		if got := strings.Contains(view, "Meru doesn't know you yet"); got != s.nudge {
			t.Errorf("%s: nudge shown = %v, want %v:\n%s", s.name, got, s.nudge, view)
		}
		if got := strings.Contains(m.header(), "no profile"); got != s.nudge {
			t.Errorf("%s: header marker = %v, want %v: %q", s.name, got, s.nudge, m.header())
		}
	}
}

// TestProfileMarkerAfterQuestion checks that the header keeps the marker
// once a question has replaced the empty conversation and its nudge.
func TestProfileMarkerAfterQuestion(t *testing.T) {
	m := screen(t, 100, 24, "What is Meru?", nil, false, nil)
	m, _ = update(t, m, pingMsg{index: &rpc.IndexStatus{Documents: 5}})
	if strings.Contains(m.View(), "Meru doesn't know you yet") {
		t.Error("the nudge shows after a question")
	}
	if !strings.Contains(m.header(), "no profile") {
		t.Errorf("header = %q, want the marker", m.header())
	}
}

func TestMemoryCount(t *testing.T) {
	tests := []struct {
		name  string
		ix    *rpc.IndexStatus
		sizes bool
		want  string
	}{
		{"before merud answers", nil, true, ""},
		{"several", bigIndex, true, "7 memories"},
		{"one", &rpc.IndexStatus{Memories: 1}, true, "1 memory"},
		{"none", &rpc.IndexStatus{}, true, "0 memories"},
		{"unknown", &rpc.IndexStatus{Memories: -1}, true, ""},
		{"without sizes", bigIndex, false, ""},
	}
	for _, tt := range tests {
		if got := memoryCount(tt.ix, tt.sizes); got != tt.want {
			t.Errorf("%s: memoryCount = %q, want %q", tt.name, got, tt.want)
		}
	}
}
