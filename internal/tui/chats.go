// This file holds the /chats box, the desktop app's list of past chats:
// merud lists the sessions from their transcripts (rpc.OpSessions), and
// Enter reopens one (rpc.OpSessionTurns). The reopened chat's next question
// carries its session ID, so the conversation goes on where it stopped.
// Each row shows the chat's folder and tags, and d, twice, deletes the
// marked chat (rpc.OpSessionDelete).

package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// chatsLimit caps how many past chats the box lists, the newest first.
// A hundred cover weeks of use; the words after /chats find an older one.
const chatsLimit = 100

// chatsNote closes the /chats box.
const chatsNote = "The next question continues the chat you open. /chats <words> lists the chats whose first question, folder or tags hold them. d twice deletes a chat for good."

// chatsBox is the open /chats box. It opens at once with loading set, and
// merud's reply fills in rows or err. words filters the list, at is the
// marked row, and opening names the session whose turns Enter asked for.
// armed is the ID of the chat a first d marked for deleting, and
// deleting the one a second d asked merud to delete.
type chatsBox struct {
	loading  bool
	words    []string
	rows     []rpc.SessionInfo
	at       int
	err      string
	opening  string
	armed    string
	deleting string
}

// chatsCommand runs /chats: it opens the box and asks merud for the past
// chats. The words after it, if any, keep only the chats whose title holds
// every one of them, without regard to case.
func (m Model) chatsCommand(arg string) (tea.Model, tea.Cmd) {
	m.openBox()
	m.chatsBox = &chatsBox{loading: true, words: strings.Fields(strings.ToLower(arg))}
	return m, requestCmd(m.ask, tagSessions, rpc.Request{Op: rpc.OpSessions, Limit: chatsLimit}, rpc.EventSessions, readTimeout)
}

// chatsKey handles a key in the /chats box: ↑ and ↓ move the marker,
// Enter asks merud for the marked chat's turns, and d, pressed twice on
// the same row, deletes the marked chat. A chat can't open or be deleted
// while a turn runs, since the answer would land in the wrong place.
func (m *Model) chatsKey(msg tea.KeyMsg) tea.Cmd {
	b := m.chatsBox
	b.at = moveMark(msg, b.at, len(b.rows))
	if isKey(msg, "d") {
		return m.chatsDelete()
	}
	b.armed = "" // any other key disarms a d
	if msg.Type != tea.KeyEnter || len(b.rows) == 0 || b.opening != "" {
		return nil
	}
	if m.streaming {
		m.notice = "wait for the answer to finish, or press ctrl+c, before you open another chat"
		return nil
	}
	b.opening = b.rows[b.at].ID
	req := rpc.Request{Op: rpc.OpSessionTurns, Session: b.opening}
	return requestCmd(m.ask, tagTurns, req, rpc.EventTurns, readTimeout)
}

// chatsDelete handles d in the /chats box: the first arms the marked
// chat, and a second d on the same chat asks merud to delete it.
func (m *Model) chatsDelete() tea.Cmd {
	b := m.chatsBox
	if len(b.rows) == 0 || b.deleting != "" {
		return nil
	}
	if m.streaming {
		m.notice = "wait for the answer to finish, or press ctrl+c, before you delete a chat"
		return nil
	}
	id := b.rows[b.at].ID
	if b.armed != id {
		b.armed = id
		m.notice = "d again deletes this chat for good"
		return nil
	}
	b.armed, b.deleting = "", id
	req := rpc.Request{Op: rpc.OpSessionDelete, Session: id}
	return requestCmd(m.ask, tagBoxDelete, req, rpc.EventDone, changeTimeout)
}

// applyBoxDelete takes the chat merud deleted off the /chats box. When it
// was the chat on screen, the screen starts a new one, as after /delete.
func (m *Model) applyBoxDelete() {
	b := m.chatsBox
	if b == nil {
		return
	}
	id := b.deleting
	b.deleting = ""
	// slices.DeleteFunc drops every row the function returns true for.
	b.rows = slices.DeleteFunc(b.rows, func(s rpc.SessionInfo) bool { return s.ID == id })
	b.at = max(min(b.at, len(b.rows)-1), 0)
	if id == m.session {
		m.newSession()
	}
	m.notice = "deleted the chat for good"
}

