// This file holds what the chat screen shows of merud's usage numbers: the
// /usage box that opens over the conversation, with the windows table and
// the dashboard under it, its `/usage by model` form with a row per answer
// model, and the last hour's summary for the header.

package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// usageBox is the open /usage box. It opens at once with loading set, and
// the next usage reply of its kind fills in windows or err. byModel is
// true for `/usage by model`. The plain box also asks for the dashboard;
// detail holds it once it comes, and detailErr says why it didn't.
type usageBox struct {
	loading bool
	byModel bool
	windows []rpc.UsageWindow
	err     string // why merud gave no numbers

	detail    *rpc.UsageDetailReply
	detailErr string
}

// usageDays is how many days the box's bar line shows: two weeks fit an
// 80-column terminal beside the labels.
const usageDays = 14

// usageSlowest is how many of the slowest questions the box lists; the
// desktop app lists all ten.
const usageSlowest = 5

// bars are the eight heights of a text bar, lowest first.
var bars = []rune("▁▂▃▄▅▆▇█")

// usageCommand runs /usage with its argument: alone it opens the usage
// box, and "by model" opens it with a row per answer model. Any other
// argument leaves the text in the input and says what /usage takes.
func (m Model) usageCommand(arg string) (tea.Model, tea.Cmd) {
	byModel := strings.Join(strings.Fields(arg), " ") == "by model"
	if arg != "" && !byModel {
		m.notice = "/usage takes nothing, or by model"
		return m, nil
	}
	m.input.Reset()
	m.layout()
	m.usageBox = &usageBox{loading: true, byModel: byModel}
	m.input.Blur() // the input takes no text while the box is open
	if byModel {
		return m, askUsage(m.ask, true)
	}
	return m, tea.Batch(askUsage(m.ask, false), askUsageDetail(m.ask))
}

// applyUsageDetail puts the dashboard in the open plain /usage box. With
// no such box, or one already filled, the reply has nowhere to go.
func (m *Model) applyUsageDetail(msg usageDetailMsg) {
	b := m.usageBox
	if b == nil || b.byModel || b.detail != nil {
		return
	}
	if msg.err != nil {
		b.detailErr = msg.err.Error()
		return
	}
	b.detail = &msg.detail
	b.detailErr = ""
}

// applyUsage takes in one usage reply. The header keeps the windows for its
// last-hour summary. When merud answered with an error, which is what an
// older merud without OpUsage does, the header drops the summary. When
// merud couldn't be reached at all, the header keeps the last numbers, as
// it keeps the document count; the status check alone decides whether
// merud is up.
//
// An open box that still waits takes the first reply of its kind,
// whichever request it answers: all of them ask for the same numbers. A
// reply by model never reaches the header, whose summary reads windows of
// time.
func (m *Model) applyUsage(msg usageMsg) {
	switch {
	case msg.byModel:
	case msg.err == nil:
		m.usage = msg.windows
	case msg.answered:
		m.usage = nil
	}
	if b := m.usageBox; b != nil && b.loading && b.byModel == msg.byModel {
		b.loading = false
		b.windows = msg.windows
		if msg.err != nil {
			b.err = msg.err.Error()
		}
	}
}

// lastHour writes the header's usage summary from the 1h window, such as
// "1h: 4 tasks · 18k in · 2.1k out". It returns "" when merud sent no
// such window.
func lastHour(windows []rpc.UsageWindow) string {
	for _, w := range windows {
		if w.Name != rpc.Usage1h {
			continue
		}
		tasks := fmt.Sprintf("%d tasks", w.Turns)
		switch w.Turns {
		case 0:
			// Token counts of zero add nothing.
			return "1h: 0 tasks"
		case 1:
			tasks = "1 task"
		}
		return fmt.Sprintf("1h: %s · %s in · %s out", tasks, rpc.ShortCount(w.TokensIn), rpc.ShortCount(w.TokensOut))
	}
	return ""
}

// usageBoxView draws the usage box for a pane width columns wide and
// height rows tall (see boxPane):
//
//	╭───────────────────────────────────────────────────────────────╮
//	│ Usage                                                         │
//	│                                                               │
//	│                   1h   today     week   month     30d     all │
//	│ sessions           1       2        5      12      14      30 │
//	│ …                                                             │
//	│                                                               │
//	│ Today, week and month follow the local calendar.              │
//	╰───────────────────────────────────────────────────────────────╯
func (m *Model) usageBoxView(width, height int) string {
	b := m.usageBox
	avail := boxRoom(width)

	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = strings.Split(ansi.Wrap("merud gave no usage numbers: "+b.err, avail, ""), "\n")
	case b.byModel && len(b.windows) == 0:
		body = []string{"No finished tasks yet."}
	case b.byModel:
		body = m.usageTableLines(rpc.ModelUsageTable(b.windows), avail)
	default:
		body = m.usageTableLines(rpc.UsageTable(b.windows), avail)
		body = append(body, m.usageDetailLines(b, avail)...)
	}
	title, note := "Usage", rpc.UsageNote
	if b.byModel {
		title, note = "Usage by model", rpc.ModelUsageNote
	}
	return m.boxPane(title, body, note, width, height)
}

