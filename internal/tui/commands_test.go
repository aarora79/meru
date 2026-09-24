// This file tests the slash commands that aren't about usage: /new.

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// TestNewSession checks /new: the screen clears, the session goes, the next
// question asks merud for a new one, and a late event from the answer that
// was streaming can't bring the old session back.
func TestNewSession(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventSession, Session: "2026-09-24T101500-ab12"}}, block: true}
	m := testModel(merud.ask, newFakeSender())
	m, _ = update(t, m, typeText("first question"), press(tea.KeyEnter))
	oldTurn := m.turn
	m, _ = update(t, m, eventMsg{turn: oldTurn, ev: rpc.Event{Type: rpc.EventSession, Session: "2026-09-24T101500-ab12"}})
	if m.session == "" || len(m.turns) != 1 || !m.streaming {
		t.Fatalf("before /new: session %q, %d turns, streaming %v", m.session, len(m.turns), m.streaming)
	}

	m, cmd := update(t, m, typeText("/new"), press(tea.KeyEnter))
	if cmd != nil {
		t.Errorf("/new returned a command; want nothing sent to merud")
	}
	if m.session != "" || len(m.turns) != 0 || m.streaming || m.input.Value() != "" {
		t.Errorf("after /new: session %q, %d turns, streaming %v, input %q; want all cleared",
			m.session, len(m.turns), m.streaming, m.input.Value())
	}
	if !strings.Contains(m.View(), "new session") {
		t.Errorf("view lacks the new-session notice:\n%s", m.View())
	}

	// A late event from the stopped answer belongs to no turn now.
	m, _ = update(t, m, eventMsg{turn: oldTurn, ev: rpc.Event{Type: rpc.EventSession, Session: "2026-09-24T101500-ab12"}})
	if m.session != "" {
		t.Errorf("a late event brought back session %q", m.session)
	}

	// The next question starts a new session: its request names none.
	merud.block = false
	m, cmd = update(t, m, typeText("second question"), press(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("the question after /new sent nothing")
	}
	finishTurn(cmd)
	last := merud.reqs[len(merud.reqs)-1]
	if last.Op != rpc.OpAsk || last.Session != "" || last.Text != "second question" {
		t.Errorf("request after /new = %+v, want an ask with no session", last)
	}
}

// TestSourceLinks checks that each source line links to its file when links
// are on, including every screen line of a source that wraps, and that the
// links don't count toward the line's width.
func TestSourceLinks(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.look.links = true
	m.home = "/Users/u"
	ex := &exchange{
		answer: "It is 4,200 dollars [1].",
		sources: []rpc.Citation{{N: 1, Path: "~/notes/a long folder name/garden budget notes.md",
			Heading: "Budget for the vegetable garden", StartLine: 3, EndLine: 5}},
	}
	const width = 40
	block := m.sourcesBlock(ex, width)
	const url = "file:///Users/u/notes/a%20long%20folder%20name/garden%20budget%20notes.md"
	lines := strings.Split(block, "\n")
	if len(lines) < 3 {
		t.Fatalf("want the source to wrap over several lines:\n%s", block)
	}
	for i, l := range lines[1:] {
		if !strings.Contains(l, "\x1b]8;;"+url+"\x1b\\") {
			t.Errorf("line %d has no link to %s: %q", i+2, url, l)
		}
		if w := ansi.StringWidth(l); w > width+answerIndent {
			t.Errorf("line %d is %d columns wide, want at most %d: %q", i+2, w, width+answerIndent, l)
		}
	}

	m.look.links = false
	if strings.Contains(m.sourcesBlock(ex, width), "\x1b]8") {
		t.Errorf("links off still wrote a link")
	}
}