// applyChats takes in merud's reply to the list or to one chat's turns. A
// reply that comes after the box closed changes nothing. It returns the
// command that tells merud to forget an incognito chat the screen leaves
// for the one it reopens, or nil.
func (m *Model) applyChats(msg replyMsg) tea.Cmd {
	b := m.chatsBox
	if b == nil {
		return nil
	}
	if msg.tag == tagSessions {
		b.loading = false
		if msg.err != nil {
			b.err = msg.err.Error()
			return nil
		}
		for _, s := range msg.ev.Sessions {
			// The words find a chat by its title, its folder or its tags.
			text := strings.ToLower(s.Title + " " + s.Folder + " " + strings.Join(s.Tags, " "))
			if hasWords(text, b.words) {
				b.rows = append(b.rows, s)
			}
		}
		return nil
	}
	id := b.opening
	b.opening = ""
	if msg.err != nil {
		m.notice = "couldn't open that chat: " + msg.err.Error()
		return nil
	}
	if m.streaming {
		return nil // a question went out while the turns were on their way
	}
	leave := m.leaveCmd()
	m.reopen(id, msg.ev.Turns)
	m.closeBox()
	return leave
}

// hasWords reports whether s holds every word in words.
func hasWords(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// reopen puts session id's past turns on the screen in place of the
// conversation, as finished turns, and makes id the session the next
// question continues. The queue and the attachments stay: they belong to
// the next question, not to the chat on screen.
//
// A past turn keeps what the transcript keeps: the question and its
// images, the answer, the route, the notice, the files its prompt held and
// its tool calls, and the time and token count. The transcript holds no
// time to the first token and no confidence, so the badge and the stats
// line leave those out.
func (m *Model) reopen(id string, turns []rpc.TurnInfo) {
	m.turn++ // late events from an earlier turn belong to no turn now
	m.turns = nil
	m.blockCount = 0
	m.session = id
	m.incognito = false // only a saved chat reopens
	for _, t := range turns {
		e := exchange{question: t.Question, images: t.Images, answer: t.Answer, route: t.Route, notice: t.Notice, state: stateDone, past: true}
		for i, p := range t.Sources {
			e.sources = append(e.sources, rpc.Citation{N: i + 1, Path: p})
		}
		for _, s := range t.Tools {
			e.tools = append(e.tools, toolCall{name: s.Name, kind: s.Kind, host: urlHost(s.Args), outcome: s.Outcome, millis: s.DurationMillis})
		}
		e.stats = rpc.Event{Type: rpc.EventDone, DurationMillis: t.DurationMillis, TokensOut: t.TokensOut}
		if t.Answer == "" {
			e.state, e.err = stateFailed, "no answer"
			if t.Outcome != "" {
				e.err += " (" + t.Outcome + ")"
			}
		}
		m.turns = append(m.turns, e)
	}
	for i := range m.turns {
		m.numberBlocks(&m.turns[i])
	}
	m.notice = fmt.Sprintf("reopened %s (%d %s) · the next question continues it",
		shortSession(id), len(turns), plural(len(turns), "question", "questions"))
	m.conversation.GotoBottom()
	m.refresh()
}

// chatsBoxView draws the /chats box for a pane width columns wide and
// height rows tall (see boxPaneAt): one row per chat, the marked one with
// "›", each with when it last changed, in the time zone merud wrote it
// in, its first question and how many questions it holds.
func (m *Model) chatsBoxView(width, height int) string {
	b := m.chatsBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no chats: "+b.err, boxRoom(width))
	case len(b.rows) == 0 && len(b.words) > 0:
		body = []string{"No chat's first question holds " + strings.Join(b.words, " ") + "."}
	case len(b.rows) == 0:
		body = []string{"No past chats yet."}
	}
	for i, s := range b.rows {
		when := s.Updated
		// time.Parse reads the RFC 3339 time merud sent; Format writes it
		// in the same zone, so the list reads the same on any machine.
		if t, err := time.Parse(time.RFC3339, s.Updated); err == nil {
			when = t.Format("2006-01-02 15:04")
		}
		row := fmt.Sprintf("%s  %s  %s", when, chatLabel(s), m.style.dim.Render(fmt.Sprintf("(%d)", s.Turns)))
		switch s.ID {
		case b.opening:
			row += m.style.dim.Render("  opening…")
		case b.deleting:
			row += m.style.dim.Render("  deleting…")
		case b.armed:
			row += m.style.notice.Render("  d again deletes it")
		}
		body = append(body, m.markRow(i == b.at, row))
	}
	title := "Past chats"
	if len(b.words) > 0 {
		title += ": " + strings.Join(b.words, " ")
	}
	return m.boxPaneAt(title, body, b.at, chatsNote, width, height)
}
