// This file holds the approval box: the question the chat screen shows when
// merud asks whether a tool call may run. It opens the box when an
// approvalRequestMsg arrives, answers it from the keys, and draws it. See
// ARCHITECTURE.md, "Approving a tool call".

package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// boxArgLines caps how many lines of a call's arguments the box shows, so
// the box leaves room for the question and the answer above it.
const boxArgLines = 10

// openApproval shows the approval box for msg. A request from a turn that
// is no longer streaming, or one with no choices, gets "deny" at once: the
// user can't see it, so nobody could agree to it.
//
// The box starts with "deny" selected when merud offers it, so an Enter
// meant for the input box can't approve a call by accident.
func (m *Model) openApproval(msg approvalRequestMsg) {
	live := m.streaming && msg.turn == m.turn
	if msg.save {
		live = m.saving != 0 && msg.turn == m.saving
	}
	if !live || len(msg.approval.Choices) == 0 {
		msg.reply <- rpc.ChoiceDeny
		return
	}
	// A second request while one is open can't happen, because rpc.Do asks
	// about one call at a time, and a save waits while a turn runs. Deny
	// the old one anyway, so its goroutine never waits for an answer that
	// won't come.
	m.closeApproval(rpc.ChoiceDeny)
	selected := max(slices.Index(msg.approval.Choices, rpc.ChoiceDeny), 0)
	m.approval = &pendingApproval{ask: msg.approval, reply: msg.reply, selected: selected, save: msg.save}
	// The approval box needs the keys and the conversation it sits in, so
	// it closes any open box.
	m.dropBoxes()
	m.input.Blur() // hide the cursor: the input takes no text while the box is open
	m.refresh()
}

// closeApproval sends choice back to the waiting goroutine and closes the
// box. It does nothing when no box is open.
func (m *Model) closeApproval(choice rpc.Choice) {
	if m.approval == nil {
		return
	}
	// The channel has room for one value and gets only this one, so the
	// send never waits.
	m.approval.reply <- choice
	m.approval = nil
	m.input.Focus()
	m.refresh()
}

// approvalKey answers the open box from one key press: a choice's letter
// picks it; ←/→ move the selection and Enter picks it; e, on a turn's
// box, is Edit first (editFirst). Other keys do nothing, so typing can't
// leak into the input. Update handles Ctrl-C and Ctrl-D before this, the
// same as when no box is open.
func (m *Model) approvalKey(msg tea.KeyMsg) {
	a := m.approval
	if !a.save && isKey(msg, editKey) {
		m.editFirst()
		return
	}
	switch msg.Type {
	case tea.KeyLeft:
		a.selected = max(a.selected-1, 0)
		m.refresh()
	case tea.KeyRight:
		a.selected = min(a.selected+1, len(a.ask.Choices)-1)
		m.refresh()
	case tea.KeyEnter:
		m.closeApproval(a.ask.Choices[a.selected])
	case tea.KeyRunes:
		for _, c := range a.ask.Choices {
			if strings.EqualFold(string(msg.Runes), choiceKey(c)) {
				m.closeApproval(c)
				return
			}
		}
	}
}

// editKey is the key for Edit first in a turn's approval box. No choice
// merud offers starts with "e", so it can't clash with one.
const editKey = "e"

// editFirst is the desktop app's Edit first: it answers deny, then puts the
// call in the input as a draft (draftOf), for the user to change and send
// as a new question. The model then makes the call again with the new
// text, and merud asks again, so nothing runs that the user didn't see.
// While the declined turn finishes, Enter queues the draft behind it.
func (m *Model) editFirst() {
	draft := draftOf(m.approval.ask)
	m.closeApproval(rpc.ChoiceDeny)
	m.input.SetValue(draft)
	m.layout()
	m.notice = "declined; the call is in the box as a draft · change it and press enter"
}

// draftOf writes the text Edit first puts in the input for call a: one
// line that says what to do, then the arguments laid out as the box shows
// them.
func draftOf(a rpc.Approval) string {
	args := rpc.ArgsLines(a.Args, 0)
	if len(args) == 0 {
		return "Run " + a.Name + " instead."
	}
	return "Run " + a.Name + " with these arguments instead:\n\n" + strings.Join(args, "\n")
}

// choiceKey returns the key that picks c: the first letter of its name,
// "o", "s" or "d".
func choiceKey(c rpc.Choice) string {
	if c == "" {
		return ""
	}
	return string(c)[:1]
}

// choiceLabel returns the words the box shows for c.
func choiceLabel(c rpc.Choice) string {
	switch c {
	case rpc.ChoiceOnce:
		return "once"
	case rpc.ChoiceSession:
		return "this session"
	case rpc.ChoiceDeny:
		return "deny"
	}
	return string(c)
}

// approvalBoxView draws the open approval box, width columns wide at most
// counting its margin:
//
//	╭───────────────────────────────────────────╮
//	│ Run mail.send?  mcp                       │
//	│ {                                         │
//	│   "to": "sam@example.com"                 │
//	│ }                                         │
//	│                                           │
//	│  o once  [d deny]  e edit first           │
//	╰───────────────────────────────────────────╯
//
// Square brackets mark the choice Enter picks, so the selection shows with
// colour off too. A save's box leaves out Edit first: it would have
// nothing to edit.
func (m *Model) approvalBoxView(width int) string {
	a := m.approval
	// The border and padding take four columns.
	inner := max(width-4, 1)
	fit := func(s string) string { return ansi.Truncate(s, inner, "…") }

	lines := []string{fit(m.style.approvalTitle.Render("Run "+a.ask.Name+"?") + "  " + m.style.dim.Render(a.ask.Kind))}
	for _, l := range rpc.ArgsLines(a.ask.Args, boxArgLines) {
		lines = append(lines, fit(l))
	}
	lines = append(lines, "")

	var choices []string
	for i, c := range a.ask.Choices {
		text := choiceKey(c) + " " + choiceLabel(c)
		if i == a.selected {
			choices = append(choices, m.style.choiceOn.Render("["+text+"]"))
		} else {
			choices = append(choices, m.style.choice.Render(" "+text+" "))
		}
	}
	if !a.save {
		choices = append(choices, m.style.choice.Render(" "+editKey+" edit first "))
	}
	lines = append(lines, fit(strings.Join(choices, " ")))
	return m.style.approvalBox.Render(strings.Join(lines, "\n"))
}
