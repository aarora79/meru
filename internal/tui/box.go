// This file holds what the boxes that open over the conversation share:
// the /usage box, the /me box and the /mcp box. Each takes the conversation's place at
// the same size, holds the keys until Esc or q closes it, and draws a
// title, a body and a dim note inside a teal border.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// boxOpen reports whether the /usage, /me or /mcp box is open.
func (m *Model) boxOpen() bool {
	return m.usageBox != nil || m.meBox != nil || m.mcpBox != nil
}

// closeBox closes whichever box is open and gives the input its cursor
// back.
func (m *Model) closeBox() {
	m.usageBox = nil
	m.meBox = nil
	m.mcpBox = nil
	m.input.Focus()
}

// boxKey handles one key while a box is open: Esc or q closes it, and
// every other key does nothing, so typing can't leak into the input behind
// it. Update handles Ctrl-C and Ctrl-D before this, as it does for the
// approval box.
func (m *Model) boxKey(msg tea.KeyMsg) {
	if msg.Type == tea.KeyEsc || (msg.Type == tea.KeyRunes && string(msg.Runes) == "q") {
		m.closeBox()
	}
}

// boxRoom returns how many columns of text fit inside a box in a pane
// width columns wide. The margin, border and padding take answerIndent
// plus four columns.
func boxRoom(width int) int {
	return max(width-answerIndent-4, 1)
}

// boxPane draws a box for a pane width columns wide and height rows tall,
// and pads it to that height so the screen keeps its shape. The box holds
// title, a blank line, the body lines, a blank line and note, dim.
//
// The box is as wide as its widest line, up to what the pane allows. Body
// lines wider than that are cut with "…", so a caller that wants them
// whole wraps them to boxRoom(width) first. The note may be wider than the
// body; it gets the width the pane allows, and wraps inside it.
func (m *Model) boxPane(title string, body []string, note string, width, height int) string {
	avail := boxRoom(width)
	inner := len(title)
	for _, l := range body {
		inner = max(inner, ansi.StringWidth(l))
	}
	inner = min(max(inner, ansi.StringWidth(note)), avail)

	lines := []string{m.style.brand.Render(title), ""}
	for _, l := range body {
		lines = append(lines, ansi.Truncate(l, inner, "…"))
	}
	lines = append(lines, "", m.style.dim.Render(ansi.Wrap(note, inner, "")))
	box := m.style.box.Width(inner + 2).Render(strings.Join(lines, "\n"))

	// A blank line above the box, as above the first turn, then blank
	// lines to fill the pane. A pane too short for the box shows its top.
	out := append([]string{""}, strings.Split(box, "\n")...)
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out[:height], "\n")
}
