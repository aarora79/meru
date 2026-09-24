// This file holds the slash commands the chat's input box understands. A
// line that starts with "/" runs here and never reaches the model.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// commandList names the slash commands the chat understands, for the line
// that answers an unknown one.
const commandList = "/new, /usage"

// command runs a line that starts with "/" instead of sending it as a
// question:
//
//   - /new starts a new session, so the next question carries none of the
//     conversation so far;
//   - /usage opens the usage box and asks merud for the numbers.
//
// Any other command leaves the text in the input, so the user can fix a
// typo, and shows one dim line with the commands the chat knows.
func (m Model) command(text string) (tea.Model, tea.Cmd) {
	name, _, _ := strings.Cut(text, " ")
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
	m.session = ""
	m.notice = "new session: the next question starts fresh"
	m.refresh()
}
