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
	"time"

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
		{N: 1, Path: "~/notes/garden.md", Heading: "Planting", StartLine: 3, EndLine: 5, Score: 0.032},
		{N: 2, Path: "~/notes/plants.md", StartLine: 1, EndLine: 9, Score: 0.016},
	}}

	toolCall := func(id, name, args string) rpc.Event {
		return rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: id, Name: name, Kind: "mcp", Args: json.RawMessage(args)}}
	}
	toolResult := func(id, name, outcome string, ms int64) rpc.Event {
		return rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: id, Name: name, Kind: "mcp", Outcome: outcome, DurationMillis: ms}}
	}
	tools := rpc.Event{Type: rpc.EventRoute, Route: "tools", Confidence: 0.82}
	mailArgs := `{"to":"sam@example.com","subject":"Garden plan","body":"We sow the tomatoes on 12 April."}`
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
		// index and usage, when set, arrive as status and usage replies
		// before anything else; usageBox then types /usage, meBox types
		// /me and answers it with profileFixture, and mcpBox types /mcp
		// and answers it with mcpFixture.
		index    *rpc.IndexStatus
		usage    []rpc.UsageWindow
		usageBox bool
		meBox    bool
		mcpBox   bool
		// queued, when set, are typed and sent while the turn runs, so
		// they wait in the queue.
		queued []string
	}{
		{name: "approval", width: 80, q: "Email Sam the garden plan", approval: mail,
			evs: []rpc.Event{session, tools, toolCall("1", "notes.search", `{"query":"garden plan"}`), toolResult("1", "notes.search", "ok", 120),
				toolCall("2", "mail.send", mailArgs)}},
		{name: "tools", width: 80, q: "Email Sam the garden plan", done: true,
			evs: []rpc.Event{session, tools, toolCall("1", "notes.search", `{"query":"garden plan"}`), toolResult("1", "notes.search", "ok", 120),
				toolCall("2", "mail.send", mailArgs), toolResult("2", "mail.send", "declined", 0),
				tok("I found the plan, tomatoes on 12 April, but didn't send the email."), stats}},
		{name: "approval-narrow", width: 40, q: "Email Sam", approval: mail,
			evs: []rpc.Event{session, tools, toolCall("2", "mail.send", mailArgs)}},
		{name: "empty", width: 80},
		{name: "waiting", width: 80, q: "What is Meru?", evs: []rpc.Event{session, direct}},
		{name: "streaming", width: 80, q: "What is Meru?", evs: []rpc.Event{session, direct, tok("Meru is a personal "), tok("assistant that runs")}},
		{name: "queued", width: 80, q: "What should I plant in April?", evs: []rpc.Event{session, direct, tok("Sow tomatoes and beans ")}, queued: gardenQueue},
		{name: "queued-narrow", width: 40, q: "What should I plant in April?", evs: []rpc.Event{session, direct, tok("Sow tomatoes and beans ")}, queued: gardenQueue},
		{name: "markdown", width: 80, q: "How do I reverse a slice in Go?", evs: []rpc.Event{session, direct, tok(markdownAnswer), stats}, done: true},
		{name: "skills", width: 80, q: "Write a short email to my landlord", evs: []rpc.Event{session,
			{Type: rpc.EventRoute, Route: "direct", Confidence: 0.91, Skills: []rpc.SkillInfo{{Name: "writing"}}},
			tok("Dear Ms Reyes, the kitchen tap still drips."), stats}, done: true},
		{name: "fallback", width: 80, q: "Find my notes on Rust", evs: []rpc.Event{session, fallback, tok("I can't search yet."), stats}, done: true},
		{name: "error", width: 80, q: "Hello?", err: errors.New("connect to merud at /home/u/.meru/merud.sock: no such file (is merud running?)"), done: true},
		{name: "stopped", width: 80, q: "Tell me a long story", evs: []rpc.Event{session, direct, tok("Once upon a time")}},
		{name: "sources", width: 80, q: "When does the garden project sow tomatoes?", evs: []rpc.Event{session, search, sources, tok("The garden project sows tomatoes on 12 April [1]."), stats}, done: true},
		{name: "narrow", width: 40, q: "How do I reverse a slice in Go?", evs: []rpc.Event{session, direct, tok(markdownAnswer), stats}, done: true},
		// The header at three widths: everything; the usage dropped; then
		// the vectors, size and memory count dropped too.
		{name: "header-wide", width: 130, height: 8, index: bigIndex, usage: usageFixture},
		{name: "header", width: 100, height: 8, index: bigIndex, usage: usageFixture},
		{name: "header-narrow", width: 60, height: 8, index: bigIndex, usage: usageFixture},
		// merud knows nothing about the user: the nudge under the hint,
		// and the marker in the header.
		{name: "no-profile", width: 80, height: 12, index: noProfileIndex},
		{name: "me", width: 80, height: 20, index: bigIndex, meBox: true},
		{name: "me-narrow", width: 40, height: 24, index: bigIndex, meBox: true},
		{name: "usage", width: 80, height: 24, index: bigIndex, usage: usageFixture, usageBox: true},
		{name: "usage-narrow", width: 40, height: 24, index: bigIndex, usage: usageFixture, usageBox: true},
		{name: "mcp", width: 100, height: 20, index: bigIndex, mcpBox: true},
		{name: "mcp-narrow", width: 40, height: 20, index: bigIndex, mcpBox: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			height := tt.height
			if height == 0 {
				height = 30
			}
			m := screen(t, tt.width, height, tt.q, tt.evs, tt.done, tt.err)
			if tt.index != nil || tt.usage != nil {
				m, _ = update(t, m, pingMsg{index: tt.index}, usageMsg{windows: tt.usage, answered: true})
			}
			if tt.usageBox {
				m, _ = update(t, m, typeText("/usage"), press(tea.KeyEnter), usageMsg{windows: tt.usage, answered: true})
			}
			if tt.meBox {
				m, _ = update(t, m, typeText("/me"), press(tea.KeyEnter), meMsg{memories: profileFixture})
			}
			if tt.mcpBox {
				m, _ = update(t, m, typeText("/mcp"), press(tea.KeyEnter), mcpMsg{rows: mcpFixture})
			}
			for _, q := range tt.queued {
				m, _ = update(t, m, typeText(q), press(tea.KeyEnter))
			}
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

// gardenQueue holds two follow-up questions typed while an answer streams.
// The second is long enough to wrap at 40 columns.
var gardenQueue = []string{"Which of those grow in shade?", "And how far apart should I plant them along a fence?"}

// bigIndex is an index status with numbers of a realistic size, from a
// user who has told Meru about themselves.
var bigIndex = &rpc.IndexStatus{Documents: 2637, Chunks: 11698, Vectors: 11698, DBBytes: 88_080_384, Memories: 7, Profile: 3}

// noProfileIndex is bigIndex before the user has told Meru anything.
var noProfileIndex = &rpc.IndexStatus{Documents: 2637, Chunks: 11698, Vectors: 11698, DBBytes: 88_080_384}

// profileFixture is the profile part of merud's answer to OpMemoryList.
var profileFixture = []rpc.MemoryInfo{
	{ID: "me/name-dana-reyes.md", Kind: "me", Text: "Name: Dana Reyes"},
	{ID: "preferences/answers-short.md", Kind: "preferences", Text: "Answers: short, with bullet points"},
	{ID: "me/work.md", Kind: "me", Text: "Work: staff engineer on the registry team at Acme, in the platform group"},
}

// usageFixture is a usage reply with every window, in merud's order.
var usageFixture = []rpc.UsageWindow{
	{Name: rpc.Usage1h, Sessions: 1, Turns: 4, TokensIn: 18_000, TokensOut: 2_100, ActiveMillis: 134_000, Docs: 3, ToolCalls: 1},
	{Name: rpc.UsageToday, Sessions: 2, Turns: 9, TokensIn: 41_200, TokensOut: 5_300, ActiveMillis: 301_000, Docs: 7, ToolCalls: 2},
	{Name: rpc.UsageWeek, Sessions: 5, Turns: 31, TokensIn: 150_000, TokensOut: 19_400, ActiveMillis: 1_210_000, Docs: 22, ToolCalls: 6},
	{Name: rpc.UsageMonth, Sessions: 12, Turns: 88, TokensIn: 420_000, TokensOut: 61_000, ActiveMillis: 3_700_000, Docs: 51, ToolCalls: 14},
	{Name: rpc.Usage30d, Sessions: 14, Turns: 97, TokensIn: 468_000, TokensOut: 66_500, ActiveMillis: 4_020_000, Docs: 55, ToolCalls: 15},
	{Name: rpc.UsageLifetime, Sessions: 30, Turns: 212, TokensIn: 1_400_000, TokensOut: 180_000, ActiveMillis: 11_100_000, Docs: 140, ToolCalls: 40},
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

// TestHeadingMarks checks that headings drop their "##" marks in the colour
// styles and keep them in "notty", which has no bold to set them apart.
func TestHeadingMarks(t *testing.T) {
	tests := []struct {
		style     string
		wantMarks bool
	}{
		{"dark", false},
		{"light", false},
		{"notty", true},
		{"no such style", false}, // falls back to dark
	}
	for _, tt := range tests {
		t.Run(tt.style, func(t *testing.T) {
			m := &Model{width: 80, look: look{renderer: plainLook().renderer, markdownStyle: tt.style}}
			out, err := m.renderMarkdown("## Setup\n\n### htop\n\nText.")
			if err != nil {
				t.Fatal(err)
			}
			plain := ansi.Strip(out)
			if !strings.Contains(plain, "Setup") || !strings.Contains(plain, "htop") {
				t.Fatalf("headings missing:\n%s", plain)
			}
			if got := strings.Contains(plain, "##"); got != tt.wantMarks {
				t.Errorf("marks shown = %v, want %v:\n%s", got, tt.wantMarks, plain)
			}
		})
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

// TestHeaderDocCount checks the index's document count in the header: left
// out until merud answers, then updated by each status check, and kept when
// a later check fails.
func TestHeaderDocCount(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	if h := m.header(); strings.Contains(h, "doc") {
		t.Errorf("header before any status = %q, want no doc count", h)
	}
	m, _ = update(t, m, pingMsg{index: &rpc.IndexStatus{Documents: 68}})
	if h := m.header(); !strings.Contains(h, "68 docs") {
		t.Errorf("header = %q, want 68 docs", h)
	}
	m, _ = update(t, m, pingMsg{index: &rpc.IndexStatus{Documents: 1}})
	if h := m.header(); !strings.Contains(h, "· 1 doc") || strings.Contains(h, "1 docs") {
		t.Errorf("header = %q, want 1 doc", h)
	}
	m, _ = update(t, m, pingMsg{err: errors.New("no socket")})
	if h := m.header(); !strings.Contains(h, "1 doc") {
		t.Errorf("header after a failed check = %q, want the last count kept", h)
	}
}

// TestHeaderIndexing checks the marker that says a scan is still running.
func TestHeaderIndexing(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	// -1 memories: this merud couldn't count them, so the header says
	// nothing about them.
	m, _ = update(t, m, pingMsg{index: &rpc.IndexStatus{Documents: 2637, Vectors: 11698, Scanning: true, Memories: -1, Profile: -1}})
	if h := m.header(); !strings.Contains(h, "2637 docs (11698 vectors) · indexing") {
		t.Errorf("header = %q, want the count and the indexing marker", h)
	}
	m, _ = update(t, m, pingMsg{index: &rpc.IndexStatus{Documents: 2700}})
	if h := m.header(); !strings.Contains(h, "2700 docs") || strings.Contains(h, "indexing") {
		t.Errorf("header = %q, want the new count without the marker", h)
	}
}

// TestRefresh checks the periodic status check: a refreshMsg asks merud
// again and books the next check, sooner while a scan runs.
func TestRefresh(t *testing.T) {
	tests := []struct {
		name string
		ix   *rpc.IndexStatus
		want time.Duration
	}{
		{"before any answer", nil, refreshScanning},
		{"while scanning", &rpc.IndexStatus{Scanning: true}, refreshScanning},
		{"idle", &rpc.IndexStatus{Documents: 5}, refreshIdle},
	}
	for _, tt := range tests {
		if got := nextRefresh(tt.ix); got != tt.want {
			t.Errorf("%s: nextRefresh = %v, want %v", tt.name, got, tt.want)
		}
	}
	m := testModel(nil, newFakeSender())
	if _, cmd := update(t, m, refreshMsg{}); cmd == nil {
		t.Errorf("refreshMsg gave no command; want a check and the next tick")
	}
}

// TestPingCmdReadsIndexStatus checks that the status check asks for the
// index status and hands its numbers to the model.
func TestPingCmdReadsIndexStatus(t *testing.T) {
	f := &fakeMerud{events: []rpc.Event{
		{Type: rpc.EventStatus, Status: &rpc.IndexStatus{Documents: 68, Chunks: 900}},
		{Type: rpc.EventDone},
	}}
	msg := pingCmd(f.ask)()
	pm, ok := msg.(pingMsg)
	if !ok || pm.err != nil || pm.index == nil || pm.index.Documents != 68 {
		t.Fatalf("pingCmd = %+v, want 68 documents and no error", msg)
	}
	if len(f.reqs) != 1 || f.reqs[0].Op != rpc.OpIndexStatus {
		t.Errorf("requests = %+v, want one index_status", f.reqs)
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
