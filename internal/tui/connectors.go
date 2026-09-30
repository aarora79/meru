// This file holds the connector rows of the chat's /mcp box: one row per
// connector merud runs, with its state and sentence, and the three keys
// that act on one. f is the Fix button: merud says which fields to ask,
// and a small form asks them one at a time, a secret without showing it.
// o turns the connector on or off, and a adopts a server set up by hand,
// after showing merud's plan. merud makes every change (rpc.OpConnectorSet,
// OpConnectorFix, OpConnectorAdopt) and reports the connector's progress,
// which the box's note line shows as it comes. See ARCHITECTURE.md,
// "Setting up a connector".

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorsHead heads the connector rows in the /mcp box.
const connectorsHead = "Connectors"

// connectorTimeout bounds how long the box waits for a connector_set or a
// connector_fix: merud follows the connector for up to five minutes while
// it installs, plus the reload before that.
const connectorTimeout = 6 * time.Minute

// connectorLine writes one connector's row, such as "obsidian · needs
// config · Obsidian needs your vault folder.", with the sign-in link when
// the connector waits for one.
func connectorLine(c rpc.ConnectorStatus) string {
	s := fmt.Sprintf("  %s · %s · %s", c.ID, rpc.ConnectorWords(c.State), oneLine(c.Sentence))
	if c.Link != "" {
		s += " Sign in: " + c.Link
	}
	return s
}

// connectorStepMsg carries one step of merud's progress on a connector
// ("Meru is installing Obsidian 2.0.1 and checking it.") into Update,
// which shows it on the /mcp box's note line.
type connectorStepMsg struct {
	sentence string
}

// fieldWalk is the connector form f opens in the /mcp box: the fields
// merud asked for, one at a time, and the answers so far. turnOn is true
// for a connector that was off, which the form turns on with its values.
type fieldWalk struct {
	row    rpc.ConnectorStatus
	fields []rpc.ConnectorField
	at     int
	input  textinput.Model
	change rpc.ConnectorChange
	turnOn bool
}

// newFieldWalk opens the form for the fields of row, and returns it with
// the command that makes its field's cursor blink.
func newFieldWalk(row rpc.ConnectorStatus, fields []rpc.ConnectorField) (*fieldWalk, tea.Cmd) {
	w := &fieldWalk{
		row: row, fields: fields, turnOn: row.State == rpc.ConnectorOff,
		change: rpc.ConnectorChange{Values: map[string]string{}, Secrets: map[string]string{}},
	}
	return w, w.open()
}

// open readies the input for the field at w.at: a secret shows • for
// each character, and the placeholder says what Enter keeps.
func (w *fieldWalk) open() tea.Cmd {
	f := w.fields[w.at]
	in := textinput.New()
	in.Prompt = "value: "
	in.CharLimit = 0 // no limit; merud decides what is too long
	switch {
	case f.Type == "secret":
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
		if f.Saved {
			in.Placeholder = "saved; Enter keeps it"
		}
	case f.Value != "":
		in.Placeholder = f.Value
	case f.Default != "":
		in.Placeholder = f.Default
	}
	w.input = in
	return w.input.Focus()
}

// take keeps what the user typed for the field at w.at, if anything, and
// reports whether it was the last field. An empty answer keeps the value
// merud holds.
func (w *fieldWalk) take() bool {
	f := w.fields[w.at]
	v := strings.TrimSpace(w.input.Value())
	switch {
	case v == "":
	case f.Type == "secret":
		w.change.Secrets[f.ID] = v
	case v != f.Value:
		w.change.Values[f.ID] = v
	}
	w.at++
	return w.at == len(w.fields)
}

// view returns the form's lines for the box body, and the note under it.
// A secret's text never shows: the input draws • in its place.
func (w *fieldWalk) view() ([]string, string) {
	f := w.fields[w.at]
	title := f.Label
	if !f.Required {
		title += " (optional)"
	}
	lines := []string{fmt.Sprintf("Set up %s · %d of %d", w.row.Name, w.at+1, len(w.fields)), title}
	if f.Help != "" {
		lines = append(lines, f.Help)
	}
	if len(f.Choices) > 0 {
		lines = append(lines, "One of: "+strings.Join(f.Choices, ", "))
	}
	lines = append(lines, w.input.View())
	note := "Enter keeps this and goes on; Esc stops. merud checks each value before it saves."
	if f.Type == "secret" {
		note = "The secret goes to merud, which saves it in secrets.toml; it never shows again."
	}
	return lines, note
}

// walkKey handles a key while the connector form is open: Enter keeps
// the field and moves on, and after the last field sends the answers to
// merud; every other key types into the field. The box's Esc closes the
// form first.
func (m *Model) walkKey(msg tea.KeyMsg) tea.Cmd {
	b := m.mcpBox
	w := b.walk
	if msg.Type != tea.KeyEnter {
		var cmd tea.Cmd
		w.input, cmd = w.input.Update(msg)
		return cmd
	}
	if !w.take() {
		return w.open()
	}
	b.walk = nil
	change := w.change
	if w.turnOn {
		on := true
		change.Enabled = &on
	}
	b.busy = "saving " + w.row.Name + "…"
	return connectorCmd(m.ask, m.send, tagConnectorSet, rpc.Request{Op: rpc.OpConnectorSet, ID: w.row.ID, Connector: &change})
}

