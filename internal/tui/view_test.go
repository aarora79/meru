// This file holds golden tests for View: each one builds a screen in a known
// state, draws it without colour, and compares the text with a file under
// testdata/. Run `go test ./internal/tui -update` to rewrite the files after
// a deliberate change to the look, then read the diff before committing.

package tui

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// updateGolden, set with -update on the command line, makes the golden tests
// write what View draws instead of comparing it. flag.Bool registers the flag
// and returns a pointer that holds its value once the test binary has parsed
// its arguments.
var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/")

// markdownAnswer is a reply with the Markdown a model often writes: a
// heading, bold text, a list and a fenced code block.
const markdownAnswer = "## Reverse a list\n\nUse **slices.Reverse**. It works in place:\n\n" +
	"- no copy\n- any element type\n\n```go\ns := []int{1, 2, 3}\nslices.Reverse(s)\n```\n"

// screen builds a chat screen of the given size, marks merud as reachable,
// asks q, and feeds it evs as merud's reply. When done is true the turn then
// ends, with doneErr as the connection's error.
func screen(t *testing.T, width, height int, q string, evs []rpc.Event, done bool, doneErr error) Model {
	t.Helper()
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: height}, pingMsg{})
	if q == "" {
		return m
	}
	m, _ = update(t, m, typeText(q), press(tea.KeyEnter))
	for _, ev := range evs {
		m, _ = update(t, m, eventMsg{turn: m.turn, ev: ev})
	}
	if done {
		m, _ = update(t, m, turnDoneMsg{turn: m.turn, err: doneErr})
	}
	return m
}

func TestViewGolden(t *testing.T) {
	session := rpc.Event{Type: rpc.EventSession, Session: "2026-09-23T101500-ab12"}
	direct := rpc.Event{Type: rpc.EventRoute, Route: "direct", Confidence: 0.91}
	fallback := rpc.Event{Type: rpc.EventRoute, Route: "search+tools", Confidence: 0.31, Fallback: true}
	stats := rpc.Event{Type: rpc.EventDone, TTFTMillis: 800, DurationMillis: 2400, TokensIn: 120, TokensOut: 64}
	tok := func(s string) rpc.Event { return rpc.Event{Type: rpc.EventToken, Text: s} }
	search := rpc.Event{Type: rpc.EventRoute, Route: "search", Confidence: 0.88}
	sources := rpc.Event{Type: rpc.EventSources, Sources: []rpc.Citation{
		{N: 1, Path: "~/notes/garden.md", Heading: "Budget", StartLine: 3, EndLine: 5, Score: 0.032},
		{N: 2, Path: "~/notes/plants.md", StartLine: 1, EndLine: 9, Score: 0.016},
	}}

	toolCall := func(id, name, args string) rpc.Event {
		return rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: id, Name: name, Kind: "mcp", Args: json.RawMessage(args)}}
	}
	toolResult := func(id, name, outcome string, ms int64) rpc.Event {
		return rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: id, Name: name, Kind: "mcp", Outcome: outcome, DurationMillis: ms}}
	}
	tools := rpc.Event{Type: rpc.EventRoute, Route: "tools", Confidence: 0.82}
	mailArgs := `{"to":"sam@example.com","subject":"Garden budget","body":"The Q3 budget is 4,200 dollars."}`
	mail := &rpc.Approval{ID: "1", Name: "mail.send", Kind: "mcp", Args: json.RawMessage(mailArgs),
		Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}}

	tests := []struct {
		name   string
		width  int
		q      string
		evs    []rpc.Event
		done   bool
		err    error
		height int
		// approval, when set, opens the approval box after the events.
		approval *rpc.Approval
	}{
		{name: "approval", width: 80, q: "Email Sam the garden budget", approval: mail,
			evs: []rpc.Event{session, tools, toolCall("1", "notes.search", `{"query":"garden budget"}`), toolResult("1", "notes.search", "ok", 120),
				toolCall("2", "mail.send", mailArgs)}},
		{name: "tools", width: 80, q: "Email Sam the garden budget", done: true,
			evs: []rpc.Event{session, tools, toolCall("1", "notes.search", `{"query":"garden budget"}`), toolResult("1", "notes.search", "ok", 120),
				toolCall("2", "mail.send", mailArgs), toolResult("2", "mail.send", "declined", 0),
				tok("I found the budget, 4,200 dollars, but didn't send the email."), stats}},
		{name: "approval-narrow", width: 40, q: "Email Sam", approval: mail,
			evs: []rpc.Event{session, tools, toolCall("2", "mail.send", mailArgs)}},
		{name: "empty", width: 80},
		{name: "waiting", width: 80, q: "What is Meru?", evs: []rpc.Event{session, direct}},
		{name: "streaming", width: 80, q: "What is Meru?", evs: []rpc.Event{session, direct, tok("Meru is a personal "), tok("assistant that runs")}},
		{name: "markdown", width: 80, q: "How do I reverse a slice in Go?", evs: []rpc.Event{session, direct, tok(markdownAnswer), stats}, done: true},
		{name: "fallback", width: 80, q: "Find my notes on Rust", evs: []rpc.Event{session, fallback, tok("I can't search yet."), stats}, done: true},
		{name: "error", width: 80, q: "Hello?", err: errors.New("connect to merud at /home/u/.meru/merud.sock: no such file (is merud running?)"), done: true},
		{name: "stopped", width: 80, q: "Tell me a long story", evs: []rpc.Event{session, direct, tok("Once upon a time")}},
		{name: "sources", width: 80, q: "What is the Q3 budget for the garden project?", evs: []rpc.Event{session, search, sources, tok("The Q3 budget for the garden project is 4,200 dollars [1]."), stats}, done: true},
		{name: "narrow", width: 40, q: "How do I reverse a slice in Go?", evs: []rpc.Event{session, direct, tok(markdownAnswer), stats}, done: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			height := tt.height
			if height == 0 {
				height = 30
			}
			m := screen(t, tt.width, height, tt.q, tt.evs, tt.done, tt.err)
			if tt.name == "stopped" {
				m, _ = update(t, m, press(tea.KeyCtrlC))
			}
			if tt.approval != nil {
				m, _ = update(t, m, approvalRequestMsg{turn: m.turn, approval: *tt.approval, reply: make(chan rpc.Choice, 1)})
			}
			view := m.View()

			// Every line must fit the terminal, or it would wrap and push
			// the screen out of shape.
			lines := strings.Split(view, "\n")
			if len(lines) != height {
				t.Errorf("view has %d lines, want %d", len(lines), height)
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > tt.width {
					t.Errorf("line %d is %d wide, want at most %d: %q", i+1, w, tt.width, l)
				}
			}
			golden(t, tt.name, view)
		})
	}
}

