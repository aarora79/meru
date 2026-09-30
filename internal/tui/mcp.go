// This file holds the MCP status table that `meru mcp` prints (MCPTable),
// and the chat's /mcp box, the desktop app's Connections: every tool
// source with each tool's Off, Ask or Allow, which ← and → change, and the
// catalog servers, which Enter adds. merud makes every change
// (rpc.OpToolPolicy, OpMCPAdd, OpMCPRemove, OpSecretSet) and writes
// config.toml and secrets.toml; this file only asks and lays out the
// answers.

package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// noServers is what the table says when config has no MCP servers.
const noServers = "No MCP servers in config.toml. Add one with meru mcp add <name>; meru mcp list shows the catalog."

// MCPTable lays out merud's MCP status rows as the lines of a table, one
// per server under a header:
//
//	SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM
//	google     http       connected       124        6        2   127.0.0.1:8000/mcp
//	obsidian   stdio      connected        13        5        1
//	notes      stdio      not connected     —        3        1   exec: "npx" not found
//
// TOOLS shows "—" for a server that isn't connected (Tools is -1). The last
// field is the reason a server isn't connected, or else the URL of an HTTP
// server without its scheme. A connector merud runs shows its own state
// instead, such as "ok" or "needs config", and its sentence:
//
//	obsidian   stdio      ok                3        3        0   Obsidian is ready. It starts when a question needs it.
//
// With no rows it returns one line that says how to add a server.
func MCPTable(rows []rpc.MCPStatus) []string {
	if len(rows) == 0 {
		return []string{noServers}
	}
	// The SERVER column grows to fit the longest name.
	nameWidth := 10
	for _, r := range rows {
		nameWidth = max(nameWidth, len([]rune(r.Name)))
	}
	// %-*s pads a string to a width taken from the argument before it; %*s
	// pads on the left, so numbers line up on the right. fmt counts
	// characters, not bytes, so "—" takes one column.
	line := func(name, transport, state, tools, allowed, confirm, last string) string {
		s := fmt.Sprintf("%-*s %-10s %-13s %5s  %7s  %7s   %s",
			nameWidth, name, transport, state, tools, allowed, confirm, last)
		return strings.TrimRight(s, " ")
	}
	out := []string{line("SERVER", "TRANSPORT", "STATE", "TOOLS", "ALLOWED", "CONFIRM", "")}
	for _, r := range rows {
		tools := "—"
		if r.Tools >= 0 {
			tools = fmt.Sprint(r.Tools)
		}
		last := strings.TrimPrefix(strings.TrimPrefix(r.URL, "http://"), "https://")
		if r.State != rpc.MCPConnected {
			last = oneLine(r.Err)
		}
		state := r.State
		if r.Connector != "" {
			state, last = rpc.ConnectorWords(r.Connector), oneLine(r.Sentence)
		}
		out = append(out, line(r.Name, r.Transport, state, tools,
			fmt.Sprint(r.Allowed), fmt.Sprint(r.Confirm), last))
	}
	return out
}

// ConnectorTable lays out merud's connectors as lines, one per connector
// under a header, with its state and sentence, and for one that needs
// config, where to set what it needs:
//
//	CONNECTOR  STATE
//	obsidian   needs config    Obsidian needs your vault folder. Set vault_path under [connectors.obsidian] in config.toml, then restart merud.
//
// With no rows it returns nothing.
func ConnectorTable(rows []rpc.ConnectorStatus) []string {
	if len(rows) == 0 {
		return nil
	}
	out := []string{"CONNECTOR  STATE"}
	for _, r := range rows {
		text := r.Sentence
		if hint := rpc.FixHint(r.ID, r.Fix); hint != "" {
			text += " " + hint
		}
		out = append(out, fmt.Sprintf("%-10s %-15s %s", r.ID, rpc.ConnectorWords(r.State), oneLine(text)))
	}
	return out
}

// oneLine folds s onto one line, so a reason with a line break keeps the
// table's shape.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mcpNote closes the /mcp box.
const mcpNote = "Ask means Meru asks before each call. meru mcp add stdio|http adds a server of your own."

// mcpBox is the open /mcp box, the desktop app's Connections: every tool
// source merud knows, each tool with its policy, then the catalog servers
// not added yet. It opens at once with loading set, and merud's reply
// fills in conns and catalog, or err.
type mcpBox struct {
	loading bool
	err     string
	conns   []rpc.Connection
	catalog []rpc.CatalogEntry // only the entries not added yet
	// at is the marked row among the ones a key can act on (mcpRows).
	at int
	// busy says what merud is doing for the box, such as "adding
	// obsidian…"; the box takes no change until it answers. pending names
	// the server an add or a remove is for, for the notice.
	busy    string
	pending string
	// confirm names the server a first d asked to remove.
	confirm string
	// keyFor is the catalog server whose API key the key field takes, and
	// need what it asks for; nil while the box asks for no key. key is the
	// field, which shows • for each character.
	keyFor *rpc.CatalogEntry
	need   rpc.CatalogNeed
	key    textinput.Model
}

