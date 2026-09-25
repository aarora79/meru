// This file holds the slash commands the chat's input box understands. A
// line that starts with "/" runs here and never reaches the model.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// commandList names the slash commands the chat understands, for the line
// that answers an unknown one.
const commandList = "/new, /usage, /me, /mcp, /copy, /exit"

// command runs a line that starts with "/" instead of sending it as a
// question:
//
//   - /new starts a new session, so the next question carries none of the
//     conversation so far;
//   - /usage opens the usage box and asks merud for the numbers;
//   - /me opens a box with what Meru knows about the user, the memories
//     merud puts into every prompt;
//   - /mcp opens a box with each MCP server's state, the table `meru mcp`
//     prints;
//   - /copy N copies code block N to the clipboard, and /copy alone the
//     newest answer's last block, as Ctrl-Y does (copy.go);
//   - /exit quits the chat, the same as Ctrl-D: an answer still streaming
//     stops first. People type it out of habit from other chat programs,
//     and without it the line would go nowhere.
//
// Any other command leaves the text in the input, so the user can fix a
// typo, and shows one dim line with the commands the chat knows.
func (m Model) command(text string) (tea.Model, tea.Cmd) {
	// strings.Cut splits text at the first space: the command's name
	// before it, and its argument, if any, after it.
	name, arg, _ := strings.Cut(text, " ")
	switch name {
	case "/new":
		m.input.Reset()
		m.newSession()
		m.layout()
		return m, nil
	case "/usage":
		m.input.Reset()
		m.layout()
		m.usageBox = &usageBox{loading: true}
		m.input.Blur() // the input takes no text while the box is open
		return m, usageCmd(m.ask)
	case "/me":
		m.input.Reset()
		m.layout()
		m.meBox = &meBox{loading: true}
		m.input.Blur()
		return m, meCmd(m.ask)
	case "/mcp":
		m.input.Reset()
		m.layout()
		m.mcpBox = &mcpBox{loading: true}
		m.input.Blur()
		return m, mcpCmd(m.ask)
	case "/copy":
		return m.copyCommand(arg)
	case "/exit":
		m.stopTurn()
		return m, tea.Quit
	}
	m.notice = "unknown command " + name + " · commands: " + commandList
	return m, nil
}

// newSession clears the screen and forgets the session, so merud starts a
// new one with the next question. A conversation that went wrong, such as
// a model that keeps saying it knows nothing, otherwise carries on into
// every later answer, because each turn's prompt holds the ones before it.
// The old session stays on disk; only this screen lets go of it.
//
// A streaming answer stops first. Counting up m.turn makes the events it
// may still send belong to no turn, so a late "session" event can't bring
// the old session back.
func (m *Model) newSession() {
	m.stopTurn()
	m.turn++
	m.turns = nil
	m.blockCount = 0 // the code blocks went with the turns
	m.session = ""
	m.notice = "new session: the next question starts fresh"
	m.refresh()
}
