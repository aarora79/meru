// This file holds the /chats box, the desktop app's list of past chats:
// merud lists the sessions from their transcripts (rpc.OpSessions), and
// Enter reopens one (rpc.OpSessionTurns). The reopened chat's next question
// carries its session ID, so the conversation goes on where it stopped.

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// chatsLimit caps how many past chats the box lists, the newest first.
// A hundred cover weeks of use; the words after /chats find an older one.
const chatsLimit = 100

// chatsNote closes the /chats box.
const chatsNote = "The next question continues the chat you open. /chats <words> lists the chats whose first question holds them."

// chatsBox is the open /chats box. It opens at once with loading set, and
// merud's reply fills in rows or err. words filters the list, at is the
// marked row, and opening names the session whose turns Enter asked for.
type chatsBox struct {
	loading bool
	words   []string
	rows    []rpc.SessionInfo
	at      int
	err     string
	opening string
}

// chatsCommand runs /chats: it opens the box and asks merud for the past
// chats. The words after it, if any, keep only the chats whose title holds
// every one of them, without regard to case.
func (m Model) chatsCommand(arg string) (tea.Model, tea.Cmd) {
	m.openBox()
	m.chatsBox = &chatsBox{loading: true, words: strings.Fields(strings.ToLower(arg))}
	return m, requestCmd(m.ask, tagSessions, rpc.Request{Op: rpc.OpSessions, Limit: chatsLimit}, rpc.EventSessions, readTimeout)
}

// chatsKey handles a key in the /chats box: ↑ and ↓ move the marker, and
// Enter asks merud for the marked chat's turns. A chat can't open while a
// turn runs, since the answer would land in the wrong conversation.
func (m *Model) chatsKey(msg tea.KeyMsg) tea.Cmd {
	b := m.chatsBox
	b.at = moveMark(msg, b.at, len(b.rows))
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

// applyChats takes in merud's reply to the list or to one chat's turns. A
// reply that comes after the box closed changes nothing.
func (m *Model) applyChats(msg replyMsg) {
	b := m.chatsBox
	if b == nil {
		return
	}
	if msg.tag == tagSessions {
		b.loading = false
		if msg.err != nil {
			b.err = msg.err.Error()
			return
		}
		for _, s := range msg.ev.Sessions {
			if hasWords(strings.ToLower(s.Title), b.words) {
				b.rows = append(b.rows, s)
			}
		}
		return
	}
	id := b.opening
	b.opening = ""
	if msg.err != nil {
		m.notice = "couldn't open that chat: " + msg.err.Error()
		return
	}
	if m.streaming {
		return // a question went out while the turns were on their way
	}
	m.reopen(id, msg.ev.Turns)
	m.closeBox()
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
		row := fmt.Sprintf("%s  %s  %s", when, s.Title, m.style.dim.Render(fmt.Sprintf("(%d)", s.Turns)))
		if s.ID == b.opening {
			row += m.style.dim.Render("  opening…")
		}
		body = append(body, m.markRow(i == b.at, row))
	}
	title := "Past chats"
	if len(b.words) > 0 {
		title += ": " + strings.Join(b.words, " ")
	}
	return m.boxPaneAt(title, body, b.at, chatsNote, width, height)
}
