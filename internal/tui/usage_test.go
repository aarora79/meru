// This file tests the usage parts of the chat screen: the /usage command
// and its box, the keys while the box is open, unknown commands, the
// header's last-hour summary and index sizes, and the usage request.

package tui

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aarora79/meru/internal/rpc"
)

// usageReply is merud's answer to OpUsage in these tests.
var usageReply = []rpc.Event{{Type: rpc.EventUsage, Usage: usageFixture}, {Type: rpc.EventDone}}

// openUsageBox types /usage, presses Enter, runs the usage requests against
// merud and feeds their answers back, as Bubble Tea would.
func openUsageBox(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(t, m, typeText("/usage"), press(tea.KeyEnter))
	if m.usageBox == nil || !m.usageBox.loading {
		t.Fatalf("usage box = %+v after /usage, want it open and waiting", m.usageBox)
	}
	if cmd == nil {
		t.Fatal("/usage returned no command, want the usage requests")
	}
	return runBatch(t, m, cmd)
}

// runBatch runs cmd and feeds its message back. tea.Batch returns a
// command whose message is a tea.BatchMsg, a list of commands; runBatch
// runs each of those in turn, as Bubble Tea would.
func runBatch(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		m, _ = update(t, m, msg)
		return m
	}
	for _, c := range batch {
		if c != nil {
			m, _ = update(t, m, c())
		}
	}
	return m
}

