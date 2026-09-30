// This file holds what the boxes that open over the conversation share.
// Each takes the conversation's place at the same size, holds the keys
// until Esc or q closes it, and draws a title, a body and a dim note
// inside a teal border. A box with rows lets ↑ and ↓ move a marker, and
// scrolls to keep the marked row in view. It also holds the one command
// every box uses to send merud a request and wait for one reply.

package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// How long a box waits for merud. readTimeout covers a request that only
// reads, which merud answers from files and memory, so a slow answer means
// something is wrong. changeTimeout covers a change to config: merud may
// start or reconnect every MCP server after it, and each may take a while
// to start. The desktop app waits as long for the same ops.
const (
	readTimeout   = 5 * time.Second
	changeTimeout = 90 * time.Second
)

// boxOpen reports whether any box is open over the conversation.
func (m *Model) boxOpen() bool {
	return m.usageBox != nil || m.meBox != nil || m.mcpBox != nil || m.modelBox != nil ||
		m.chatsBox != nil || m.usedBox != nil || m.foldersBox != nil || m.skillsBox != nil ||
		m.logBox != nil || m.aboutBox != nil || m.helpBox != nil
}

// dropBoxes closes every box without giving the input its cursor back,
// for the approval box, which keeps the keys itself.
func (m *Model) dropBoxes() {
	m.usageBox, m.meBox, m.mcpBox, m.modelBox = nil, nil, nil, nil
	m.chatsBox, m.usedBox, m.foldersBox, m.skillsBox, m.logBox = nil, nil, nil, nil, nil
	m.aboutBox, m.helpBox = nil, nil
}

// closeBox closes whichever box is open and gives the input its cursor
// back.
func (m *Model) closeBox() {
	m.dropBoxes()
	m.input.Focus()
}

// openBox readies the screen for a box: the typed command leaves the
// input, which then takes no text while the box is open.
func (m *Model) openBox() {
	m.input.Reset()
	m.layout()
	m.input.Blur()
}

// boxKey handles one key while a box is open. Esc closes any box, and q
// closes one that isn't waiting for typed text. Every other key goes to the
// open box, which ignores the ones it has no use for, so typing can't leak
// into the input behind it. It returns the command a key starts, such as
// a change sent to merud. Update handles Ctrl-C and Ctrl-D before this, as
// it does for the approval box.
func (m *Model) boxKey(msg tea.KeyMsg) tea.Cmd {
	typing := m.mcpBox != nil && (m.mcpBox.keyFor != nil || m.mcpBox.walk != nil)
	if msg.Type == tea.KeyEsc && typing {
		// Esc leaves the key field or the connector form, not the box.
		m.mcpBox.keyFor, m.mcpBox.walk = nil, nil
		return nil
	}
	if msg.Type == tea.KeyEsc || (!typing && msg.Type == tea.KeyRunes && string(msg.Runes) == "q") {
		m.closeBox()
		return nil
	}
	switch {
	case m.mcpBox != nil:
		return m.mcpKey(msg)
	case m.meBox != nil:
		return m.meKey(msg)
	case m.chatsBox != nil:
		return m.chatsKey(msg)
	case m.usedBox != nil:
		return m.usedKey(msg)
	case m.foldersBox != nil:
		return m.foldersKey(msg)
	case m.skillsBox != nil:
		return m.skillsKey(msg)
	case m.logBox != nil:
		m.logBox.at = moveMark(msg, m.logBox.at, len(m.logBox.rows))
	case m.helpBox != nil:
		m.helpBox.at = moveMark(msg, m.helpBox.at, m.helpBox.lines)
	case m.aboutBox != nil:
		m.aboutBox.at = moveMark(msg, m.aboutBox.at, m.aboutBox.lines)
	}
	return nil
}

