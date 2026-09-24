// This file holds the chat screen's look: the brand colours, the Lip Gloss
// styles built from them, and the key map that both drives the keys and fills
// the help line at the bottom.

package tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// styles holds every Lip Gloss style the screen uses.
//
// A lipgloss.Style is a value that describes how to draw text: colour, bold,
// borders, padding. style.Render(s) returns s wrapped in the terminal escape
// codes for that look. Styles come from a *lipgloss.Renderer, which knows how
// many colours the terminal supports. With NO_COLOR set, or when output isn't
// a terminal, the renderer's colour profile is plain ASCII and Render adds no
// escape codes at all. The tests build their styles from an ASCII renderer
// for the same reason: their golden files hold plain text.
type styles struct {
	brand      lipgloss.Style // "Meru मेरु" in the header
	dim        lipgloss.Style // hints, stats, the header details
	rule       lipgloss.Style // the line under the header
	online     lipgloss.Style // "● connected"
	offline    lipgloss.Style // "● merud not running"
	you        lipgloss.Style // the "You" label
	meru       lipgloss.Style // the "Meru" label
	question   lipgloss.Style // the question text, with a bar on its left
	badge      lipgloss.Style // the route badge, normal case
	badgeAmber lipgloss.Style // the route badge when the router fell back
	raw        lipgloss.Style // a streaming answer, indented like Glamour's output
	cursor     lipgloss.Style // the ▍ at the end of a streaming answer
	spinner    lipgloss.Style // the "thinking" spinner
	errorBox   lipgloss.Style // an error, in a red rounded box
	inputBox   lipgloss.Style // the rounded border round the input
}

// answerIndent is how far message text sits from the left edge. Glamour's
// built-in styles put a two-column margin round every document, so the
// question, the streaming text, the stats line and the error box use the same
// indent, and nothing jumps sideways when the finished answer is re-rendered.
const answerIndent = 2

// newStyles builds the styles from r. Run passes the renderer for the real
// terminal; tests pass one fixed to plain ASCII.
//
// The screen uses a few colours. An AdaptiveColor holds one value for
// terminals with a light background and one for dark ones, and Lip Gloss asks
// the terminal which it has. Hex values need a terminal with 24-bit colour;
// on older terminals Lip Gloss picks the nearest colour the terminal has.
func newStyles(r *lipgloss.Renderer) styles {
	// teal is Meru's brand colour: the name in the header, the input
	// border and the streaming cursor.
	teal := lipgloss.AdaptiveColor{Light: "#0E7C7B", Dark: "#5FC4BD"}
	// blue marks what the user wrote: the "You" label and the bar beside
	// the question.
	blue := lipgloss.AdaptiveColor{Light: "#3558C2", Dark: "#8AB4F8"}
	// amber marks a route the router fell back to because it wasn't sure.
	amber := lipgloss.AdaptiveColor{Light: "#B26B00", Dark: "#E5A445"}
	// green colours the "Meru" label and the connection status; red
	// colours the error box.
	green := lipgloss.AdaptiveColor{Light: "#2E7D32", Dark: "#7BC67E"}
	red := lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#EF6C6C"}
	// grey draws lines and hints that should stay in the background.
	grey := lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}

	return styles{
		brand:      r.NewStyle().Foreground(teal).Bold(true),
		dim:        r.NewStyle().Foreground(grey),
		rule:       r.NewStyle().Foreground(grey),
		online:     r.NewStyle().Foreground(green),
		offline:    r.NewStyle().Foreground(red),
		you:        r.NewStyle().Foreground(blue).Bold(true),
		meru:       r.NewStyle().Foreground(green).Bold(true),
		badge:      r.NewStyle().Foreground(grey),
		badgeAmber: r.NewStyle().Foreground(amber),
		// A border on the left side only draws a thin bar next to the
		// question. The four booleans are top, right, bottom and left.
		question: r.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(blue).
			PaddingLeft(1).
			MarginLeft(answerIndent),
		raw:     r.NewStyle().PaddingLeft(answerIndent),
		cursor:  r.NewStyle().Foreground(teal),
		spinner: r.NewStyle().Foreground(teal),
		errorBox: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(red).
			Foreground(red).
			Padding(0, 1).
			MarginLeft(answerIndent),
		inputBox: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(teal).
			Padding(0, 1),
	}
}

// keyMap lists the chat screen's keys. Each key.Binding pairs the keys that
// trigger an action with the text the help line shows for it, so the help
// line can't drift from what the keys do.
type keyMap struct {
	Send    key.Binding
	Stop    key.Binding
	Quit    key.Binding
	Recall  key.Binding
	Scroll  key.Binding
	Newline key.Binding
}

// newKeyMap returns the chat screen's keys.
func newKeyMap() keyMap {
	return keyMap{
		Send:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Stop:    key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "stop/quit")),
		Quit:    key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "quit")),
		Recall:  key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "last question")),
		Scroll:  key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll")),
		Newline: key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline")),
	}
}

// ShortHelp returns the keys the help line shows, in order. Having this
// method makes keyMap satisfy help.KeyMap, the interface the Bubbles help
// component draws from. Ctrl-J is left out: the line would no longer fit an
// 80-column terminal, and the help component cuts what doesn't fit.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Send, k.Stop, k.Quit, k.Recall, k.Scroll}
}

// FullHelp returns every key as one column. The help component asks for it
// only in its expanded mode, which the chat screen never turns on.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Send, k.Newline, k.Stop, k.Quit, k.Recall, k.Scroll}}
}
