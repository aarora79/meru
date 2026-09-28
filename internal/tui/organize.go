// This file holds the commands that organize chats, as the desktop app's
// right-click menu does: /incognito, /delete, /folder, /move, /tag and
// /untag. Each sends one op to merud, which changes the files; the chat
// only says how it went on the notice line. ARCHITECTURE.md, "Organizing
// past chats", describes the ops.

package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// The requests this file sends, for applyReply.
const (
	tagDelete      = "session_delete" // /delete
	tagBoxDelete   = "chat_delete"    // d d in the /chats box
	tagLeave       = "leave"          // forgetting an incognito chat the user left
	tagChatMeta    = "session_meta"   // /move, /tag and /untag
	tagChatFolders = "chat_folders"   // /folder
)

// folderUse is the notice for a /folder the chat can't read.
const folderUse = "/folder new <name> · /folder rename <old> -> <new> · /folder delete <name>"

// incognitoCommand runs /incognito: a new chat, like /new, that merud
// keeps in memory only. The first question carries the flag; merud names
// the chat in its "session" event, and the next questions continue it.
func (m Model) incognitoCommand() (tea.Model, tea.Cmd) {
	m.input.Reset()
	leave := m.leaveCmd()
	m.newSession()
	m.incognito = true
	m.notice = "incognito chat: Meru keeps no record of it · /new ends it"
	m.layout()
	return m, leave
}

// leaveCmd returns the command that tells merud to forget the incognito
// chat on screen, before the screen lets go of it, or nil when the chat
// isn't one or has no question yet. merud would forget it an hour later
// anyway; telling it now leaves nothing in memory either.
func (m *Model) leaveCmd() tea.Cmd {
	if !m.incognito || m.session == "" || m.ask == nil {
		return nil
	}
	req := rpc.Request{Op: rpc.OpSessionDelete, Session: m.session}
	return requestCmd(m.ask, tagLeave, req, rpc.EventDone, readTimeout)
}

// forgetIncognito tells merud to forget the incognito chat on screen, and
// waits up to readTimeout for it. Run calls it as the chat quits, when no
// command can run any more.
func (m Model) forgetIncognito() {
	if !m.incognito || m.session == "" || m.ask == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	for range m.ask(ctx, rpc.Request{Op: rpc.OpSessionDelete, Session: m.session}, nil) {
		// range drains the reply; an error changes nothing at quit.
	}
}

// deleteCommand runs /delete. The first one arms it and says what a
// second one does; the second, with nothing typed between, deletes the
// chat for good, and the screen starts a new one. A chat with no question
// yet has nothing to delete.
func (m Model) deleteCommand() (tea.Model, tea.Cmd) {
	m.input.Reset()
	switch {
	case m.session == "":
		m.notice = "nothing to delete: this chat has no questions yet"
		return m, nil
	case m.streaming:
		m.notice = "wait for the answer to finish, or press ctrl+c, before you delete the chat"
		return m, nil
	case m.deleteArmed != m.session:
		m.deleteArmed = m.session
		m.notice = "/delete again deletes this chat for good: its transcript and what search holds of it"
		return m, nil
	}
	m.deleteArmed = ""
	req := rpc.Request{Op: rpc.OpSessionDelete, Session: m.session}
	return m, requestCmd(m.ask, tagDelete, req, rpc.EventDone, changeTimeout)
}

// folderCommand runs /folder: alone it lists the chat folders; "new
// <name>" adds one, "rename <old> -> <new>" renames one with its chats,
// and "delete <name>" takes one away and moves its chats back to the main
// list.
func (m Model) folderCommand(arg string) (tea.Model, tea.Cmd) {
	verb, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	var req rpc.Request
	switch {
	case verb == "":
		req = rpc.Request{Op: rpc.OpChatFolders}
	case verb == "new" && rest != "":
		req = rpc.Request{Op: rpc.OpChatFolderAdd, ID: rest}
	case verb == "delete" && rest != "":
		req = rpc.Request{Op: rpc.OpChatFolderRemove, ID: rest}
	case verb == "rename":
		from, to, ok := strings.Cut(rest, "->")
		if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			m.notice = folderUse
			return m, nil
		}
		req = rpc.Request{Op: rpc.OpChatFolderRename, ID: strings.TrimSpace(from), Text: strings.TrimSpace(to)}
	default:
		m.notice = folderUse
		return m, nil
	}
	m.input.Reset()
	return m, requestCmd(m.ask, tagChatFolders, req, rpc.EventChatFolders, changeTimeout)
}

