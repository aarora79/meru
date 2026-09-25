// This file tests the links in a finished answer: which links findLinks
// finds, where markLinks puts its markers, how shortURL shortens a URL, and
// the screen with links on and off, next to the copy labels.

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// TestMarkLinks checks where markLinks puts a marker for each kind of link,
// and that it leaves code and other schemes alone.
func TestMarkLinks(t *testing.T) {
	plan := "https://example.com/notes/garden-plan"
	// The marker is as wide as the URL the screen shows: the URL without
	// its scheme.
	mk := linkMarker(1, len("example.com/notes/garden-plan"))
	tests := []struct {
		name, src, want string
	}{
		{"inline link", "See [the plan](" + plan + ") now.", "See [the plan](" + mk + ") now."},
		{"inline link in angle brackets", "See [the plan](<" + plan + ">) now.", "See [the plan](" + mk + ") now."},
		{"autolink", "See <" + plan + "> now.", "See <" + mk + "> now."},
		{"bare URL", "See " + plan + ". Then plant.", "See <" + mk + ">. Then plant."},
		{"in a list", "- the plan: " + plan + "\n", "- the plan: <" + mk + ">\n"},
		{"in a quote", "> [plan](" + plan + ")\n", "> [plan](" + mk + ")\n"},
		{"www without a scheme", "See www.example.com today.", "See <" + linkMarker(1, len("www.example.com")) + "> today."},
		{"code block untouched", "```\ncurl " + plan + "\n```\n", "```\ncurl " + plan + "\n```\n"},
		{"code span untouched", "Run `open " + plan + "` now.", "Run `open " + plan + "` now."},
		{"javascript untouched", "[click](javascript:alert(1))", "[click](javascript:alert(1))"},
		{"mailto untouched", "<mailto:sam@example.com>", "<mailto:sam@example.com>"},
		{"email untouched", "Write to sam@example.com.", "Write to sam@example.com."},
		{"title untouched", `[plan](` + plan + ` "Garden")`, `[plan](` + plan + ` "Garden")`},
		{"reference untouched", "[plan][p]\n\n[p]: " + plan + "\n", "[plan][p]\n\n[p]: " + plan + "\n"},
		{"image untouched", "![beds](" + plan + ".png)", "![beds](" + plan + ".png)"},
		{"table untouched", "| a |\n|---|\n| " + plan + " |\n", "| a |\n|---|\n| " + plan + " |\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := markLinks(tt.src, findLinks(tt.src), 80, true)
			if got != tt.want {
				t.Errorf("markLinks = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMarkLinksTwo checks that two links get two markers, numbered in
// order, and that each swap holds an OSC 8 link to its full URL.
func TestMarkLinksTwo(t *testing.T) {
	src := "[a](https://example.com/a) and https://example.com/b"
	got, swaps := markLinks(src, findLinks(src), 80, true)
	want := "[a](" + linkMarker(1, 13) + ") and <" + linkMarker(2, 13) + ">"
	if got != want {
		t.Errorf("markLinks = %q, want %q", got, want)
	}
	if len(swaps) != 2 {
		t.Fatalf("got %d swaps, want 2", len(swaps))
	}
	for i, u := range []string{"https://example.com/a", "https://example.com/b"} {
		if !strings.Contains(swaps[i].text, "\x1b]8;;"+u+"\x1b\\") {
			t.Errorf("swap %d = %q, want a link to %s", i+1, swaps[i].text, u)
		}
	}
}

// TestShortURL checks how a URL shrinks to fit a width.
func TestShortURL(t *testing.T) {
	tests := []struct {
		url   string
		width int
		want  string
	}{
		{"https://example.com/", 40, "example.com"},
		{"https://example.com/notes/garden-plan", 40, "example.com/notes/garden-plan"},
		{"https://example.com/notes/garden-plan", 20, "example.com/notes/g…"},
		{"http://example.com/a", 40, "example.com/a"},
		{"HTTPS://Example.com/A", 40, "Example.com/A"},
		{"file:///home/sam/notes/garden.md", 40, "/home/sam/notes/garden.md"},
		{"https://mail.google.com/mail/u/0/#inbox/18f2c0a4b7e9d311", 30, "mail.google.com/mail/u/0/#inb…"},
		{"https://example.com/a", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := shortURL(tt.url, tt.width)
			if got != tt.want {
				t.Errorf("shortURL(%q, %d) = %q, want %q", tt.url, tt.width, got, tt.want)
			}
			if w := ansi.StringWidth(got); w > tt.width {
				t.Errorf("shortURL(%q, %d) is %d wide", tt.url, tt.width, w)
			}
		})
	}
}

// TestSplitWidth checks that splitWidth cuts a URL into pieces that fit
// and join back into the URL.
func TestSplitWidth(t *testing.T) {
	u := "https://example.com/notes/garden-plan"
	got := splitWidth(u, 16)
	want := []string{"https://example.", "com/notes/garden", "-plan"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("splitWidth = %q, want %q", got, want)
	}
}

// TestSwapLinks checks that swapLinks refuses output where a marker is
// missing, shows twice, or sits on a line wider than the screen.
func TestSwapLinks(t *testing.T) {
	mk := linkMarker(1, 13)
	swaps := []linkSwap{{marker: mk, text: "example.com/a"}}
	tests := []struct {
		name, rendered string
		ok             bool
	}{
		{"once", "  see " + mk + " now", true},
		{"missing", "  see nothing", false},
		{"twice", "  " + mk + " and " + mk, false},
		{"too wide", "  " + mk + strings.Repeat(" word", 20), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := swapLinks(tt.rendered, swaps, 40)
			if ok != tt.ok {
				t.Fatalf("swapLinks ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != "  see example.com/a now" {
				t.Errorf("swapLinks = %q", got)
			}
		})
	}
}

// linkAnswer is an answer with a long link inside a list, a bare URL, and
// a code block that gets a copy label.
const linkAnswer = "Two places to look:\n\n" +
	"1. The mail, [Gmail link](https://mail.google.com/mail/u/0/#inbox/18f2c0a4b7e9d311), from Monday.\n" +
	"2. The plan, https://example.com/notes/garden-plan/2026/spring-planting-schedule-for-the-raised-beds\n\n" +
	"Save the plan with:\n\n" +
	"```sh\ncurl -O https://example.com/notes/garden-plan.pdf\n```\n"

// linkChat builds a chat screen width columns wide, with links on or off,
// and runs one finished turn whose answer is linkAnswer.
func linkChat(t *testing.T, width int, links bool) Model {
	t.Helper()
	m := screen(t, width, 30, "", nil, false, nil)
	m.look.links = links
	m, _ = update(t, m, typeText("Where is the plan?"), press(tea.KeyEnter))
	m, _ = update(t, m,
		eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventToken, Text: linkAnswer}},
		eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventDone, TTFTMillis: 800, DurationMillis: 2400, TokensOut: 64}},
		turnDoneMsg{turn: m.turn})
	return m
}

