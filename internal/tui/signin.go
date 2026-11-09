// This file holds /signin, which signs in to an MCP server with auth =
// "oauth", as the desktop app's Sign in button does, and the line a turn
// shows when merud says such a server waits for a sign-in
// (rpc.EventSignInNeeded). merud starts the sign-in (rpc.OpMCPSignIn) and
// waits for the browser; this file opens the sign-in page and asks
// mcp_status every two seconds until the server connects.

package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

const (
	tagSignIn      = "sign_in"       // /signin <name>
	tagSignInCheck = "sign_in_check" // the checks while the browser is open
	// signInPoll is how often the chat asks whether the server connected,
	// and signInWait how long it keeps asking; merud gives up on a
	// sign-in after ten minutes too.
	signInPoll = 2 * time.Second
	signInWait = 10 * time.Minute
)

// signInTickMsg asks the chat to check the sign-in under way again.
type signInTickMsg struct{}

// signingIn is the sign-in under way: the server's name, and when the
// chat stops asking.
type signingIn struct {
	name  string
	until time.Time
}

// signInLine is the line a turn shows under "Meru" when merud said tool
// servers wait for a sign-in: merud's sentence, and the command for each.
func signInLine(ev rpc.Event) string {
	line := ev.Text
	for _, s := range ev.Servers {
		line += " Type /signin " + s.Name + "."
	}
	return line
}

// signInCommand runs /signin <name>: it asks merud to start the sign-in,
// and applySignIn opens the page merud gives back. Alone, /signin signs in
// to the one server the newest turn named, if it named one.
func (m Model) signInCommand(arg string) (tea.Model, tea.Cmd) {
	name := arg
	if name == "" && len(m.turns) > 0 && len(m.turns[len(m.turns)-1].signInServers) == 1 {
		name = m.turns[len(m.turns)-1].signInServers[0]
	}
	if name == "" {
		m.notice = "/signin takes a server's name, such as /signin github"
		return m, nil
	}
	m.input.Reset()
	m.notice = "starting the sign-in to " + name + "…"
	m.signingIn = &signingIn{name: name, until: time.Now().Add(signInWait)}
	return m, requestCmd(m.ask, tagSignIn, rpc.Request{Op: rpc.OpMCPSignIn, ID: name}, rpc.EventSignIn, changeTimeout)
}

// applySignIn takes in merud's reply to /signin or to a check while the
// browser is open. The first opens the sign-in page and starts the checks;
// each check ends the sign-in once the server connects, or once it again
// waits for a sign-in with no page open, as after the user said no in the
// browser, and otherwise books the next check.
func (m *Model) applySignIn(msg replyMsg) tea.Cmd {
	s := m.signingIn
	if s == nil {
		return nil
	}
	if msg.err != nil {
		if msg.tag == tagSignInCheck && time.Now().Before(s.until) {
			return signInTick() // merud may be restarting; ask again
		}
		m.signingIn = nil
		m.notice = "sign-in failed: " + msg.err.Error()
		return nil
	}
	if msg.tag == tagSignIn {
		name := s.name
		m.notice = "Sign in to " + name + " in your browser; Meru waits. The page: " + msg.ev.Text
		if err := m.open(msg.ev.Text); err == nil {
			m.notice = "Opened " + name + "'s sign-in page in your browser. Meru waits for you to sign in…"
		}
		return signInTick()
	}
	for _, row := range msg.ev.MCP {
		if row.Name != s.name {
			continue
		}
		switch {
		case row.State == rpc.MCPConnected:
			m.signingIn = nil
			m.notice = "Signed in to " + s.name + ". Ask again to use its tools."
			return nil
		case row.State == rpc.MCPSignIn && row.Link == "":
			m.signingIn = nil
			m.notice = "The sign-in to " + s.name + " didn't finish. Type /signin " + s.name + " to try again."
			return nil
		}
	}
	if time.Now().After(s.until) {
		m.signingIn = nil
		m.notice = "The sign-in to " + s.name + " took too long. Type /signin " + s.name + " to try again."
		return nil
	}
	return signInTick()
}

// signInTick books the next check in signInPoll. tea.Tick runs the timer
// in Bubble Tea's own goroutine and sends the message when it fires.
func signInTick() tea.Cmd {
	return tea.Tick(signInPoll, func(time.Time) tea.Msg { return signInTickMsg{} })
}

// signInCheck asks merud for the MCP status rows, for the sign-in under
// way.
func (m Model) signInCheck() tea.Cmd {
	return requestCmd(m.ask, tagSignInCheck, rpc.Request{Op: rpc.OpMCPStatus}, rpc.EventMCPStatus, readTimeout)
}
