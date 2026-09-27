// This file holds the three ways to copy a code block from an answer:
// /copy N, /copy or Ctrl-Y for the newest answer's last block, and, with
// [chat] mouse_copy on, a click on a block's "⧉ copy N" label. Its mouse
// handler also sends a click on a link to open.go.

package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// copiedMsg reports how a copy went, for the notice line.
type copiedMsg struct {
	n     int    // the block's number, or 0 for a whole answer
	lines int    // how many lines it has
	via   string // how the text reached the clipboard, such as "pbcopy"
	err   error
}

// notice writes msg as the dim line under the input: "copied block 3
// (2 lines)", with a note when it went through OSC 52, or the error.
func (msg copiedMsg) notice() string {
	if msg.err != nil {
		if msg.n == 0 {
			return fmt.Sprintf("couldn't copy the answer: %v", msg.err)
		}
		return fmt.Sprintf("couldn't copy block %d: %v", msg.n, msg.err)
	}
	what := fmt.Sprintf("block %d", msg.n)
	if msg.n == 0 {
		what = "the answer"
	}
	s := fmt.Sprintf("copied %s (%d %s)", what, msg.lines, plural(msg.lines, "line", "lines"))
	if msg.via == "OSC 52" {
		s += " through the terminal (OSC 52): no clipboard program found"
	}
	return s
}

// plural returns one when n is 1 and many otherwise.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// numberBlocks finds the code blocks in the answer of t, a turn that has
// just ended, and numbers them on from the session's blocks before. It
// does nothing for a turn it has numbered already. The cached drawing of
// the answer goes, so the next draw adds the labels.
func (m *Model) numberBlocks(t *exchange) {
	if t.firstBlock != 0 {
		return
	}
	t.code = findCodeBlocks(t.answer)
	t.firstBlock = m.blockCount + 1
	m.blockCount += len(t.code)
	t.rendered = ""
}

// block returns the text of code block n, and false when no block has
// that number.
func (m *Model) block(n int) (string, bool) {
	for _, t := range m.turns {
		if i := n - t.firstBlock; t.firstBlock != 0 && i >= 0 && i < len(t.code) {
			return t.code[i].text, true
		}
	}
	return "", false
}

// copyCommand runs /copy: with a number, it copies that block; alone, the
// newest answer's last block, as Ctrl-Y does; with "answer", the newest
// answer's whole text.
func (m Model) copyCommand(arg string) (tea.Model, tea.Cmd) {
	m.input.Reset()
	m.layout()
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return m.copyLast()
	}
	if arg == "answer" {
		return m.copyAnswer()
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		m.notice = "/copy takes a block number, such as /copy 2, or answer"
		return m, nil
	}
	return m.copyBlock(n)
}

// copyBlock copies block n, or says why it can't.
func (m Model) copyBlock(n int) (tea.Model, tea.Cmd) {
	text, ok := m.block(n)
	switch {
	case ok:
		return m, copyCmd(m.copy, n, text)
	case m.blockCount == 0:
		m.notice = "no code block to copy yet"
	default:
		m.notice = fmt.Sprintf("no code block %d · blocks go from 1 to %d", n, m.blockCount)
	}
	return m, nil
}

// copyLast copies the last code block of the newest finished answer. An
// answer still streaming has no numbered blocks yet, so it is skipped.
func (m Model) copyLast() (tea.Model, tea.Cmd) {
	for i := len(m.turns) - 1; i >= 0; i-- {
		t := &m.turns[i]
		if t.firstBlock == 0 {
			continue
		}
		if len(t.code) == 0 {
			m.notice = "the newest answer has no code block"
			if m.blockCount > 0 {
				m.notice += " · /copy N copies an older one"
			}
			return m, nil
		}
		return m.copyBlock(t.firstBlock + len(t.code) - 1)
	}
	m.notice = "no code block to copy yet"
	return m, nil
}

// copyAnswer copies the newest finished answer's whole text, as the
// model wrote it, Markdown and all: the desktop app's Copy under an
// answer.
func (m Model) copyAnswer() (tea.Model, tea.Cmd) {
	for i := len(m.turns) - 1; i >= 0; i-- {
		if t := m.turns[i]; t.state == stateDone && t.answer != "" {
			return m, copyCmd(m.copy, 0, t.answer)
		}
	}
	m.notice = "no answer to copy yet"
	return m, nil
}

// copyCmd returns a command that puts text, block n, on the clipboard; an
// n of 0 means the whole answer.
// Copying starts a program, so it runs as a command, off the loop that
// draws the screen, and reports back with a copiedMsg.
func copyCmd(copyText copyFunc, n int, text string) tea.Cmd {
	return func() tea.Msg {
		via, err := copyText(text)
		return copiedMsg{n: n, lines: strings.Count(text, "\n") + 1, via: via, err: err}
	}
}

// handleMouse reacts to the mouse, which the chat only captures with
// [chat] mouse_copy on. A left click on a "⧉ copy N" label copies block N,
// a left click on a link opens it (open.go), and the wheel scrolls the
// conversation, as it would without capture. The chat has to open links
// itself: with the mouse captured, the terminal never sees the click.
// While a box is open the mouse does nothing.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.info.MouseCopy || m.approval != nil || m.boxOpen() {
		return m, nil
	}
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		m.conversation.ScrollUp(3)
	case msg.Button == tea.MouseButtonWheelDown:
		m.conversation.ScrollDown(3)
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		if n := m.labelUnder(msg.X, msg.Y); n > 0 {
			return m.copyBlock(n)
		}
		if u := m.linkUnder(msg.X, msg.Y); u != "" {
			return m.openLink(u)
		}
	}
	return m, nil
}

// labelUnder returns the number of the block whose label sits at column x
// and row y of the screen, or 0 when none does. The conversation starts
// under the header, and what it shows is exactly its View, so the row
// picks one line of that.
func (m Model) labelUnder(x, y int) int {
	row := y - headerLines
	lines := strings.Split(m.conversation.View(), "\n")
	if row < 0 || row >= len(lines) {
		return 0
	}
	return labelAt(lines[row], x)
}