// boxKeys returns the help line's keys for the open box: the ones that
// box answers, then Esc or q to close, then Ctrl-C.
func (m *Model) boxKeys() keyList {
	bind := func(k, text string) key.Binding { return key.NewBinding(key.WithKeys(k), key.WithHelp(k, text)) }
	move := bind("↑/↓", "move")
	var k keyList
	switch {
	case m.mcpBox != nil && m.mcpBox.keyFor != nil:
		return keyList{bind("enter", "save key"), bind("esc", "cancel")}
	case m.mcpBox != nil && m.mcpBox.walk != nil:
		return keyList{bind("enter", "next"), bind("esc", "cancel")}
	case m.mcpBox != nil:
		// A connector row takes its own keys; the help line names the
		// ones the marked row takes, so it stays short.
		if r, ok := m.mcpBox.marked(); ok && r.cx >= 0 {
			k = keyList{move, bind("f", "fix"), bind("o", "on/off"), bind("a", "adopt")}
			break
		}
		k = keyList{move, bind("←/→", "policy"), bind("enter", "add"), bind("d", "remove")}
	case m.meBox != nil, m.usedBox != nil:
		k = keyList{move, bind("d", "forget")}
	case m.chatsBox != nil:
		k = keyList{move, bind("enter", "open"), bind("d", "delete")}
	case m.foldersBox != nil:
		k = keyList{move, bind("enter", "add"), bind("d", "remove")}
	case m.skillsBox != nil:
		k = keyList{move, bind("enter", "on/off")}
	case m.logBox != nil:
		k = keyList{move}
	case m.helpBox != nil, m.aboutBox != nil:
		k = keyList{bind("↑/↓", "scroll")}
	}
	return append(k,
		bind("esc/q", "close"),
		key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "stop/quit")),
	)
}

// moveMark returns where a marker at at lands after key msg in a list of
// n rows: ↑ and ↓ move it one row, PgUp and PgDn ten, Home and End to the
// ends. Any other key leaves it where it is. It stays in 0..n-1, and at 0
// when the list is empty.
func moveMark(msg tea.KeyMsg, at, n int) int {
	switch msg.Type {
	case tea.KeyUp:
		at--
	case tea.KeyDown:
		at++
	case tea.KeyPgUp:
		at -= 10
	case tea.KeyPgDown:
		at += 10
	case tea.KeyHome:
		at = 0
	case tea.KeyEnd:
		at = n - 1
	}
	return max(min(at, n-1), 0)
}

// isKey reports whether msg is the one printable key k, such as "d".
func isKey(msg tea.KeyMsg, k string) bool {
	return msg.Type == tea.KeyRunes && string(msg.Runes) == k
}

// markRow draws one row of a list: "› " in front of the marked row and two
// spaces in front of the others, so the marker shows with colour off too.
func (m *Model) markRow(on bool, s string) string {
	if on {
		return m.style.choiceOn.Render("› " + s)
	}
	return "  " + s
}

// boxRoom returns how many columns of text fit inside a box in a pane
// width columns wide. The margin, border and padding take answerIndent
// plus four columns.
func boxRoom(width int) int {
	return max(width-answerIndent-4, 1)
}

// boxPane draws a box for a pane width columns wide and height rows tall,
// and pads it to that height so the screen keeps its shape. The box holds
// title, a blank line, the body lines, a blank line and note, dim. It is
// boxPaneAt with no marked row.
func (m *Model) boxPane(title string, body []string, note string, width, height int) string {
	return m.boxPaneAt(title, body, -1, note, width, height)
}

// boxPaneAt draws a box as boxPane does, with body line focus kept in
// view: when the body is taller than the pane has room for, it shows a
// window of lines around focus, with "↑ more" or "↓ more" where lines
// are hidden. A focus of -1 shows the top.
//
// The box is as wide as its widest line, up to what the pane allows. Body
// lines wider than that are cut with "…", so a caller that wants them
// whole wraps them to boxRoom(width) first. The note may be wider than the
// body; it gets the width the pane allows, and wraps inside it.
func (m *Model) boxPaneAt(title string, body []string, focus int, note string, width, height int) string {
	avail := boxRoom(width)
	inner := len(title)
	for _, l := range body {
		inner = max(inner, ansi.StringWidth(l))
	}
	inner = min(max(inner, ansi.StringWidth(note)), avail)
	noteText := ansi.Wrap(note, inner, "")

	// The lines round the body: the blank line above the box, its top and
	// bottom border, the title and the blank under it, and the blank line
	// and the note under the body.
	room := height - 6 - (strings.Count(noteText, "\n") + 1)
	body = window(body, focus, room, m.style.dim.Render("↑ more"), m.style.dim.Render("↓ more"))

	lines := []string{m.style.brand.Render(title), ""}
	for _, l := range body {
		lines = append(lines, ansi.Truncate(l, inner, "…"))
	}
	lines = append(lines, "", m.style.dim.Render(noteText))
	box := m.style.box.Width(inner + 2).Render(strings.Join(lines, "\n"))

	// A blank line above the box, as above the first turn, then blank
	// lines to fill the pane. A pane too short for the box shows its top.
	out := append([]string{""}, strings.Split(box, "\n")...)
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out[:height], "\n")
}