func TestSlashUsageOpensBox(t *testing.T) {
	merud := &fakeMerud{events: usageReply}
	m := openUsageBox(t, testModel(merud.ask, newFakeSender()))

	want := []rpc.Request{{Op: rpc.OpUsage}, {Op: rpc.OpUsage, Kind: rpc.UsageDetail}}
	if len(merud.reqs) != 2 || merud.reqs[0] != want[0] || merud.reqs[1] != want[1] {
		t.Errorf("requests = %+v, want %+v", merud.reqs, want)
	}
	if len(m.turns) != 0 || m.streaming {
		t.Errorf("turns = %+v, streaming = %v; /usage must not ask the model", m.turns, m.streaming)
	}
	if m.input.Value() != "" {
		t.Errorf("input = %q, want it cleared", m.input.Value())
	}
	b := m.usageBox
	if b == nil || b.loading || b.err != "" || len(b.windows) != len(usageFixture) {
		t.Fatalf("usage box = %+v, want the six windows", b)
	}
	if len(m.usage) != len(usageFixture) {
		t.Errorf("header usage = %+v, want the reply kept for the header too", m.usage)
	}
	view := m.View()
	for _, want := range []string{"Usage", "tokens in", rpc.UsageNote, "esc/q close"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestSlashUsageWhileStreaming(t *testing.T) {
	merud := &fakeMerud{events: usageReply}
	m := testModel(merud.ask, newFakeSender())
	m.turns = []exchange{{question: "q", state: stateActive}}
	m.streaming = true
	m = openUsageBox(t, m)
	if !m.streaming || len(m.turns) != 1 {
		t.Errorf("streaming = %v, turns = %d; /usage must leave the answer running", m.streaming, len(m.turns))
	}
}

func TestUsageBoxKeys(t *testing.T) {
	tests := []struct {
		name      string
		key       tea.Msg
		streaming bool
		wantOpen  bool
		wantQuit  bool
	}{
		{"esc closes", press(tea.KeyEsc), false, false, false},
		{"q closes", typeText("q"), false, false, false},
		{"other letters do nothing", typeText("x"), false, true, false},
		{"enter does nothing", press(tea.KeyEnter), false, true, false},
		{"up does nothing", press(tea.KeyUp), false, true, false},
		{"ctrl-c quits when idle", press(tea.KeyCtrlC), false, true, true},
		{"ctrl-c stops a streaming answer", press(tea.KeyCtrlC), true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merud := &fakeMerud{events: usageReply}
			m := testModel(merud.ask, newFakeSender())
			m.lastQuestion = "earlier"
			if tt.streaming {
				m.turns = []exchange{{question: "q", state: stateActive}}
				m.streaming = true
			}
			m = openUsageBox(t, m)

			m, cmd := update(t, m, tt.key)
			if open := m.usageBox != nil; open != tt.wantOpen {
				t.Errorf("box open = %v, want %v", open, tt.wantOpen)
			}
			quit := cmd != nil && cmd() == tea.Quit()
			if quit != tt.wantQuit {
				t.Errorf("quit = %v, want %v", quit, tt.wantQuit)
			}
			if m.input.Value() != "" {
				t.Errorf("input = %q, want the key kept out of it", m.input.Value())
			}
			if len(merud.reqs) != 2 {
				t.Errorf("requests = %d, want only the two usage requests", len(merud.reqs))
			}
			if tt.streaming && m.streaming {
				t.Error("still streaming after Ctrl-C")
			}
		})
	}
}

func TestUsageBoxClosedByApproval(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.turn = 1
	m.turns = []exchange{{question: "q", state: stateActive}}
	m.streaming = true
	m.usageBox = &usageBox{windows: usageFixture}
	m, _ = update(t, m, approvalRequestMsg{turn: 1, approval: rpc.Approval{Name: "mail.send", Choices: allChoices}, reply: make(chan rpc.Choice, 1)})
	if m.usageBox != nil || m.approval == nil {
		t.Errorf("usage box = %v, approval = %v; want the approval box alone", m.usageBox, m.approval)
	}
}

func TestUnknownCommand(t *testing.T) {
	merud := &fakeMerud{events: reply}
	m := testModel(merud.ask, newFakeSender())
	m, cmd := update(t, m, typeText("/usag"), press(tea.KeyEnter))
	if cmd != nil || len(merud.reqs) != 0 || len(m.turns) != 0 {
		t.Errorf("cmd = %v, requests = %+v, turns = %+v; want nothing sent", cmd != nil, merud.reqs, m.turns)
	}
	if m.input.Value() != "/usag" {
		t.Errorf("input = %q, want the text kept to fix", m.input.Value())
	}
	lines := strings.Split(m.View(), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "unknown command /usag") || !strings.Contains(last, "/help") {
		t.Errorf("help line = %q, want the unknown command and /help", last)
	}
	// The next key brings the help line back.
	m, _ = update(t, m, press(tea.KeyBackspace))
	lines = strings.Split(m.View(), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "enter send") {
		t.Errorf("help line after a key = %q, want the keys again", last)
	}
}

// TestApplyUsage checks what each kind of usage reply does to the header
// and to a waiting box.
func TestApplyUsage(t *testing.T) {
	unknownOp := usageMsg{answered: true, err: errors.New(`unknown op "usage"`)}
	unreachable := usageMsg{err: errors.New("connect: no such file")}

	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, pingMsg{}, usageMsg{windows: usageFixture, answered: true})
	if lastHour(m.usage) == "" {
		t.Fatal("no last-hour summary after a usage reply")
	}
	m, _ = update(t, m, unreachable)
	if lastHour(m.usage) == "" {
		t.Error("an unreachable merud dropped the summary; want the last numbers kept")
	}
	m, _ = update(t, m, unknownOp)
	if m.usage != nil {
		t.Errorf("usage = %+v after unknown op, want none", m.usage)
	}
	if m.link != linkUp {
		t.Errorf("link = %v after unknown op, want merud still up", m.link)
	}

	m.usageBox = &usageBox{loading: true}
	m, _ = update(t, m, unknownOp)
	if m.usageBox.loading || !strings.Contains(m.usageBox.err, "unknown op") {
		t.Errorf("box = %+v, want the error shown", m.usageBox)
	}
	if view := m.View(); !strings.Contains(view, "merud gave no usage numbers") {
		t.Errorf("view lacks the error:\n%s", view)
	}
	// A later reply leaves a box that already shows something alone.
	m, _ = update(t, m, usageMsg{windows: usageFixture, answered: true})
	if m.usageBox.windows != nil {
		t.Errorf("box windows = %+v, want the box unchanged", m.usageBox.windows)
	}
}

func TestUsageCmd(t *testing.T) {
	tests := []struct {
		name         string
		merud        *fakeMerud
		wantWindows  int
		wantAnswered bool
		wantErr      bool
	}{
		{"answer", &fakeMerud{events: usageReply}, len(usageFixture), true, false},
		{"older merud", &fakeMerud{events: []rpc.Event{{Type: rpc.EventError, Error: `unknown op "usage"`}}}, 0, true, true},
		{"merud down", &fakeMerud{err: errors.New("no socket")}, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := usageCmd(tt.merud.ask)().(usageMsg)
			if !ok {
				t.Fatal("usageCmd returned no usageMsg")
			}
			if len(msg.windows) != tt.wantWindows || msg.answered != tt.wantAnswered || (msg.err != nil) != tt.wantErr {
				t.Errorf("usageCmd = %+v, want %d windows, answered %v, error %v", msg, tt.wantWindows, tt.wantAnswered, tt.wantErr)
			}
			if len(tt.merud.reqs) != 1 || tt.merud.reqs[0].Op != rpc.OpUsage {
				t.Errorf("requests = %+v, want one usage request", tt.merud.reqs)
			}
		})
	}
}