// golden compares got with testdata/<name>.golden, or writes the file when
// the -update flag is set. Trailing spaces are trimmed from each line first,
// so editors that strip them don't break the test.
func golden(t *testing.T, name, got string) {
	t.Helper()
	got = trimLines(got)
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- a fixed name under testdata
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("view differs from %s (run with -update if the change is deliberate)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// trimLines removes trailing spaces from every line of s and ends it with
// one newline.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestTidy checks that tidy drops Glamour's blank edges, even when colour
// codes wrap the padding, and keeps blank lines inside the answer.
func TestTidy(t *testing.T) {
	in := "\n\x1b[38;5;252m    \x1b[0m\n  one  \n\n  two\n\x1b[0m  \n\n"
	if got, want := tidy(in), "  one\n\n  two"; got != want {
		t.Errorf("tidy = %q, want %q", got, want)
	}
}

// TestHeaderStatus checks the connection status on the right of the header
// through its three states.
func TestHeaderStatus(t *testing.T) {
	m := testModel(nil, newFakeSender())
	if h := m.header(); !strings.Contains(h, "● connecting…") {
		t.Errorf("header before ping = %q, want connecting", h)
	}
	m, _ = update(t, m, pingMsg{})
	if h := m.header(); !strings.Contains(h, "● connected") {
		t.Errorf("header after ping = %q, want connected", h)
	}
	m, _ = update(t, m, pingMsg{err: errors.New("no socket")})
	if h := m.header(); !strings.Contains(h, "● merud not running") {
		t.Errorf("header after failed ping = %q, want not running", h)
	}
}

// TestStatsLine checks the numbers under a finished answer.
func TestStatsLine(t *testing.T) {
	tests := []struct {
		name  string
		stats rpc.Event
		want  string
	}{
		{"full", rpc.Event{TTFTMillis: 800, DurationMillis: 2400, TokensOut: 64}, "0.8s to first token · 40.0 tok/s · 2.4s"},
		{"runtime's writing time", rpc.Event{TTFTMillis: 800, DurationMillis: 2400, TokensOut: 64, EvalMillis: 1000}, "0.8s to first token · 64.0 tok/s · 2.4s"},
		{"no tokens", rpc.Event{TTFTMillis: 500, DurationMillis: 900}, "0.5s to first token · 0.9s"},
		{"older merud sends none", rpc.Event{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statsLine(&exchange{stats: tt.stats}); got != tt.want {
				t.Errorf("statsLine = %q, want %q", got, tt.want)
			}
		})
	}
}
