// This file tests opening a clicked link: the hit test on a screen line,
// the notice, and a click through the whole model with a fake opener, so
// no test starts a browser. internal/opener tests the URL checks and the
// program each system runs.

package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// fakeOpener records the URLs it gets and opens nothing.
type fakeOpener struct {
	got []string
	err error
}

// open is the fake openFunc.
func (f *fakeOpener) open(url string) error {
	f.got = append(f.got, url)
	return f.err
}

// TestLinkAt checks the hit test on one line with two links and colour
// codes round them, a wide rune before the second link, and each kind of
// string terminator.
func TestLinkAt(t *testing.T) {
	const garden = "https://example.com/notes/garden-plan"
	const seeds = "https://example.com/notes/seed-list"
	red, reset := "\x1b[31m", "\x1b[0m"
	line := red + "see " + reset +
		rpc.Hyperlink(garden, red+"garden"+reset) +
		" and 園 " +
		"\x1b]8;id=2;" + seeds + "\a" + "seeds" + "\x1b]8;;\a" +
		" done"
	// "see " is 4 columns, "garden" 6 (4-9), " and " 5 (10-14), "園" 2
	// (15-16), " " 1 (17), "seeds" 5 (18-22), " done" 5 (23-27).
	tests := []struct {
		name string
		col  int
		want string
	}{
		{"before the first link", 3, ""},
		{"first cell of the first link", 4, garden},
		{"last cell of the first link", 9, garden},
		{"between the links", 10, ""},
		{"on the wide rune", 16, ""},
		{"first cell of the second link", 18, seeds},
		{"last cell of the second link", 22, seeds},
		{"after the second link", 23, ""},
		{"past the end of the line", 40, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkAt(line, tt.col); got != tt.want {
				t.Errorf("linkAt(col %d) = %q, want %q", tt.col, got, tt.want)
			}
		})
	}
}

// TestLinkAtBroken checks lines the hit test must not trip on: plain text,
// a link never closed, and a code with no terminator.
func TestLinkAtBroken(t *testing.T) {
	const u = "https://example.com/notes/garden-plan"
	tests := []struct {
		name string
		line string
		col  int
		want string
	}{
		{"no links", "plain text", 2, ""},
		{"never closed", "ab\x1b]8;;" + u + "\x1b\\garden", 3, u},
		{"no terminator", "ab\x1b]8;;" + u + "garden", 3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkAt(tt.line, tt.col); got != tt.want {
				t.Errorf("linkAt = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOpenedNotice checks the notice after an open, and after a failed
// one.
func TestOpenedNotice(t *testing.T) {
	const u = "https://example.com/notes/garden-plan"
	if got, want := (openedMsg{url: u}).notice(), "opened example.com/notes/garden-plan"; got != want {
		t.Errorf("notice = %q, want %q", got, want)
	}
	got := openedMsg{url: u, err: errors.New("open: exit status 1")}.notice()
	if want := "couldn't open example.com/notes/garden-plan: open: exit status 1"; got != want {
		t.Errorf("notice = %q, want %q", got, want)
	}
}

// click returns a left-button press at column x, row y.
func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

// TestClickOpensLink clicks the mail link in an answer: with [chat]
// mouse_copy on, the fake opener gets the full URL and the notice says so;
// with it off, the chat leaves the click to the terminal and opens
// nothing.
func TestClickOpensLink(t *testing.T) {
	const mail = "https://mail.google.com/mail/u/0/#inbox/18f2c0a4b7e9d311"
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("mouse_copy=%v", on), func(t *testing.T) {
			opener := &fakeOpener{}
			m := linkChat(t, 80, true)
			m.info.MouseCopy = on
			m.open = opener.open
			x, y := labelPos(t, m, "mail.google.com/mail/u/0")

			m, cmd := update(t, m, click(x+2, y))
			m = runCopy(t, m, cmd)

			var want []string
			if on {
				want = []string{mail}
			}
			if !reflect.DeepEqual(opener.got, want) {
				t.Fatalf("opened %q, want %q", opener.got, want)
			}
			if on && !strings.HasPrefix(m.notice, "opened mail.google.com/mail/u/0/") {
				t.Errorf("notice = %q, want it to say what opened", m.notice)
			}
		})
	}
}

// TestClickBesideLink checks that a click on plain answer text opens
// nothing.
func TestClickBesideLink(t *testing.T) {
	opener := &fakeOpener{}
	m := linkChat(t, 80, true)
	m.info.MouseCopy = true
	m.open = opener.open
	x, y := labelPos(t, m, "Where is the plan?")
	m, cmd := update(t, m, click(x, y))
	runCopy(t, m, cmd)
	if len(opener.got) != 0 {
		t.Errorf("opened %q, want nothing", opener.got)
	}
}

// TestClickOpensSource clicks a line of the Sources list, which links to
// the cited file.
func TestClickOpensSource(t *testing.T) {
	opener := &fakeOpener{}
	m := screen(t, 80, 30, "", nil, false, nil)
	m.look.links = true
	m.info.MouseCopy = true
	m.open = opener.open
	m.home = "/Users/sam"
	m, _ = update(t, m, typeText("When does the garden project sow tomatoes?"), press(tea.KeyEnter))
	m, _ = update(t, m,
		eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventSources, Sources: []rpc.Citation{
			{N: 1, Path: "/Users/sam/notes/garden.md", StartLine: 3, EndLine: 5, Score: 0.032},
		}}},
		eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventToken, Text: "The garden project sows tomatoes on 12 April [1]."}},
		eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventDone, TokensOut: 12}},
		turnDoneMsg{turn: m.turn})

	// The answer holds "[1]" too, so find the source by its path.
	x, y := labelPos(t, m, "[1] /Users/sam/notes/garden.md")
	m, cmd := update(t, m, click(x+1, y))
	m = runCopy(t, m, cmd)
	if len(opener.got) != 1 || !strings.HasPrefix(opener.got[0], "file://") || !strings.HasSuffix(opener.got[0], "garden.md") {
		t.Fatalf("opened %q, want the file URL of garden.md", opener.got)
	}
	if !strings.HasPrefix(m.notice, "opened ") {
		t.Errorf("notice = %q, want it to say what opened", m.notice)
	}
}

// TestClickRefusesScheme checks that a click on a link the chat won't
// open sets a notice and runs no opener.
func TestClickRefusesScheme(t *testing.T) {
	m := testModel(nil, newFakeSender())
	opener := &fakeOpener{}
	m.open = opener.open
	next, cmd := m.openLink("-https://example.com/")
	m = next.(Model)
	if cmd != nil || len(opener.got) != 0 {
		t.Errorf("a URL starting with - still opened")
	}
	if !strings.HasPrefix(m.notice, "won't open") {
		t.Errorf("notice = %q, want a refusal", m.notice)
	}
}