// TestRefreshAsksForUsage checks that the status timer and the end of an
// answer both ask merud for usage, next to the index status.
func TestRefreshAsksForUsage(t *testing.T) {
	tests := []struct {
		msg tea.Msg
		// requests is how many of the batch's commands are requests. The
		// timer's batch ends with the next tick, which would wait for it,
		// so the test runs only the requests.
		requests int
	}{
		{refreshMsg{}, 2},
		{turnDoneMsg{}, 2},
	}
	for _, tt := range tests {
		merud := &fakeMerud{}
		m := testModel(merud.ask, newFakeSender())
		_, cmd := update(t, m, tt.msg)
		batch, ok := cmd().(tea.BatchMsg)
		if !ok || len(batch) < tt.requests {
			t.Fatalf("%T gave %v, want a batch of at least %d", tt.msg, batch, tt.requests)
		}
		for _, c := range batch[:tt.requests] {
			c()
		}
		ops := map[rpc.Op]bool{}
		for _, r := range merud.reqs {
			ops[r.Op] = true
		}
		if !ops[rpc.OpIndexStatus] || !ops[rpc.OpUsage] {
			t.Errorf("%T asked %+v, want index_status and usage", tt.msg, merud.reqs)
		}
	}
}

func TestLastHour(t *testing.T) {
	tests := []struct {
		name    string
		windows []rpc.UsageWindow
		want    string
	}{
		{"none", nil, ""},
		{"no 1h window", []rpc.UsageWindow{{Name: rpc.UsageToday, Turns: 3}}, ""},
		{"idle hour", []rpc.UsageWindow{{Name: rpc.Usage1h}}, "1h: 0 tasks"},
		{"one question", []rpc.UsageWindow{{Name: rpc.Usage1h, Turns: 1, TokensIn: 950, TokensOut: 120}}, "1h: 1 task · 950 in · 120 out"},
		{"busy hour", usageFixture, "1h: 4 tasks · 18k in · 2.1k out"},
	}
	for _, tt := range tests {
		if got := lastHour(tt.windows); got != tt.want {
			t.Errorf("%s: lastHour = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestDocCountSizes(t *testing.T) {
	tests := []struct {
		name  string
		ix    *rpc.IndexStatus
		sizes bool
		want  string
	}{
		{"before merud answers", nil, true, ""},
		{"full", bigIndex, true, "2637 docs (11698 vectors, 84 MB)"},
		{"without sizes", bigIndex, false, "2637 docs"},
		{"older merud sends no size", &rpc.IndexStatus{Documents: 2637, Vectors: 11698}, true, "2637 docs (11698 vectors)"},
		{"one of each", &rpc.IndexStatus{Documents: 1, Vectors: 1, DBBytes: 4096}, true, "1 doc (1 vector, 4.0 KB)"},
		{"scanning", &rpc.IndexStatus{Documents: 5, Vectors: 9, DBBytes: 1 << 30, Scanning: true}, true, "5 docs (9 vectors, 1.0 GB) · indexing"},
		{"re-indexing for a new embedding model", &rpc.IndexStatus{Documents: 900, Chunks: 8400, Vectors: 1200, Scanning: true}, false, "900 docs · re-indexing 1.2k of 8.4k"},
	}
	for _, tt := range tests {
		if got := docCount(tt.ix, tt.sizes); got != tt.want {
			t.Errorf("%s: docCount = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestHeaderDropsUsageFirst narrows the screen one column at a time and
// checks the order in which the header gives things up: the usage, then
// the index sizes, then the rest of the details.
func TestHeaderDropsUsageFirst(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, pingMsg{index: bigIndex}, usageMsg{windows: usageFixture, answered: true})
	sawSizesWithoutUsage := false
	for width := 140; width >= 30; width-- {
		m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		h := m.header()
		if w := lipgloss.Width(h); w > width {
			t.Fatalf("width %d: header is %d wide: %q", width, w, h)
		}
		hasUsage := strings.Contains(h, "1h:")
		hasSizes := strings.Contains(h, "(11698 vectors, 84 MB)")
		if strings.Contains(h, "7 memories") != hasSizes {
			t.Errorf("width %d: the memory count and the sizes must go together: %q", width, h)
		}
		if hasUsage && !hasSizes {
			t.Errorf("width %d: usage kept but sizes dropped: %q", width, h)
		}
		if hasSizes && !hasUsage {
			sawSizesWithoutUsage = true
		}
		if strings.Contains(h, "(") && !hasSizes {
			t.Errorf("width %d: a cut bracket: %q", width, h)
		}
		if !strings.Contains(h, "● connected") {
			t.Errorf("width %d: status gone: %q", width, h)
		}
	}
	if !sawSizesWithoutUsage {
		t.Error("no width showed the sizes without the usage")
	}
}

// detailFixture is a dashboard as merud sends it: 14 days with questions
// on some, two slow questions, one tool with failed calls and one without,
// and the /metrics address. The slow questions' times are built from local
// time, so the box shows "3 Oct 10:12" in any time zone.
func detailFixture() rpc.UsageDetailReply {
	var days []rpc.UsageDay
	first := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	for i := range 14 {
		days = append(days, rpc.UsageDay{Date: first.AddDate(0, 0, i).Format(time.DateOnly), Questions: i % 5})
	}
	days[13].Questions = 8
	days[13].TTFTp50Millis, days[13].TTFTp95Millis = 1200, 4800
	at := func(day, hour, minute int) string {
		return time.Date(2026, 10, day, hour, minute, 0, 0, time.Local).UTC().Format(time.RFC3339)
	}
	return rpc.UsageDetailReply{
		Days: days,
		Slowest: []rpc.SlowQuestion{
			{Session: "s1", At: at(3, 10, 12), Model: "qwen3.6:35b", Millis: 42100, TTFT: 38000},
			{Session: "s2", At: at(8, 9, 5), Model: "qwen3.6:35b", Millis: 12500, TTFT: 900},
		},
		Tools: []rpc.ToolOutcomes{
			{Tool: "web_fetch", Kind: "builtin", Calls: 12, Outcomes: map[string]int{"ok": 9, "error": 2, "timeout": 1}},
			{Tool: "datetime", Kind: "builtin", Calls: 4, Outcomes: map[string]int{"ok": 4}},
		},
		Metrics: "http://127.0.0.1:9464/metrics",
	}
}

// TestUsageBoxDetail checks the dashboard under /usage's table: the bar
// line, today's time to first token, the slowest questions, only the tools
// with failed calls, and the Prometheus line, with the address or with how
// to turn it on. A merud too old for the dashboard leaves a line that says
// so.
func TestUsageBoxDetail(t *testing.T) {
	// byKind answers the windows request and the dashboard request apart,
	// which fakeMerud, with one list of events, can't.
	byKind := func(_ context.Context, req rpc.Request, _ rpc.ApproveFunc) iter.Seq2[rpc.Event, error] {
		return func(yield func(rpc.Event, error) bool) {
			if req.Kind == rpc.UsageDetail {
				d := detailFixture()
				yield(rpc.Event{Type: rpc.EventUsageDetail, UsageDetail: &d}, nil)
				return
			}
			for _, ev := range usageReply {
				if !yield(ev, nil) {
					return
				}
			}
		}
	}
	m := openUsageBox(t, testModel(byKind, newFakeSender()))
	if m.usageBox.detail == nil {
		t.Fatalf("usage box = %+v, want the dashboard", m.usageBox)
	}
	text := strings.Join(m.usageDetailLines(m.usageBox, 76), "\n")
	for _, want := range []string{
		"Tasks per day, last 14 days (most 8)", "Tokens in per day", "Tokens today:", "█", "26 Sep to 9 Oct",
		"First token today: p50 1.2s · p95 4.8s",
		"Slowest this week", "42s  first token 38s", "3 Oct 10:12", "qwen3.6:35b",
		"web_fetch  12 calls · error 2 · timeout 1",
		"Prometheus can read these at http://127.0.0.1:9464/metrics",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("dashboard lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "datetime") {
		t.Errorf("dashboard lists a tool with no failed calls:\n%s", text)
	}

	m.usageBox.detail.Metrics = ""
	if text := strings.Join(m.usageDetailLines(m.usageBox, 76), "\n"); !strings.Contains(text, `prometheus = "127.0.0.1:9464"`) {
		t.Errorf("with /metrics off, the dashboard doesn't say how to turn it on:\n%s", text)
	}

	// An older merud answers the dashboard request with plain windows.
	old := openUsageBox(t, testModel((&fakeMerud{events: usageReply}).ask, newFakeSender()))
	if text := strings.Join(old.usageDetailLines(old.usageBox, 76), "\n"); !strings.Contains(text, "need a newer merud") {
		t.Errorf("against an older merud the box says:\n%s", text)
	}
}

func TestBarLine(t *testing.T) {
	tests := []struct {
		in   []int64
		want string
	}{
		{nil, ""},
		{[]int64{0, 0}, "  "},
		{[]int64{1, 8, 0, 4}, "▁█ ▄"},
		{[]int64{1, 1000}, "▁█"},
	}
	for _, tt := range tests {
		if got := barLine(tt.in); got != tt.want {
			t.Errorf("barLine(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
