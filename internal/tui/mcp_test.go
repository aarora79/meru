// This file tests the MCP status table and the /mcp box: the table's exact
// layout, the command that opens the box, and the replies that fill it.

package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// mcpFixture is the two catalog servers and one added by hand, in the
// three shapes a row takes: an HTTP server that is connected, a stdio
// server that is connected, and one that isn't.
var mcpFixture = []rpc.MCPStatus{
	{Name: "google", Transport: "http", State: "connected", URL: "http://127.0.0.1:8000/mcp", Tools: 124, Allowed: 6, Confirm: 2},
	{Name: "obsidian", Transport: "stdio", State: "connected", Tools: 13, Allowed: 5, Confirm: 1},
	{Name: "notes", Transport: "stdio", State: "not connected", Tools: -1, Allowed: 3, Confirm: 1, Err: `exec: "npx" not found`},
}

func TestMCPTable(t *testing.T) {
	tests := []struct {
		name string
		rows []rpc.MCPStatus
		want []string
	}{
		{"three servers", mcpFixture, []string{
			"SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM",
			"google     http       connected       124        6        2   127.0.0.1:8000/mcp",
			"obsidian   stdio      connected        13        5        1",
			`notes      stdio      not connected     —        3        1   exec: "npx" not found`,
		}},
		{"a down http server shows the reason, not the url", []rpc.MCPStatus{
			{Name: "google", Transport: "http", State: "not connected", URL: "http://127.0.0.1:8000/mcp", Tools: -1, Allowed: 6, Confirm: 2,
				Err: "connect: dial tcp 127.0.0.1:8000:\nconnection refused"},
		}, []string{
			"SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM",
			"google     http       not connected     —        6        2   connect: dial tcp 127.0.0.1:8000: connection refused",
		}},
		{"a long name widens the column", []rpc.MCPStatus{
			{Name: "google-workspace", Transport: "http", State: "connected", URL: "https://mcp.example.com/mcp", Tools: 0},
		}, []string{
			"SERVER           TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM",
			"google-workspace http       connected         0        0        0   mcp.example.com/mcp",
		}},
		{"no servers", nil, []string{noServers}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MCPTable(tt.rows)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("MCPTable =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestSlashMCPOpensBox(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventMCPStatus, MCP: mcpFixture}, {Type: rpc.EventDone}}}
	m, cmd := update(t, testModel(merud.ask, newFakeSender()), typeText("/mcp"), press(tea.KeyEnter))
	if m.mcpBox == nil || !m.mcpBox.loading {
		t.Fatalf("mcp box = %+v after /mcp, want it open and waiting", m.mcpBox)
	}
	if cmd == nil {
		t.Fatal("/mcp returned no command, want the status request")
	}
	m, _ = update(t, m, cmd())
	if len(merud.reqs) != 1 || merud.reqs[0].Op != rpc.OpMCPStatus {
		t.Errorf("requests = %+v, want one mcp_status", merud.reqs)
	}
	if len(m.turns) != 0 || m.input.Value() != "" {
		t.Errorf("turns = %d, input = %q; /mcp must not ask the model", len(m.turns), m.input.Value())
	}
	if b := m.mcpBox; b == nil || b.loading || len(b.rows) != 3 {
		t.Fatalf("mcp box = %+v, want the three rows", b)
	}
	view := m.View()
	for _, want := range []string{"MCP servers", "SERVER", "obsidian", "not connected", "esc/q close"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	// A second reply changes nothing, and q closes the box.
	m, _ = update(t, m, mcpMsg{err: errors.New("late")})
	if m.mcpBox.err != "" {
		t.Errorf("a late reply set err %q", m.mcpBox.err)
	}
	m, _ = update(t, m, typeText("q"))
	if m.mcpBox != nil {
		t.Error("q didn't close the /mcp box")
	}
}

func TestMCPBoxShowsError(t *testing.T) {
	merud := &fakeMerud{}
	m, _ := update(t, testModel(merud.ask, newFakeSender()), typeText("/mcp"), press(tea.KeyEnter),
		mcpMsg{err: errors.New(`unknown op "mcp_status"`)})
	if m.mcpBox == nil || m.mcpBox.loading || !strings.Contains(m.mcpBox.err, "unknown op") {
		t.Errorf("box = %+v, want the error shown", m.mcpBox)
	}
}
