// This file holds the /used box, the desktop app's "What this answer used"
// panel for the newest answer: every file its prompt held, each tool call,
// the memories recall brought in, with d to forget one, and a line that
// says who the tool calls reached.

package tui

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// usedBox is the open /used box. turn is the index in m.turns of the turn
// it shows, at the marked memory, and confirm the ID of the memory a first
// d asked to forget.
type usedBox struct {
	turn    int
	at      int
	confirm string
}

// usedCommand runs /used: it opens the box for the newest turn, finished
// or not. A turn still running shows what it used so far.
func (m Model) usedCommand() (tea.Model, tea.Cmd) {
	if len(m.turns) == 0 {
		m.notice = "ask something first: no answer has used anything yet"
		return m, nil
	}
	m.openBox()
	m.usedBox = &usedBox{turn: len(m.turns) - 1}
	return m, nil
}

// usedKey handles a key in the /used box: ↑ and ↓ move the marker over the
// memories, and d forgets the marked one after a second d.
func (m *Model) usedKey(msg tea.KeyMsg) tea.Cmd {
	b := m.usedBox
	mems := m.turns[b.turn].memories
	return m.forgetKey(msg, &b.at, &b.confirm, mems)
}

// forgetKey is the /used and /me boxes' key handling over a list of
// memories: ↑ and ↓ move the marker at, and d forgets the marked memory
// after a second d. confirm holds the ID a first d named; any other key
// clears it. It returns the command that asks merud to forget, or nil.
func (m *Model) forgetKey(msg tea.KeyMsg, at *int, confirm *string, mems []rpc.MemoryInfo) tea.Cmd {
	*at = moveMark(msg, *at, len(mems))
	if !isKey(msg, "d") || len(mems) == 0 || m.forgetting != "" {
		*confirm = ""
		return nil
	}
	id := mems[*at].ID
	if *confirm != id {
		*confirm = id
		return nil
	}
	*confirm = ""
	m.forgetting = id
	return requestCmd(m.ask, tagForget, rpc.Request{Op: rpc.OpMemoryForget, ID: id}, rpc.EventDone, changeTimeout)
}

// usedBoxView draws the /used box for a pane width columns wide and height
// rows tall (see boxPaneAt).
func (m *Model) usedBoxView(width, height int) string {
	b := m.usedBox
	t := &m.turns[b.turn]
	room := boxRoom(width)
	dim := m.style.dim.Render

	body := []string{dim("Sources")}
	if len(t.sources) == 0 {
		body = append(body, "No files from your folders.")
	}
	for _, c := range t.sources {
		body = append(body, splitWrap(c.String(), room)...)
	}
	body = append(body, "", dim("Tools"))
	if len(t.tools) == 0 {
		body = append(body, "No tools ran.")
	}
	for _, tc := range t.tools {
		body = append(body, toolText(tc))
	}
	body = append(body, "", dim("Remembered"))
	focus := -1
	switch {
	case len(t.memories) == 0 && t.past:
		// The transcript keeps no record of what recall brought in.
		body = append(body, "Past chats don't record what was remembered.")
	case len(t.memories) == 0:
		body = append(body, "Nothing Meru remembered came up.")
	}
	for i, mem := range t.memories {
		if i == b.at {
			focus = len(body)
		}
		body = append(body, m.markRow(i == b.at, strings.Join(strings.Fields(mem.Text), " ")))
	}
	note := privacyLine(t)
	if b.confirm != "" {
		note = "Press d again to forget the marked memory; any other key keeps it."
	}
	return m.boxPaneAt("What this answer used", body, focus, note, width, height)
}

// privacyLine says where the turn's work happened, as the app's panel
// does: "The model ran on this computer. Only google was contacted."
func privacyLine(t *exchange) string {
	start := "The model ran on this computer."
	if t.state == stateActive {
		return start
	}
	names := contacted(t.tools)
	switch len(names) {
	case 0:
		return start + " Nothing else was contacted."
	case 1:
		return start + " Only " + names[0] + " was contacted."
	}
	return start + " Only " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " were contacted."
}

// contacted lists who a turn's tool calls reached, in the order of first
// use: each MCP server and A2A agent by name, "web search" for web_search
// and the site for web_fetch. A call that never ran (denied or declined)
// reached nobody. Built-in tools that read the user's files, and local
// commands, reach no one else, so they add nothing. The desktop app's
// Bridge makes the same list from its own record of the calls.
func contacted(tools []toolCall) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, tc := range tools {
		if tc.outcome == "denied" || tc.outcome == "declined" {
			continue
		}
		switch {
		case tc.kind == "mcp":
			server, _, _ := strings.Cut(tc.name, ".")
			add(server)
		case tc.kind == "a2a":
			// merud names an agent's skill "a2a.<agent>.<skill>".
			agent, _, _ := strings.Cut(strings.TrimPrefix(tc.name, "a2a."), ".")
			add(agent)
		case tc.name == "web_search":
			add("web search")
		case tc.name == "web_fetch":
			add(tc.host)
		}
	}
	return out
}

// urlHost returns the host in a call's "url" argument, such as
// "example.com", or "" when there is none.
func urlHost(args json.RawMessage) string {
	var a struct {
		URL string `json:"url"` // a struct tag: the JSON key this field reads
	}
	if json.Unmarshal(args, &a) != nil || a.URL == "" {
		return ""
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// applyMemoryChange takes in merud's reply to a forget or an add. A
// forgotten memory leaves every turn's list and the /me box; an added one
// joins the /me box. The notice says what happened.
func (m *Model) applyMemoryChange(msg replyMsg) tea.Cmd {
	if msg.tag == tagMemoryAdd {
		if msg.err != nil {
			m.notice = "not saved: " + msg.err.Error()
			return nil
		}
		m.notice = "saved: Meru puts it in every answer from now on"
		if m.meBox != nil {
			m.meBox.loading = true // the list comes again, with the new memory
			return meCmd(m.ask)
		}
		return nil
	}
	id := m.forgetting
	m.forgetting = ""
	if msg.err != nil {
		m.notice = "couldn't forget it: " + msg.err.Error()
		return nil
	}
	text := ""
	drop := func(mems []rpc.MemoryInfo) []rpc.MemoryInfo {
		// slices.DeleteFunc removes each memory the function picks.
		return slices.DeleteFunc(mems, func(x rpc.MemoryInfo) bool {
			if x.ID == id {
				text = x.Text
				return true
			}
			return false
		})
	}
	for i := range m.turns {
		m.turns[i].memories = drop(m.turns[i].memories)
	}
	if b := m.meBox; b != nil {
		b.memories = drop(b.memories)
		b.at = min(b.at, max(len(b.memories)-1, 0))
	}
	if b := m.usedBox; b != nil {
		b.at = min(b.at, max(len(m.turns[b.turn].memories)-1, 0))
	}
	m.notice = "forgot: " + strings.Join(strings.Fields(text), " ")
	return nil
}