// TestLinksGolden draws the answer at two widths, with links on and off,
// and compares the screen with golden files. With links on, the golden
// file holds the screen with the link codes stripped, so it shows each URL
// as the user sees it: short enough for its line. With links off, as with
// NO_COLOR, each URL shows in full, unbroken where it fits and in pieces
// one to a line where it doesn't. The copy label shows in every case.
func TestLinksGolden(t *testing.T) {
	for _, tt := range []struct {
		name  string
		width int
		links bool
	}{
		{"links", 80, true},
		{"links-narrow", 40, true},
		{"links-off", 80, false},
		{"links-off-narrow", 40, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			view := linkChat(t, tt.width, tt.links).View()
			if hasLink := strings.Contains(view, "\x1b]8;;"); hasLink != tt.links {
				t.Errorf("links %v, but link codes on screen: %v", tt.links, hasLink)
			}
			view = ansi.Strip(view)
			for i, l := range strings.Split(view, "\n") {
				if w := ansi.StringWidth(l); w > tt.width {
					t.Errorf("line %d is %d wide, want at most %d: %q", i+1, w, tt.width, l)
				}
			}
			for _, bad := range []string{"MERULINK", "MERUCOPY"} {
				if strings.Contains(view, bad) {
					t.Errorf("%q shows on screen:\n%s", bad, view)
				}
			}
			if !strings.Contains(view, copyLabel(1)) {
				t.Errorf("no copy label:\n%s", view)
			}
			golden(t, tt.name, view)
		})
	}
}

// TestLinksOnAndOff checks the link itself: with links on, the screen
// holds an OSC 8 link to the full URL and shows it short; with links off,
// it shows the full URL as plain text. It checks again after each resize.
func TestLinksOnAndOff(t *testing.T) {
	const mail = "https://mail.google.com/mail/u/0/#inbox/18f2c0a4b7e9d311"
	const bare = "https://example.com/notes/garden-plan/2026/spring-planting-schedule-for-the-raised-beds"
	for _, on := range []bool{true, false} {
		m := linkChat(t, 80, on)
		for _, w := range []int{80, 50, 120} {
			m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: 40})
			answer := m.renderedAnswer(&m.turns[len(m.turns)-1])
			if strings.Contains(answer, "MERULINK") {
				t.Fatalf("links %v, width %d: a marker shows:\n%s", on, w, answer)
			}
			if !strings.Contains(ansi.Strip(answer), copyLabel(1)) {
				t.Errorf("links %v, width %d: no copy label:\n%s", on, w, answer)
			}
			for _, u := range []string{mail, bare} {
				link := "\x1b]8;;" + u + "\x1b\\"
				switch {
				case on && !strings.Contains(answer, link):
					t.Errorf("width %d: no link to %s:\n%q", w, u, answer)
				case !on && strings.Contains(answer, "\x1b]8"):
					t.Errorf("width %d: links off still wrote a link", w)
				case !on && w >= 120 && !strings.Contains(answer, u):
					t.Errorf("width %d: links off doesn't show %s in full:\n%s", w, u, answer)
				}
			}
			if on && w >= 80 && !strings.Contains(ansi.Strip(answer), "mail.google.com/mail/u/0/#inbox/18f2c0a4b7e9d311") {
				t.Errorf("width %d: the mail link doesn't show its URL:\n%s", w, ansi.Strip(answer))
			}
		}
	}
}
