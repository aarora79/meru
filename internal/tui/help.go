// This file holds the /help box, which lists every key and every slash
// command. The help line at the bottom has room for five entries, so it
// ends with "/help commands" and this box holds the rest.

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// helpNote closes the /help box.
const helpNote = "Esc or q closes a box. Commands run at once, even while an answer streams."

// scrollBox is an open box that only scrolls, /help or /about. at is the
// line the box keeps in view, which ↑ and ↓ move, and lines how many lines
// the box held when last drawn.
type scrollBox struct {
	at    int
	lines int
}

// helpBoxView draws the /help box for a pane width columns wide and height
// rows tall (see boxPaneAt): the keys from FullHelp, then each command
// from commandHelp, each in two columns.
func (m *Model) helpBoxView(width, height int) string {
	var keys, what []string
	for _, b := range m.keys.FullHelp()[0] {
		keys = append(keys, b.Help().Key)
		what = append(what, b.Help().Desc)
	}
	body := []string{m.style.dim.Render("Keys")}
	body = append(body, twoColumns(keys, what, boxRoom(width))...)
	body = append(body, "", m.style.dim.Render("Commands"))
	var uses, whats []string
	for _, c := range commandHelp {
		uses = append(uses, c.use)
		whats = append(whats, c.what)
	}
	body = append(body, twoColumns(uses, whats, boxRoom(width))...)
	m.helpBox.lines = len(body)
	return m.boxPaneAt("Keys and commands", body, m.helpBox.at, helpNote, width, height)
}

// twoColumns lays out pairs as lines of two columns, the first padded to
// its widest entry and two spaces before the second. When the two don't
// fit in width, the second goes on its own line under the first, indented
// four spaces.
func twoColumns(first, second []string, width int) []string {
	w := 0
	for _, s := range first {
		w = max(w, ansi.StringWidth(s))
	}
	var out []string
	for i, s := range first {
		if w+2+ansi.StringWidth(second[i]) <= width {
			// %-*s pads s on the right to w columns; the * takes w.
			out = append(out, fmt.Sprintf("%-*s  %s", w, s, second[i]))
			continue
		}
		out = append(out, s)
		for _, l := range strings.Split(ansi.Wrap(second[i], max(width-4, 1), ""), "\n") {
			out = append(out, "    "+l)
		}
	}
	return out
}