// mcpRow is one line of the /mcp box. act is false for a line no key acts
// on, such as a heading. conn and tool index the connection and its tool,
// and entry the catalog server; -1 means none.
type mcpRow struct {
	text  string
	act   bool
	conn  int
	tool  int
	entry int
}

// mcpRows lays out the box's lines: for each source a heading, which a key
// can act on only for an MCP server in config.toml, which d removes, then
// a row per tool, then the catalog servers under "Add a connection". A
// connector merud runs has no [[mcp.servers]] entry to remove.
func (b *mcpBox) mcpRows() []mcpRow {
	var rows []mcpRow
	for ci, c := range b.conns {
		if ci > 0 {
			rows = append(rows, mcpRow{conn: -1, tool: -1, entry: -1})
		}
		removable := c.Kind == "mcp" && (c.Connector == "" || c.Connector == rpc.ConnectorByHand)
		rows = append(rows, mcpRow{text: connHeading(c), act: removable, conn: ci, tool: -1, entry: -1})
		if c.Fixed && c.Note != "" {
			rows = append(rows, mcpRow{text: "    " + c.Note, conn: -1, tool: -1, entry: -1})
		}
		for ti, t := range c.Tools {
			text := fmt.Sprintf("  %-11s %s", policyWords(t.Policy), t.Name)
			if t.Missing {
				text += " · not offered"
			}
			rows = append(rows, mcpRow{text: text, act: !c.Fixed, conn: ci, tool: ti, entry: -1})
		}
	}
	if len(b.catalog) > 0 {
		rows = append(rows, mcpRow{conn: -1, tool: -1, entry: -1}, mcpRow{text: "Add a connection", conn: -1, tool: -1, entry: -1})
	}
	for ei, e := range b.catalog {
		text := "  " + e.Name + " · " + e.Title
		if _, ok := keyNeed(e); ok {
			text += " · needs an API key"
		}
		rows = append(rows, mcpRow{text: text, act: true, conn: -1, tool: -1, entry: ei})
	}
	return rows
}

// marked returns the row the marker sits on, and false when no row can
// take it.
func (b *mcpBox) marked() (mcpRow, bool) {
	n := 0
	for _, r := range b.mcpRows() {
		if !r.act {
			continue
		}
		if n == b.at {
			return r, true
		}
		n++
	}
	return mcpRow{}, false
}

// actCount returns how many rows a key can act on.
func (b *mcpBox) actCount() int {
	n := 0
	for _, r := range b.mcpRows() {
		if r.act {
			n++
		}
	}
	return n
}

// connHeading writes one source's heading: its name, what it is, whether
// it is connected, and how many of its tools are on, such as "google · MCP
// server, http · connected · 6 of 124 tools on".
func connHeading(c rpc.Connection) string {
	name, what := c.Name, "MCP server"
	switch c.Kind {
	case "a2a":
		what = "agent"
	case "builtin":
		name, what = "meru", "built in"
	case "command":
		name, what = "Local commands", "programs on this computer"
	}
	if c.Transport != "" {
		what += ", " + c.Transport
	}
	if c.Remote {
		what += ", remote"
	}
	on := 0
	for _, t := range c.Tools {
		if t.Policy != rpc.PolicyOff {
			on++
		}
	}
	total := len(c.Tools)
	if c.Kind != "builtin" && c.Offered > total {
		total = c.Offered
	}
	s := fmt.Sprintf("%s · %s · %d of %d tools on", name, what, on, total)
	switch {
	case c.Connector != "" && c.Connector != rpc.ConnectorByHand:
		// A connector merud runs: its own state and sentence, and where
		// to set a field it needs.
		s = fmt.Sprintf("%s · connector, %s · %s · %d of %d tools on · %s", name, c.Transport,
			rpc.ConnectorWords(c.Connector), on, total, oneLine(c.Sentence))
		if hint := rpc.FixHint(c.Name, c.Fix); hint != "" {
			s += " " + hint
		}
	case c.Kind == "builtin" && c.WebSentence != "":
		// The built-in tools hold web search, whose state is the SearXNG
		// connector's.
		s += " · web search " + rpc.ConnectorWords(c.Web) + ": " + oneLine(c.WebSentence)
	case c.Kind == "mcp" || c.Kind == "a2a":
		s = fmt.Sprintf("%s · %s · %s · %d of %d tools on", name, what, c.State, on, total)
		if c.State != rpc.MCPConnected && c.Err != "" {
			s += " · " + oneLine(c.Err)
		}
		if c.Connector == rpc.ConnectorByHand {
			s += " · set up by hand"
		}
	}
	return s
}