// connectorKey handles a key on connector c's row: f asks merud to fix it,
// o turns it on or off, and a, for one set up by hand, asks merud for the
// adopt plan and, pressed again while the plan shows (adoptFor names c),
// applies it.
func (m *Model) connectorKey(msg tea.KeyMsg, c rpc.ConnectorStatus, adoptFor string) tea.Cmd {
	b := m.mcpBox
	switch {
	case isKey(msg, "f"):
		b.busy = "fixing " + c.Name + "…"
		return connectorCmd(m.ask, m.send, tagConnectorFix, rpc.Request{Op: rpc.OpConnectorFix, ID: c.ID})
	case isKey(msg, "o"):
		switch {
		case c.Kind == "dependency":
			m.notice = "Meru doesn't run " + c.Name + "; it only checks that it answers"
			return nil
		case c.State == rpc.ConnectorByHand:
			m.notice = c.Name + " is set up by hand; press a to have Meru run it"
			return nil
		}
		on := c.State == rpc.ConnectorOff
		verb := "turning " + c.Name + " off…"
		if on {
			verb = "turning " + c.Name + " on…"
		}
		b.busy = verb
		return connectorCmd(m.ask, m.send, tagConnectorSet, rpc.Request{Op: rpc.OpConnectorSet, ID: c.ID, Connector: &rpc.ConnectorChange{Enabled: &on}})
	case isKey(msg, "a"):
		if c.State != rpc.ConnectorByHand {
			m.notice = "only a server set up by hand can be adopted"
			return nil
		}
		if adoptFor == c.ID {
			b.busy = "adopting " + c.ID + "…"
			return requestCmd(m.ask, tagAdopt, rpc.Request{Op: rpc.OpConnectorAdopt, ID: c.ID, Adopt: &rpc.AdoptRequest{Apply: true}}, rpc.EventAdopt, changeTimeout)
		}
		b.busy = "asking merud what adopting " + c.ID + " changes…"
		return requestCmd(m.ask, tagAdoptPlan, rpc.Request{Op: rpc.OpConnectorAdopt, ID: c.ID}, rpc.EventAdopt, readTimeout)
	}
	return nil
}

// connectorCmd returns a command that sends req, a connector_set or a
// connector_fix, and hands each "connector" event but the last to Update
// through send as a connectorStepMsg, as it arrives. The last comes back
// as a replyMsg tagged tag, and says where the connector settled. It
// fails when merud can't be reached, refuses, or sends no status.
func connectorCmd(ask askFunc, send sender, tag string, req rpc.Request) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), connectorTimeout)
		defer cancel()
		msg := replyMsg{tag: tag}
		got := false
		for ev, err := range ask(ctx, req, nil) {
			if err != nil {
				msg.err = err
				return msg
			}
			switch ev.Type {
			case rpc.EventConnector:
				if ev.Connector == nil {
					continue
				}
				if got && send != nil {
					send.Send(connectorStepMsg{sentence: msg.ev.Connector.Sentence})
				}
				msg.ev, got = ev, true
			case rpc.EventError:
				msg.err = errors.New(ev.Error)
				return msg
			}
		}
		if !got {
			msg.err = errors.New("merud sent no connector status")
		}
		return msg
	}
}

// applyConnectorStep shows one step of merud's progress on the /mcp box's
// note line, while the box waits for the change.
func (m *Model) applyConnectorStep(msg connectorStepMsg) {
	if b := m.mcpBox; b != nil && b.busy != "" {
		b.busy = msg.sentence
	}
}

// applyConnector takes in merud's answer to a connector request from the
// /mcp box: the connector list, the end of a set or a fix, or an adopt's
// plan or result. A set, a fix with nothing to ask, and an adopt end with
// a fresh look at the sources and the connectors; a fix that names
// fields opens the form for them.
func (m *Model) applyConnector(msg replyMsg) tea.Cmd {
	b := m.mcpBox
	if b == nil {
		return nil
	}
	if msg.tag == tagConnectors {
		// A merud from before the connectors op says it doesn't know it;
		// the box then shows no connector rows.
		if msg.err == nil {
			b.connectors = msg.ev.Connectors
			b.at = min(b.at, max(b.actCount()-1, 0))
		}
		return nil
	}
	b.busy = ""
	if msg.err != nil {
		m.notice = "no change: " + msg.err.Error()
		if msg.tag == tagConnectorSet && strings.Contains(msg.err.Error(), " needs ") {
			m.notice += " Press f to set it up."
		}
		return nil
	}
	refresh := requestCmd(m.ask, tagConns, rpc.Request{Op: rpc.OpConnections}, rpc.EventConnections, readTimeout)
	switch msg.tag {
	case tagAdoptPlan:
		a := msg.ev.Adopted
		if a == nil || a.Nothing {
			if a != nil {
				m.notice = strings.Join(a.Changes, " ")
			}
			return nil
		}
		b.adoptFor, b.adoptPlan = a.ID, a.Changes
		return nil
	case tagAdopt:
		m.notice = "adopted " + msg.ev.Adopted.ID + "; meru mcp unadopt " + msg.ev.Adopted.ID + " puts the old entry back"
		return refresh
	}
	row := *msg.ev.Connector
	if msg.tag == tagConnectorFix {
		if fields := rpc.AskFields(row); len(fields) > 0 {
			var cmd tea.Cmd
			b.walk, cmd = newFieldWalk(row, fields)
			return cmd
		}
	}
	m.notice = row.Name + ": " + rpc.ConnectorWords(row.State) + ". " + row.Sentence
	if row.Link != "" {
		m.notice += " Sign in: " + row.Link
	}
	return refresh
}