// usageTableLines lines up the table's cells in columns: labels on the
// left, numbers on the right of each column, two spaces between columns.
// The window names on the first row are dim.
//
// When the columns don't fit in width, it drops windows from the right,
// all first, then 30d and so on, down to the first window. The windows run
// from the most recent to the longest, and the recent ones say most about
// how you use Meru now; `meru usage` prints every window at any width.
// The table by model drops its columns the same way, from CAPPED back.
func (m *Model) usageTableLines(rows [][]string, width int) []string {
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], ansi.StringWidth(cell))
		}
	}
	total := func(n int) int {
		t := widths[0]
		for _, w := range widths[1:n] {
			t += 2 + w
		}
		return t
	}
	for cols > 2 && total(cols) > width {
		cols--
	}

	lines := make([]string, len(rows))
	for r, row := range rows {
		// %-*s pads a cell on the right to the column's width, and %*s on
		// the left; the * takes the width from the argument before the cell.
		line := fmt.Sprintf("%-*s", widths[0], row[0])
		for i := 1; i < cols; i++ {
			line += fmt.Sprintf("  %*s", widths[i], row[i])
		}
		if r == 0 {
			line = m.style.dim.Render(line)
		}
		lines[r] = line
	}
	return lines
}

// usageDetailLines writes the dashboard under the windows table: tasks
// and tokens in per day as bar lines, today's tokens and time to first
// token, the slowest tasks this week, the tools whose calls failed, and
// where Prometheus can read the same numbers. The desktop app's Usage section shows the
// same data with charts.
func (m *Model) usageDetailLines(b *usageBox, width int) []string {
	switch {
	case b.detailErr != "":
		return append([]string{""}, strings.Split(ansi.Wrap("The charts need a newer merud: "+b.detailErr, width, ""), "\n")...)
	case b.detail == nil:
		return []string{"", m.style.dim.Render("asking merud for the charts…")}
	}
	d := b.detail
	lines := []string{""}

	days := d.Days
	if len(days) > usageDays {
		days = days[len(days)-usageDays:]
	}
	if len(days) > 0 {
		counts := make([]int64, len(days))
		tokens := make([]int64, len(days))
		var top, topTokens int64
		for i, day := range days {
			counts[i] = int64(day.Questions)
			tokens[i] = day.TokensIn
			top = max(top, counts[i])
			topTokens = max(topTokens, tokens[i])
		}
		span := m.style.dim.Render(dayLabel(days[0].Date) + " to " + dayLabel(days[len(days)-1].Date))
		today := days[len(days)-1]
		lines = append(lines,
			m.style.dim.Render(fmt.Sprintf("Tasks per day, last %d days (most %d)", len(days), top)),
			barLine(counts)+"  "+span,
			m.style.dim.Render(fmt.Sprintf("Tokens in per day (most %s)", rpc.ShortCount(topTokens))),
			barLine(tokens)+"  "+span,
			fmt.Sprintf("Tokens today: %s in · %s out", rpc.ShortCount(today.TokensIn), rpc.ShortCount(today.TokensOut)))
		if today.TTFTp50Millis > 0 {
			lines = append(lines, fmt.Sprintf("First token today: p50 %s · p95 %s", rpc.ShortMillis(today.TTFTp50Millis), rpc.ShortMillis(today.TTFTp95Millis)))
		}
	}

	if len(d.Slowest) > 0 {
		lines = append(lines, "", m.style.dim.Render("Slowest this week"))
		for _, q := range d.Slowest[:min(len(d.Slowest), usageSlowest)] {
			line := fmt.Sprintf("%7s  first token %s  %s  %s", rpc.ShortMillis(q.Millis), rpc.ShortMillis(q.TTFT), stampLabel(q.At), q.Model)
			lines = append(lines, ansi.Truncate(line, width, "…"))
		}
	}

	var failed []string
	for _, t := range d.Tools {
		var bad []string
		for outcome, n := range t.Outcomes {
			if outcome != "ok" {
				bad = append(bad, fmt.Sprintf("%s %d", outcome, n))
			}
		}
		if len(bad) == 0 {
			continue
		}
		sort.Strings(bad) // map order is random in Go; sort for a steady screen
		failed = append(failed, ansi.Truncate(fmt.Sprintf("%s  %d calls · %s", t.Tool, t.Calls, strings.Join(bad, " · ")), width, "…"))
	}
	if len(failed) > 0 {
		lines = append(lines, "", m.style.dim.Render("Tools with failed calls, last 30 days"))
		lines = append(lines, failed...)
	}

	lines = append(lines, "")
	if d.Metrics != "" {
		lines = append(lines, ansi.Truncate("Prometheus can read these at "+d.Metrics, width, "…"))
	} else {
		lines = append(lines, strings.Split(ansi.Wrap(`For Prometheus, set prometheus = "127.0.0.1:9464" under [observability] in config.toml.`, width, ""), "\n")...)
	}
	return lines
}

// barLine draws values as one character each, from ▁ to █, scaled to the
// largest; a zero shows as a space, so days with no questions stand out.
func barLine(values []int64) string {
	var top int64
	for _, v := range values {
		top = max(top, v)
	}
	var b strings.Builder
	for _, v := range values {
		if v == 0 || top == 0 {
			b.WriteRune(' ')
			continue
		}
		i := int((v*int64(len(bars)) - 1) / top)
		b.WriteRune(bars[min(i, len(bars)-1)])
	}
	return b.String()
}

// dayLabel writes "2026-10-09" as "9 Oct", or returns date as it came when
// it doesn't parse.
func dayLabel(date string) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return t.Format("2 Jan")
}

// stampLabel writes a UTC time from the store in local time, "3 Oct 10:12".
func stampLabel(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.Local().Format("2 Jan 15:04")
}
