// This file holds the chat screen's state (Model) and the two methods Bubble
// Tea calls on it: Update, which turns a message into a new state, and View,
// which draws the state as text. Neither touches the terminal or the socket,
// so tests drive them with plain values.

package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// prompt starts the input line and marks each question in the transcript.
const prompt = "> "

// footerLines counts the rows below the transcript: one status line and one
// input line. The transcript gets the rest of the window.
const footerLines = 2

// dim draws the route line and the status line in the terminal's faint style.
// Lip Gloss comes in with Bubbles already; this is the only style we use.
var dim = lipgloss.NewStyle().Faint(true)

// entryKind says what a transcript line is, which decides how View draws it.
type entryKind int

// The kinds of transcript entry. iota counts up from zero inside a const
// block, so each name gets the next number without us writing it out.
const (
	entryQuestion entryKind = iota // what the user asked
	entryRoute                     // the route merud picked, drawn dim
	entryAnswer                    // the answer text, grown token by token
	entryError                     // an error, from merud or the connection
	entryNote                      // a note from the UI itself, like "(cancelled)"
)

// entry is one block in the transcript.
type entry struct {
	kind entryKind
	text string
}

// Model is everything the chat screen knows. Bubble Tea keeps one Model and
// replaces it with whatever Update returns.
type Model struct {
	ask  askFunc // sends a question to merud
	send sender  // puts stream events back into the program

	input      textinput.Model // the line the user types in
	transcript viewport.Model  // the scrolling pane above it
	spin       spinner.Model   // spins in the status line while an answer streams

	entries      []entry
	width        int    // terminal width, for wrapping
	session      string // from merud's "session" event; empty until the first reply
	lastQuestion string // what Up arrow puts back in the input

	// streaming is true from Enter until the turn ends or the user cancels.
	streaming bool
	// turn counts questions. Events carry the turn they belong to, so events
	// from a cancelled turn can't leak into the next one.
	turn int
	// cancel stops the current turn's request. Calling it closes the socket,
	// which tells merud to stop generating.
	cancel context.CancelFunc
}

// newModel builds the starting screen: an empty transcript and a focused
// input line, sized for an 80x24 terminal until the first resize message.
func newModel(ask askFunc, send sender) Model {
	in := textinput.New()
	in.Prompt = prompt
	in.Placeholder = "Ask Meru something"
	in.Focus()

	m := Model{
		ask:        ask,
		send:       send,
		input:      in,
		transcript: viewport.New(80, 24-footerLines),
		spin:       spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.resize(80, 24)
	return m
}

// Init returns the first command Bubble Tea runs: start the cursor blinking.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update turns one message into the next Model plus an optional command for
// Bubble Tea to run.
//
// Update has a value receiver, (m Model): it works on its own copy of the
// model and returns that copy. Bubble Tea keeps the returned one. Helpers
// below use pointer receivers, (m *Model), so they can change the copy in
// place before Update returns it.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A type switch picks a branch by the concrete type stored in msg, an
	// interface, and gives msg that type inside the branch.
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case eventMsg:
		m.handleEvent(msg)
		return m, nil
	case turnDoneMsg:
		m.handleDone(msg)
		return m, nil
	case spinner.TickMsg:
		// Returning no command lets the spinner stop ticking when idle.
		if !m.streaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	// Anything else, such as the cursor blink, belongs to the input line.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleKey reacts to a key press. Keys the chat screen doesn't claim go to
// the input line as typing.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlD:
		m.stopTurn()
		return m, tea.Quit
	case tea.KeyCtrlC:
		if !m.streaming {
			return m, tea.Quit
		}
		// While an answer streams, Ctrl-C cancels that turn and keeps the
		// chat open, the way Ctrl-C stops a command in a shell.
		m.stopTurn()
		m.add(entryNote, "(cancelled)")
		return m, nil
	case tea.KeyEnter:
		return m.submit()
	case tea.KeyUp:
		if m.lastQuestion != "" {
			m.input.SetValue(m.lastQuestion)
			m.input.CursorEnd()
		}
		return m, nil
	case tea.KeyPgUp:
		m.transcript.PageUp()
		return m, nil
	case tea.KeyPgDown:
		m.transcript.PageDown()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// submit sends the typed question, unless the line is blank or an answer is
// still streaming. It returns the command that runs the turn and the command
// that starts the spinner; tea.Batch runs both.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.streaming {
		return m, nil
	}
	m.input.Reset()
	m.lastQuestion = text
	m.turn++
	m.streaming = true

	// Each turn gets its own context so Ctrl-C can cancel it without ending
	// the chat. The context lives only in the command below; the model keeps
	// the cancel function, which is all it needs.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	m.add(entryQuestion, text)
	req := rpc.Request{
		Op:      rpc.OpAsk,
		Session: m.session, // empty on the first question, so merud starts a session
		Text:    text,
		Source:  rpc.SourceTUI,
	}
	return m, tea.Batch(streamCmd(ctx, m.ask, m.send, m.turn, req), m.spin.Tick)
}

