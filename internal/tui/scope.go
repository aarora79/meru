// This file holds /scope, which sets where the next questions may look,
// as the desktop app's "Where Meru looks" switch does, and /retry, the
// app's Try again. See ARCHITECTURE.md, "Desktop app", for what each scope
// lets a turn touch.

package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// scopeLabel names scope s as the header shows it, in the app's words:
// "my files", "mail and calendar", "web" or "just talk". Auto, the scope
// every question has unless the user picks another, gives "", so the
// header says nothing.
func scopeLabel(s string) string {
	switch s {
	case rpc.ScopeFiles:
		return "my files"
	case rpc.ScopeMail:
		return "mail and calendar"
	case rpc.ScopeWeb:
		return "web"
	case rpc.ScopeTalk:
		return "just talk"
	}
	return ""
}

// scopeCommand runs /scope. With a scope's name it makes that the scope of
// the questions sent from now on, while questions queued already keep
// theirs. Alone, it says which scope is set and which ones exist. merud checks the
// scope again, so an unknown one never reaches it.
func (m Model) scopeCommand(arg string) (tea.Model, tea.Cmd) {
	names := strings.Join(rpc.Scopes(), ", ")
	if arg == "" {
		now := rpc.ScopeAuto
		if m.scope != "" {
			now = m.scope
		}
		m.notice = "scope: " + now + " · /scope takes one of " + names
		return m, nil
	}
	if !slices.Contains(rpc.Scopes(), arg) {
		m.notice = "/scope takes one of " + names
		return m, nil
	}
	m.input.Reset()
	m.layout()
	m.scope = arg
	if arg == rpc.ScopeAuto {
		m.scope = "" // the request leaves auto out, as the app's does
		m.notice = "scope: auto · Meru decides where to look"
		return m, nil
	}
	m.notice = "scope: " + scopeLabel(arg) + " · the next questions look only there"
	return m, nil
}

// retryCommand runs /retry: it asks the newest question again, in the same
// session, with the scope and the images it had the first time, as the
// app's Try again does. While a turn runs, the question waits in the
// queue like any other.
// continueWords is what the chat shows for /continue, as merud's
// transcript and the desktop app record it.
const continueWords = "Continue"

// stoppedShort reports whether t's answer stopped at the length limit or
// the turn's time limit, which /continue can pick up.
func stoppedShort(t *exchange) bool {
	return t.state == stateDone && (t.stats.Outcome == "cut_off" || t.stats.Outcome == "timeout")
}

// continueCommand runs /continue: merud picks up the chat's newest answer
// where it stopped short, with the question and the tool calls it already
// made, so nothing is fetched twice. Only the newest answer can continue,
// and only once it has ended.
func (m Model) continueCommand() (tea.Model, tea.Cmd) {
	m.input.Reset()
	if m.streaming {
		m.notice = "wait for this answer to end, then /continue"
		return m, nil
	}
	if len(m.turns) == 0 || m.session == "" || !stoppedShort(&m.turns[len(m.turns)-1]) {
		m.notice = "nothing to continue: /continue picks up the newest answer when it stopped short"
		return m, nil
	}
	t := m.turns[len(m.turns)-1]
	return m, m.sendOrQueue(outgoing{text: continueWords, scope: t.scope, cont: true})
}

func (m Model) retryCommand() (tea.Model, tea.Cmd) {
	if len(m.turns) == 0 {
		m.input.Reset()
		m.notice = "nothing to ask again yet"
		return m, nil
	}
	if m.streaming && len(m.queue) >= maxQueue {
		m.notice = "queue full: wait for the next answer to start"
		return m, nil
	}
	t := m.turns[len(m.turns)-1]
	m.input.Reset()
	return m, m.sendOrQueue(outgoing{text: t.question, scope: t.scope, images: t.images})
}
