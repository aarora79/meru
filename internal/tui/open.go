// This file opens a link the user clicks in the chat. With [chat]
// mouse_copy on, the chat captures the mouse, so the terminal never sees
// the click and can't open the link itself. The chat finds the link under
// the pointer and hands the URL to internal/opener, which starts the
// platform's program for opening files and URLs, and so the browser.

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/opener"
)

// openFunc opens url in the program the system picks for it, such as the
// browser. The model holds opener.Open, so tests can pass a fake that
// opens nothing.
type openFunc func(url string) error

// noticeURLWidth is how many columns a URL may take in the notice line.
const noticeURLWidth = 50

// openedMsg reports how opening a link went, for the notice line.
type openedMsg struct {
	url string
	err error
}

// notice writes msg as the dim line under the input: "opened
// example.com/notes/garden-plan", or the error.
func (msg openedMsg) notice() string {
	short := shortURL(msg.url, noticeURLWidth)
	if msg.err != nil {
		return fmt.Sprintf("couldn't open %s: %v", short, msg.err)
	}
	return "opened " + short
}

// openLink returns a command that opens u, or, for a URL the chat won't
// open, sets a notice that says why and returns no command. opener.Check
// decides which URLs open: http, https and file, and none that starts
// with "-". Opening starts a program, so it runs as a command, off the loop
// that draws the screen, and reports back with an openedMsg.
func (m Model) openLink(u string) (tea.Model, tea.Cmd) {
	if err := opener.Check(u); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	open := m.open
	return m, func() tea.Msg {
		return openedMsg{url: u, err: open(u)}
	}
}

// linkUnder returns the URL of the link at column x and row y of the
// screen, or "" when no link sits there. It finds the row as labelUnder
// does.
func (m Model) linkUnder(x, y int) string {
	row := y - headerLines
	lines := strings.Split(m.conversation.View(), "\n")
	if row < 0 || row >= len(lines) {
		return ""
	}
	return linkAt(lines[row], x)
}

// osc8Start is how every OSC 8 escape code begins: ESC ] 8 ;.
const osc8Start = "\x1b]8;"

// linkAt returns the URL of the OSC 8 link whose text covers column col of
// line, or "" when none does. A link on screen is
//
//	ESC ] 8 ; params ; URL ST  text  ESC ] 8 ; ; ST
//
// where ST, the string terminator, is ESC \ or BEL. linkAt walks line from
// code to code and counts the columns of the text between them with
// ansi.StringWidth, which skips colour codes and counts a wide rune, such
// as a CJK character, as two columns, as the terminal draws it.
func linkAt(line string, col int) string {
	at := 0    // columns drawn so far
	start := 0 // the column where the open link's text starts
	open := "" // the URL of the link the text is in, or ""
	rest := line
	for {
		i := strings.Index(rest, osc8Start)
		if i < 0 {
			// The text runs to the end of the line. A link left open
			// there still counts up to the last column.
			at += ansi.StringWidth(rest)
			if open != "" && col >= start && col < at {
				return open
			}
			return ""
		}
		at += ansi.StringWidth(rest[:i])
		// A new code ends the link before it, whether it closes that link
		// or opens another.
		if open != "" && col >= start && col < at {
			return open
		}
		u, n := parseOSC8(rest[i:])
		if n == 0 {
			// A code with no terminator: nothing after it is a link.
			return ""
		}
		open, start = u, at
		rest = rest[i+n:]
	}
}

// parseOSC8 reads the OSC 8 code at the start of s. It returns the URL,
// "" for the code that closes a link, and how many bytes the code takes,
// or 0 when s has no string terminator.
func parseOSC8(s string) (url string, n int) {
	body := s[len(osc8Start):]
	end, stLen := -1, 0
	if i := strings.Index(body, "\x1b\\"); i >= 0 {
		end, stLen = i, 2
	}
	if i := strings.IndexByte(body, '\a'); i >= 0 && (end < 0 || i < end) {
		end, stLen = i, 1
	}
	if end < 0 {
		return "", 0
	}
	// The body is params ; URL. The params, such as id=3, hold no ";".
	_, u, _ := strings.Cut(body[:end], ";")
	return u, len(osc8Start) + end + stLen
}
