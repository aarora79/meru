// This file holds the few styles meru's plain-text output uses: dim for tool
// lines and hints, bold for names, and green, red and amber for status. A
// style adds colour only when its writer is a terminal that supports it and
// NO_COLOR is unset, so a pipe or a file gets plain text.

package main

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// look holds the styles for one writer, such as stderr.
//
// A lipgloss.Renderer made for a writer checks that writer when a style
// first renders: whether it is a terminal, how many colours it has, and
// whether NO_COLOR is set. Without colour, Render returns its text with no
// escape codes, so tests that write into a bytes.Buffer see plain text.
type look struct {
	dim   lipgloss.Style
	bold  lipgloss.Style
	good  lipgloss.Style // green: connected, ok
	bad   lipgloss.Style // red: not connected, failed
	amber lipgloss.Style // warnings, and tools that ask first
	// links is true when the writer is a terminal with styling on, so a
	// source line can be a clickable link (rpc.Hyperlink). A pipe, a file
	// or NO_COLOR gets plain text.
	links bool
}

// newLook builds the styles for w. The colours are the terminal's own
// numbered colours (2 green, 1 red, 3 yellow), which every colour terminal
// has, so meru never has to ask the terminal about its background.
func newLook(w io.Writer) look {
	r := lipgloss.NewRenderer(w)
	return look{
		links: r.ColorProfile() != termenv.Ascii,
		dim:   r.NewStyle().Faint(true),
		bold:  r.NewStyle().Bold(true),
		good:  r.NewStyle().Foreground(lipgloss.Color("2")),
		bad:   r.NewStyle().Foreground(lipgloss.Color("1")),
		amber: r.NewStyle().Foreground(lipgloss.Color("3")),
	}
}