// policyWords says a policy the way the app's switches do: Off, Ask,
// Allow, or Always asks for a tool that asks before every call.
func policyWords(p string) string {
	switch p {
	case rpc.PolicyOff:
		return "Off"
	case rpc.PolicyAsk:
		return "Ask"
	case rpc.PolicyAllow:
		return "Allow"
	case rpc.PolicyAlways:
		return "Always asks"
	}
	return p
}

// nextPolicy returns the policy one step left (step -1) or right (+1) of
// tool t's, in the order Off, Ask, Allow. A tool that always asks steps
// between Off and Always asks, which merud turns on as Ask; it never
// reaches Allow. configure always asks even while it is off.
func nextPolicy(kind string, t rpc.ToolPolicy, step int) string {
	if t.Policy == rpc.PolicyAlways || (kind == "builtin" && t.Name == "configure") {
		if step > 0 {
			return rpc.PolicyAsk
		}
		return rpc.PolicyOff
	}
	order := []string{rpc.PolicyOff, rpc.PolicyAsk, rpc.PolicyAllow}
	i := max(slices.Index(order, t.Policy), 0)
	return order[min(max(i+step, 0), len(order)-1)]
}

// keyNeed returns the API key catalog server e still needs, and false when
// it needs none or secrets.toml holds it already.
func keyNeed(e rpc.CatalogEntry) (rpc.CatalogNeed, bool) {
	for _, n := range e.Needs {
		if n.Kind == "api_key" && !n.Saved {
			return n, true
		}
	}
	return rpc.CatalogNeed{}, false
}

// mcpKey handles a key in the /mcp box. While the key field is open,
// Enter sends the key and every other key types into the field. Otherwise
// ↑ and ↓ move the marker; ← and → step a tool's policy; Enter adds the
// marked catalog server, first asking for its API key when it needs one;
// and d removes the marked MCP server after a second d.
func (m *Model) mcpKey(msg tea.KeyMsg) tea.Cmd {
	b := m.mcpBox
	if b.keyFor != nil {
		if msg.Type != tea.KeyEnter {
			var cmd tea.Cmd
			b.key, cmd = b.key.Update(msg)
			return cmd
		}
		key := strings.TrimSpace(b.key.Value())
		if key == "" {
			return nil
		}
		e := *b.keyFor
		b.keyFor = nil
		b.busy, b.pending = "saving the key and adding "+e.Name+"…", e.Name
		return addServerCmd(m.ask, e.Name, b.need.Secret, key)
	}
	b.at = moveMark(msg, b.at, b.actCount())
	confirm := b.confirm
	b.confirm = ""
	r, ok := b.marked()
	if !ok || b.busy != "" {
		return nil
	}
	switch {
	case (msg.Type == tea.KeyLeft || msg.Type == tea.KeyRight) && r.tool >= 0:
		c := b.conns[r.conn]
		t := c.Tools[r.tool]
		step := 1
		if msg.Type == tea.KeyLeft {
			step = -1
		}
		next := nextPolicy(c.Kind, t, step)
		if next == t.Policy || (t.Policy == rpc.PolicyAlways && next == rpc.PolicyAsk) {
			return nil
		}
		server := c.Name
		if c.Kind == "builtin" {
			server = "meru" // merud's name for its own tools
		}
		b.busy = "setting " + t.Name + " to " + policyWords(next) + "…"
		change := rpc.PolicyChange{Kind: c.Kind, Server: server, Tool: t.Name, Policy: next}
		return requestCmd(m.ask, tagPolicy, rpc.Request{Op: rpc.OpToolPolicy, Policy: &change}, rpc.EventConnections, changeTimeout)
	case msg.Type == tea.KeyEnter && r.entry >= 0:
		e := b.catalog[r.entry]
		if need, ok := keyNeed(e); ok {
			b.keyFor, b.need = &e, need
			b.key = newKeyField()
			return b.key.Focus()
		}
		b.busy, b.pending = "adding "+e.Name+"…", e.Name
		return requestCmd(m.ask, tagMCPAdd, rpc.Request{Op: rpc.OpMCPAdd, ID: e.Name}, rpc.EventConnections, changeTimeout)
	case isKey(msg, "d") && r.tool < 0 && r.entry < 0:
		name := b.conns[r.conn].Name
		if confirm != name {
			b.confirm = name
			return nil
		}
		b.busy, b.pending = "removing "+name+"…", name
		return requestCmd(m.ask, tagMCPRemove, rpc.Request{Op: rpc.OpMCPRemove, ID: name}, rpc.EventConnections, changeTimeout)
	}
	return nil
}

