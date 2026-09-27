// This file holds /save, the desktop app's "Save to a note" and "Share as
// file". merud writes the Markdown with the write_file tool, through
// dispatch (rpc.OpSaveFile), so it asks first as write_file does; the
// question comes back here as an approval, which opens in the
// conversation's place because a save belongs to no turn.

package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// savedMsg reports how a save ended: n is its number, path the file merud
// wrote, and err why it wrote none.
type savedMsg struct {
	n    int
	path string
	err  error
}

// saveCommand runs /save. Alone, it saves the newest finished answer as a
// note under notes/; "chat" saves the whole session under chats/. It
// waits while a turn runs, so only one approval box can be open at a time,
// and while another save runs.
func (m Model) saveCommand(arg string) (tea.Model, tea.Cmd) {
	req := rpc.Request{Op: rpc.OpSaveFile, Session: m.session, Source: rpc.SourceTUI}
	what := "note"
	switch arg {
	case "":
		req.Kind = rpc.SaveNote
		for i := len(m.turns) - 1; i >= 0; i-- {
			if m.turns[i].state == stateDone && m.turns[i].answer != "" {
				req.Text = m.turns[i].answer
				break
			}
		}
	case "chat":
		req.Kind, what = rpc.SaveChat, "chat"
	default:
		m.notice = "/save takes nothing, for the newest answer, or chat"
		return m, nil
	}
	// From here the command is right, so it leaves the input, whether or
	// not the save can start now.
	m.input.Reset()
	m.layout()
	switch {
	case m.streaming:
		m.notice = "wait for the answer to finish, or press ctrl+c, before you save"
		return m, nil
	case m.saving != 0:
		m.notice = "a save is still running"
		return m, nil
	case m.session == "" || (req.Kind == rpc.SaveNote && req.Text == ""):
		m.notice = "ask something first: there is nothing to save yet"
		return m, nil
	}
	m.saves++
	m.saving = m.saves
	m.notice = "saving the " + what + "…"
	return m, saveCmd(m.ask, m.send, m.saving, req)
}

// saveCmd returns a command that sends req, a save, to merud. merud asks
// before write_file runs; approveVia hands that question to Update as it
// does a turn's, marked as a save's. The command returns a savedMsg with
// the path merud wrote, or why it wrote none. It waits as long as the user
// takes to answer.
func saveCmd(ask askFunc, send sender, n int, req rpc.Request) tea.Cmd {
	return func() tea.Msg {
		msg := savedMsg{n: n}
		for ev, err := range ask(context.Background(), req, approveVia(send, n, true)) {
			if err != nil {
				msg.err = err
				return msg
			}
			switch ev.Type {
			case rpc.EventSaved:
				msg.path = ev.Text
			case rpc.EventError:
				msg.err = errors.New(ev.Error)
				return msg
			}
		}
		if msg.path == "" {
			msg.err = errors.New("merud saved nothing")
		}
		return msg
	}
}

// applySaved ends save msg.n and says on the notice line where the file
// went, as a path with ~ for the home folder, or why it didn't.
func (m *Model) applySaved(msg savedMsg) {
	if msg.n != m.saving {
		return
	}
	m.saving = 0
	if m.approval != nil && m.approval.save {
		m.closeApproval(rpc.ChoiceDeny)
	}
	if msg.err != nil {
		m.notice = msg.err.Error()
		return
	}
	m.notice = "saved " + rpc.ShortPath(m.home, msg.path)
}

// savePaneView draws a save's approval box in the conversation's place,
// under a blank line, padded to height rows.
func (m *Model) savePaneView(width, height int) string {
	lines := append([]string{""}, strings.Split(m.approvalBoxView(max(width-answerIndent, 10)), "\n")...)
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