// window returns the lines of body that fit in room rows, around line
// focus. The first or last line of the window gives way to up or down
// when lines lie hidden above or below it. With room for all of body, or
// room for fewer than three lines, it returns body as it is and lets the
// box cut it.
func window(body []string, focus, room int, up, down string) []string {
	if len(body) <= room || room < 3 {
		return body
	}
	// Centre the focus, then keep the window inside the body.
	start := min(max(focus-room/2, 0), len(body)-room)
	out := append([]string(nil), body[start:start+room]...)
	if start > 0 {
		out[0] = up
	}
	if start+room < len(body) {
		out[room-1] = down
	}
	return out
}

// replyMsg carries merud's answer to one request a box or a command sent.
// tag says which request it answers, ev is the reply event of the type
// asked for, and err says why none came.
type replyMsg struct {
	tag string
	ev  rpc.Event
	err error
}

// The requests a replyMsg can answer. Each file that sends one handles
// its reply in applyReply.
const (
	tagSessions  = "sessions"   // /chats: the list
	tagTurns     = "turns"      // /chats: one session's turns
	tagAttach    = "attach"     // /attach <path>
	tagForget    = "forget"     // d in the /me or /used box
	tagMemoryAdd = "memory_add" // /me add and /me prefer
	tagConns     = "connections"
	tagPolicy    = "policy"     // ←/→ in the /mcp box
	tagMCPAdd    = "mcp_add"    // enter in the /mcp box
	tagMCPRemove = "mcp_remove" // d d in the /mcp box
	tagFolders   = "folders"
	tagSkills    = "skills"
	tagLog       = "log"
	// The /mcp box's connector rows (connectors.go).
	tagConnectors   = "connectors"
	tagConnectorSet = "connector_set" // o, and the end of the f form
	tagConnectorFix = "connector_fix" // f
	tagAdoptPlan    = "adopt_plan"    // a
	tagAdopt        = "adopt"         // a a
)

// requestCmd returns a command that sends req to merud once and hands
// back its event of type want as a replyMsg tagged tag. It waits at most
// timeout. It reports an error when merud can't be reached, answers with
// an error event, or sends no event of that type.
func requestCmd(ask askFunc, tag string, req rpc.Request, want rpc.EventType, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		msg := replyMsg{tag: tag}
		got := false
		// These requests run no tools, so a nil ApproveFunc is right: it
		// would deny any approval.
		for ev, err := range ask(ctx, req, nil) {
			if err != nil {
				msg.err = err
				return msg
			}
			switch ev.Type {
			case want:
				msg.ev, got = ev, true
			case rpc.EventError:
				msg.err = errors.New(ev.Error)
				return msg
			}
		}
		if !got {
			msg.err = errors.New("merud sent no " + string(want) + " reply")
		}
		return msg
	}
}

// applyReply hands one replyMsg to the code that sent its request.
func (m *Model) applyReply(msg replyMsg) tea.Cmd {
	switch msg.tag {
	case tagSessions, tagTurns:
		return m.applyChats(msg)
	case tagDelete, tagBoxDelete, tagLeave, tagChatMeta, tagChatFolders:
		m.applyOrganize(msg)
	case tagAttach:
		m.applyAttach(msg)
	case tagForget, tagMemoryAdd:
		return m.applyMemoryChange(msg)
	case tagConns, tagPolicy, tagMCPAdd, tagMCPRemove:
		return m.applyConnections(msg)
	case tagConnectors, tagConnectorSet, tagConnectorFix, tagAdoptPlan, tagAdopt:
		return m.applyConnector(msg)
	case tagFolders:
		m.applyFolders(msg)
	case tagSkills:
		m.applySkills(msg)
	case tagLog:
		m.applyLog(msg)
	}
	return nil
}