// newKeyField returns the field an API key goes into. It shows • for each
// character, so a key typed or pasted never shows on screen.
func newKeyField() textinput.Model {
	f := textinput.New()
	f.EchoMode = textinput.EchoPassword
	f.EchoCharacter = '•'
	f.Prompt = "key: "
	f.CharLimit = 0 // no limit; merud decides what is too long
	return f
}

// addServerCmd returns a command that saves key as the secret secret,
// through merud (rpc.OpSecretSet), and then adds the catalog server name
// (rpc.OpMCPAdd), as the app does. The key goes to merud over the socket,
// which only this user can open, and never comes back. The reply is the
// add's, tagged tagMCPAdd, or the save's error.
func addServerCmd(ask askFunc, name, secret, key string) tea.Cmd {
	save := requestCmd(ask, tagMCPAdd, rpc.Request{Op: rpc.OpSecretSet, ID: secret, Text: key}, rpc.EventDone, changeTimeout)
	add := requestCmd(ask, tagMCPAdd, rpc.Request{Op: rpc.OpMCPAdd, ID: name}, rpc.EventConnections, changeTimeout)
	return func() tea.Msg {
		// msg.(replyMsg) is a type assertion; save always returns one.
		if msg := save().(replyMsg); msg.err != nil {
			return msg
		}
		return add()
	}
}

// applyConnections takes in merud's reply to /mcp or to a change made in
// its box: the sources and the catalog as they stand now. A change that
// failed keeps the box as it was and says why on the notice line.
func (m *Model) applyConnections(msg replyMsg) tea.Cmd {
	b := m.mcpBox
	if b == nil {
		return nil
	}
	b.loading, b.busy = false, ""
	name := b.pending
	b.pending = ""
	if msg.err != nil {
		if msg.tag == tagConns {
			b.err = msg.err.Error()
		} else {
			m.notice = "no change: " + msg.err.Error()
		}
		return nil
	}
	b.conns = msg.ev.Connections
	b.catalog = nil
	for _, e := range msg.ev.Catalog {
		if !e.Added {
			b.catalog = append(b.catalog, e)
		}
	}
	b.at = min(b.at, max(b.actCount()-1, 0))
	switch msg.tag {
	case tagPolicy:
		m.notice = "saved to config.toml"
	case tagMCPAdd:
		m.notice = "added " + name + ": its tools are as the catalog sets them"
	case tagMCPRemove:
		m.notice = "removed " + name + "; its key stays in secrets.toml"
	}
	return nil
}

// mcpBoxView draws the /mcp box for a pane width columns wide and height
// rows tall (see boxPaneAt), with the marked row kept in view.
func (m *Model) mcpBoxView(width, height int) string {
	b := m.mcpBox
	var body []string
	focus := -1
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no connections: "+b.err, boxRoom(width))
	case len(b.conns) == 0 && len(b.catalog) == 0:
		body = []string{"No tool sources."}
	}
	n := 0
	for _, r := range b.mcpRows() {
		switch {
		case !r.act && r.text != "" && r.tool < 0 && r.conn >= 0, !r.act && r.text == "Add a connection":
			// Two spaces line a heading up with the ones a marker can
			// sit on.
			body = append(body, "  "+m.style.brand.Render(r.text))
		case !r.act:
			body = append(body, m.style.dim.Render(r.text))
		default:
			if n == b.at {
				focus = len(body)
			}
			text := r.text
			if r.tool < 0 && r.entry < 0 {
				text = m.style.brand.Render(text)
			}
			body = append(body, m.markRow(n == b.at, text))
			n++
		}
	}
	note := mcpNote
	switch {
	case b.keyFor != nil:
		body = append(body, "", b.need.Prompt, b.key.View())
		focus = len(body) - 1
		note = "The key goes to merud, which saves it in secrets.toml; it never shows again."
		if b.need.Help != "" {
			note = b.need.Help + " " + note
		}
	case b.busy != "":
		note = b.busy
	case b.confirm != "":
		note = "Press d again to remove " + b.confirm + " from config.toml; any other key keeps it."
	}
	return m.boxPaneAt("Connections", body, focus, note, width, height)
}
