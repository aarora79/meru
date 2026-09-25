// This file opens a link the user clicks in the chat. With [chat]
// mouse_copy on, the chat captures the mouse, so the terminal never sees
// the click and can't open the link itself. The chat finds the link under
// the pointer and hands the URL to the platform's program for opening
// files and URLs, which starts the browser.

package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// openFunc opens url in the program the system picks for it, such as the
// browser. The model holds one, so tests can pass a fake that opens
// nothing.
type openFunc func(url string) error

// openTimeout bounds how long the opener may run. open, xdg-open and
// rundll32 hand the URL to the browser and return; one stuck on a missing
// display shouldn't wait for ever.
const openTimeout = 10 * time.Second

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
// open, sets a notice that says why and returns no command. Opening starts
// a program, so it runs as a command, off the loop that draws the screen,
// and reports back with an openedMsg.
func (m Model) openLink(u string) (tea.Model, tea.Cmd) {
	if err := checkOpenable(u); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	open := m.open
	return m, func() tea.Msg {
		return openedMsg{url: u, err: open(u)}
	}
}

// checkOpenable returns an error when u isn't a URL the chat opens: it
// must be http, https or file, with no space or control character in it
// (linkable in links.go), and it must not start with "-". The opener gets
// the URL as an argument, and a program reads an argument that starts with
// "-" as an option, so such a URL could change what the program does.
func checkOpenable(u string) error {
	if strings.HasPrefix(u, "-") || !linkable(u) {
		return fmt.Errorf("won't open %s: only http, https and file links open", shortURL(u, noticeURLWidth))
	}
	return nil
}

// systemOpen opens url with the program openCommand picks for this
// machine. It fails when the program can't start, exits with an error or
// runs past openTimeout.
func systemOpen(url string) error {
	argv := openCommand(runtime.GOOS, url)
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	// defer runs cancel when this function returns, which frees the timer.
	defer cancel()
	// exec starts the program directly, with no shell, and passes url as
	// one argument, so nothing in it can run as a command. checkOpenable
	// has already refused a URL that starts with "-".
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- a fixed program per platform, no shell
	// Standard output and error stay unset, so they go to the null device,
	// and Run doesn't wait on a browser that the opener leaves running.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", programName(argv[0]), err)
	}
	return nil
}

// openCommand returns the program, with its arguments, that opens url on
// goos: open on macOS, rundll32 with url.dll's FileProtocolHandler on
// Windows, and xdg-open on Linux and the rest. Each hands the URL to the
// default program for it, as a double-click would.
func openCommand(goos, url string) []string {
	switch goos {
	case "darwin":
		return []string{"open", url}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}
	}
	return []string{"xdg-open", url}
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