// handleEvent applies one event from merud to the transcript. It ignores
// events from any turn but the one streaming now.
func (m *Model) handleEvent(msg eventMsg) {
	if !m.streaming || msg.turn != m.turn {
		return
	}
	ev := msg.ev
	switch ev.Type {
	case rpc.EventSession:
		m.session = ev.Session
	case rpc.EventRoute:
		m.add(entryRoute, fmt.Sprintf("route: %s (%.2f)", ev.Route, ev.Confidence))
	case rpc.EventToken:
		m.appendToken(ev.Text)
	case rpc.EventError:
		m.add(entryError, "error: "+ev.Error)
	}
	// EventDone needs nothing here; the turnDoneMsg right behind it ends the
	// turn. Unknown types are skipped, so a newer merud can't break an older
	// client.
}

// handleDone ends the current turn. A connection error shows inline. A turn
// the user already cancelled is ignored, because Ctrl-C ended it.
func (m *Model) handleDone(msg turnDoneMsg) {
	if !m.streaming || msg.turn != m.turn {
		return
	}
	m.stopTurn()
	if msg.err != nil {
		m.add(entryError, "error: "+msg.err.Error())
	}
}

// stopTurn cancels the running turn, if any, and marks the screen idle.
func (m *Model) stopTurn() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.streaming = false
}

// add appends a transcript entry and redraws the transcript.
func (m *Model) add(kind entryKind, text string) {
	m.entries = append(m.entries, entry{kind: kind, text: text})
	m.refresh()
}

// appendToken grows the answer being streamed. The first token of a turn
// starts a new answer entry; later tokens extend it.
func (m *Model) appendToken(text string) {
	last := len(m.entries) - 1
	if last >= 0 && m.entries[last].kind == entryAnswer {
		m.entries[last].text += text
		m.refresh()
		return
	}
	m.add(entryAnswer, text)
}

// resize fits the transcript and input line to a new window size and
// rewraps the transcript for the new width.
func (m *Model) resize(width, height int) {
	m.width = width
	m.transcript.Width = width
	m.transcript.Height = max(height-footerLines, 1)
	m.input.Width = max(width-len(prompt)-1, 1)
	m.refresh()
}

// refresh redraws the transcript text. If the user was already at the
// bottom, it follows the new text down; if they scrolled up to read, it
// leaves them where they are.
func (m *Model) refresh() {
	follow := m.transcript.AtBottom()
	m.transcript.SetContent(m.render())
	if follow {
		m.transcript.GotoBottom()
	}
}

// render draws every transcript entry as text wrapped to the window width,
// with a blank line before each question after the first.
func (m *Model) render() string {
	var b strings.Builder
	for i, e := range m.entries {
		if e.kind == entryQuestion && i > 0 {
			b.WriteString("\n")
		}
		text := e.text
		if e.kind == entryQuestion {
			text = prompt + text
		}
		// ansi.Wrap breaks at spaces where it can, and mid-word where a word
		// is wider than the window.
		text = ansi.Wrap(text, m.width, "")
		if e.kind == entryRoute {
			text = dim.Render(text)
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// View draws the whole screen: the transcript, a status line, and the input
// line. Bubble Tea calls it after every Update and repaints what changed.
func (m Model) View() string {
	status := "enter: send · up: last question · pgup/pgdn: scroll · ctrl-d: quit"
	if m.streaming {
		status = m.spin.View() + " answering · ctrl-c: cancel"
	}
	status = dim.Render(ansi.Truncate(status, m.width, "…"))
	return m.transcript.View() + "\n" + status + "\n" + m.input.View()
}
