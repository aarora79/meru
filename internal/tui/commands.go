// This file holds the slash commands the chat's input box understands. A
// line that starts with "/" runs here and never reaches the model.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// commandList names the slash commands the chat understands, for the line
// that answers an unknown one.
const commandList = "/new, /usage, /me, /mcp, /model, /copy, /exit"

// command runs a line that starts with "/" instead of sending it as a
// question:
//
//   - /new starts a new session, so the next question carries none of the
//     conversation so far. While a turn runs, it stops that turn and drops
//     the queued questions, which belonged to the old conversation;
//   - /usage opens the usage box and asks merud for the numbers, and
//     /usage by model opens it with a row per answer model;
//   - /me opens a box with what Meru knows about the user, the memories
//     merud puts into every prompt;
//   - /mcp opens a box with each MCP server's state, the table `meru mcp`
//     prints;
//   - /model opens a box with the model sets, the table `meru model`
//     prints; /model <name> switches to a set and /model save makes the
//     set in use the default (models.go);
//   - /copy N copies code block N to the clipboard, and /copy alone the
//     newest answer's last block, as Ctrl-Y does (copy.go);
//   - /exit quits the chat, the same as Ctrl-D: an answer still streaming
//     stops first, and the queued questions go unasked. People type it out
//     of habit from other chat programs, and without it the line would go
//     nowhere.
//
// The other commands only open a box, copy text or talk to merud, so they
// run at once, even while a turn runs; none of them waits in the queue.
// The one exception is a model switch, which waits for the turn to end
// (see modelCommand).
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
		return m.usageCommand(arg)
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
	case "/model":
		return m.modelCommand(arg)
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
// the old session back. Queued questions go too: the user typed them for
// the old conversation, and sending them into the new one would carry it
// on. The notice says how many went.
func (m *Model) newSession() {
	m.stopTurn()
	m.turn++
	m.turns = nil
	m.blockCount = 0 // the code blocks went with the turns
	m.session = ""
	m.notice = "new session: the next question starts fresh"
	if dropped := m.dropQueue(); dropped != "" {
		m.notice += " · " + dropped
	}
	m.refresh()
}
