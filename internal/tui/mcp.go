// This file holds the MCP status table and the /mcp box that shows it in
// the chat. `meru mcp` prints the same table through MCPTable, so the two
// views can't drift apart. merud sends the rows (rpc.OpMCPStatus); this
// file only lays them out.

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// mcpNote closes the /mcp box: where to see more, and how to add a server.
const mcpNote = "meru tools lists each tool; meru mcp list shows the catalog and meru mcp add adds a server."

// noServers is what the table says when config has no MCP servers.
const noServers = "No MCP servers in config.toml. Add one with meru mcp add <name>; meru mcp list shows the catalog."

// MCPTable lays out merud's MCP status rows as the lines of a table, one
// per server under a header:
//
//	SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM
//	google     http       connected       124        6        2   127.0.0.1:8000/mcp
//	brave      stdio      connected         4        2        0
//	obsidian   stdio      not connected     —        3        1   exec: "npx" not found
//
// TOOLS shows "—" for a server that isn't connected (Tools is -1). The last
// field is the reason a server isn't connected, or else the URL of an HTTP
// server without its scheme. With no rows it returns one line that says
// how to add a server.
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
		out = append(out, line(r.Name, r.Transport, r.State, tools,
			fmt.Sprint(r.Allowed), fmt.Sprint(r.Confirm), last))
	}
	return out
}

// oneLine folds s onto one line, so a reason with a line break keeps the
// table's shape.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mcpBox is the open /mcp box. It opens at once with loading set, and the
// next status reply fills in rows or err.
type mcpBox struct {
	loading bool
	rows    []rpc.MCPStatus
	err     string // why merud sent no rows
}

// mcpMsg reports what merud said to OpMCPStatus. err says why no rows
// came, such as an older merud that doesn't know the op.
type mcpMsg struct {
	rows []rpc.MCPStatus
	err  error
}

// mcpCmd returns a command that asks merud once for its MCP status.
func mcpCmd(ask askFunc) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		defer cancel()
		var rows []rpc.MCPStatus
		for ev, err := range ask(ctx, rpc.Request{Op: rpc.OpMCPStatus}, nil) {
			if err != nil {
				return mcpMsg{err: err}
			}
			switch ev.Type {
			case rpc.EventMCPStatus:
				rows = ev.MCP
			case rpc.EventDone:
				return mcpMsg{rows: rows}
			case rpc.EventError:
				return mcpMsg{err: errors.New(ev.Error)}
			}
		}
		return mcpMsg{err: errors.New("merud sent no reply")}
	}
}

// applyMCP fills a waiting /mcp box with merud's reply. A reply that comes
// after the box closed, or after it already shows something, changes
// nothing.
func (m *Model) applyMCP(msg mcpMsg) {
	b := m.mcpBox
	if b == nil || !b.loading {
		return
	}
	b.loading = false
	b.rows = msg.rows
	if msg.err != nil {
		b.err = msg.err.Error()
	}
}

// mcpBoxView draws the /mcp box for a pane width columns wide and height
// rows tall (see boxPane): the MCPTable lines, cut with "…" where the pane
// is too narrow for them.
func (m *Model) mcpBoxView(width, height int) string {
	b := m.mcpBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = []string{"merud gave no MCP status: " + b.err}
	default:
		body = MCPTable(b.rows)
	}
	return m.boxPane("MCP servers", body, mcpNote, width, height)
}
