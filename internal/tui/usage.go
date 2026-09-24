// This file holds what the chat screen shows of merud's usage numbers: the
// /usage box that opens over the conversation, and the last hour's summary
// for the header.

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// usageBox is the open /usage box. It opens at once with loading set, and
// the next usage reply fills in windows or err.
type usageBox struct {
	loading bool
	windows []rpc.UsageWindow
	err     string // why merud gave no numbers
}

// closeUsage closes the usage box and gives the input its cursor back.
func (m *Model) closeUsage() {
	m.usageBox = nil
	m.input.Focus()
}

// usageKey handles one key while the usage box is open: Esc or q closes
// it, and every other key does nothing, so typing can't leak into the
// input behind it. Update handles Ctrl-C and Ctrl-D before this, as it
// does for the approval box.
func (m *Model) usageKey(msg tea.KeyMsg) {
	if msg.Type == tea.KeyEsc || (msg.Type == tea.KeyRunes && string(msg.Runes) == "q") {
		m.closeUsage()
	}
}

// applyUsage takes in one usage reply. The header keeps the windows for its
// last-hour summary. When merud answered with an error, which is what an
// older merud without OpUsage does, the header drops the summary. When
// merud couldn't be reached at all, the header keeps the last numbers, as
// it keeps the document count; the status check alone decides whether
// merud is up.
//
// An open box that still waits takes the first reply, whichever request it
// answers: all of them ask for the same numbers.
func (m *Model) applyUsage(msg usageMsg) {
	switch {
	case msg.err == nil:
		m.usage = msg.windows
	case msg.answered:
		m.usage = nil
	}
	if b := m.usageBox; b != nil && b.loading {
		b.loading = false
		b.windows = msg.windows
		if msg.err != nil {
			b.err = msg.err.Error()
		}
	}
}

// lastHour writes the header's usage summary from the 1h window, such as
// "1h: 4 questions · 18k in · 2.1k out". It returns "" when merud sent no
// such window.
func lastHour(windows []rpc.UsageWindow) string {
	for _, w := range windows {
		if w.Name != rpc.Usage1h {
			continue
		}
		questions := fmt.Sprintf("%d questions", w.Turns)
		switch w.Turns {
		case 0:
			// Token counts of zero add nothing.
			return "1h: 0 questions"
		case 1:
			questions = "1 question"
		}
		return fmt.Sprintf("1h: %s · %s in · %s out", questions, rpc.ShortCount(w.TokensIn), rpc.ShortCount(w.TokensOut))
	}
	return ""
}

// usageBoxView draws the usage box for a pane width columns wide and
// height rows tall, and pads it to that height so the screen keeps its
// shape:
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
	// The margin, border and padding take answerIndent plus four columns.
	avail := max(width-answerIndent-4, 1)

	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = strings.Split(ansi.Wrap("merud gave no usage numbers: "+b.err, avail, ""), "\n")
	default:
		body = m.usageTableLines(rpc.UsageTable(b.windows), avail)
	}
	inner := len("Usage")
	for _, l := range body {
		inner = max(inner, ansi.StringWidth(l))
	}
	// The note may be wider than the table; it gets the width the pane
	// allows, and wraps inside it.
	inner = min(max(inner, ansi.StringWidth(rpc.UsageNote)), avail)

	lines := []string{m.style.brand.Render("Usage"), ""}
	for _, l := range body {
		lines = append(lines, ansi.Truncate(l, inner, "…"))
	}
	lines = append(lines, "", m.style.dim.Render(ansi.Wrap(rpc.UsageNote, inner, "")))
	box := m.style.usageBox.Width(inner + 2).Render(strings.Join(lines, "\n"))

	// A blank line above the box, as above the first turn, then blank
	// lines to fill the pane. A pane too short for the box shows its top.
	out := append([]string{""}, strings.Split(box, "\n")...)
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out[:height], "\n")
}

// usageTableLines lines up the table's cells in columns: labels on the
// left, numbers on the right of each column, two spaces between columns.
// The window names on the first row are dim.
//
// When the columns don't fit in width, it drops windows from the right,
// all first, then 30d and so on, down to the first window. The windows run
// from the most recent to the longest, and the recent ones say most about
// how you use Meru now; `meru usage` prints every window at any width.
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