// moveCommand runs /move: it puts the chat on screen in the folder the
// words name, or, alone, takes it out of its folder.
func (m Model) moveCommand(arg string) (tea.Model, tea.Cmd) {
	m.input.Reset()
	if msg := m.cantOrganize(); msg != "" {
		m.notice = msg
		return m, nil
	}
	req := rpc.Request{Op: rpc.OpSessionMove, Session: m.session, Text: arg}
	return m, requestCmd(m.ask, tagChatMeta, req, rpc.EventSessions, changeTimeout)
}

// tagCommand runs /tag, or /untag when remove is true: it adds or takes
// out the tags the words name, one word each, on the chat on screen.
func (m Model) tagCommand(arg string, remove bool) (tea.Model, tea.Cmd) {
	if msg := m.cantOrganize(); msg != "" {
		m.input.Reset()
		m.notice = msg
		return m, nil
	}
	tags := strings.Fields(arg)
	if len(tags) == 0 {
		m.notice = "name a tag or more, such as /tag garden bulbs"
		return m, nil
	}
	change := &rpc.TagChange{Add: tags}
	if remove {
		change = &rpc.TagChange{Remove: tags}
	}
	m.input.Reset()
	req := rpc.Request{Op: rpc.OpSessionTag, Session: m.session, Tags: change}
	return m, requestCmd(m.ask, tagChatMeta, req, rpc.EventSessions, changeTimeout)
}

// cantOrganize says why the chat on screen can't take a folder or tags,
// or returns "" when it can: it needs a question first, and an incognito
// chat keeps neither.
func (m *Model) cantOrganize() string {
	switch {
	case m.incognito:
		return "an incognito chat has no folder or tags"
	case m.session == "":
		return "ask something first: this chat isn't saved yet"
	}
	return ""
}

// applyOrganize takes in merud's reply to one of this file's requests.
func (m *Model) applyOrganize(msg replyMsg) {
	if msg.err != nil {
		if msg.tag != tagLeave {
			m.notice = "merud said no: " + msg.err.Error()
		}
		return
	}
	switch msg.tag {
	case tagDelete:
		m.newSession()
		m.notice = "deleted the chat for good · the next question starts a new one"
	case tagBoxDelete:
		m.applyBoxDelete()
	case tagChatMeta:
		if len(msg.ev.Sessions) == 1 {
			m.notice = metaNotice(msg.ev.Sessions[0])
		}
	case tagChatFolders:
		if len(msg.ev.ChatFolders) == 0 {
			m.notice = "no chat folders · /folder new <name> makes one"
			return
		}
		m.notice = "chat folders: " + strings.Join(msg.ev.ChatFolders, ", ")
	}
}

// metaNotice says where a chat sits and how it is tagged, after /move,
// /tag or /untag.
func metaNotice(s rpc.SessionInfo) string {
	where := "in no folder"
	if s.Folder != "" {
		where = "in " + s.Folder
	}
	tags := "no tags"
	if len(s.Tags) > 0 {
		tags = "tags: " + strings.Join(s.Tags, ", ")
	}
	return "this chat is " + where + " · " + tags
}

// chatLabel returns what a /chats row shows after the time: the folder in
// brackets, the title, and each tag with a "#".
func chatLabel(s rpc.SessionInfo) string {
	label := s.Title
	if s.Folder != "" {
		label = "[" + s.Folder + "] " + label
	}
	for _, t := range s.Tags {
		label += " #" + t
	}
	return label
}
