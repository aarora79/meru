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
		{"a connector shows its own state and sentence", []rpc.MCPStatus{
			{Name: "obsidian", Transport: "stdio", State: "connected", Connector: rpc.ConnectorOK, Tools: 3, Allowed: 3,
				Sentence: "Obsidian is ready. It starts when a question needs it."},
			{Name: "notes", Transport: "stdio", State: "not connected", Connector: rpc.ConnectorNeedsConfig, Tools: -1, Allowed: 3,
				Err: "Notes needs your folder.", Sentence: "Notes needs your folder."},
		}, []string{
			"SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM",
			"obsidian   stdio      ok                3        3        0   Obsidian is ready. It starts when a question needs it.",
			"notes      stdio      needs config      —        3        0   Notes needs your folder.",
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

// TestConnectorTable checks the connector lines `meru mcp` prints under
// its table, with where to set a field the connector needs.
func TestConnectorTable(t *testing.T) {
	got := ConnectorTable([]rpc.ConnectorStatus{
		{ID: "obsidian", State: rpc.ConnectorNeedsConfig, Sentence: "Obsidian needs your vault folder.", Fix: []string{"vault_path"}},
		{ID: "notes", State: rpc.ConnectorByHand, Sentence: "Notes is set up by hand, as the notes entry in [[mcp.servers]]."},
		{ID: "google", State: rpc.ConnectorNeedsConfig, Sentence: "Google needs you to sign in.", Link: "https://accounts.example.test/o/oauth2/auth?client_id=x"},
	})
	want := []string{
		"CONNECTOR  STATE",
		"obsidian   needs config    Obsidian needs your vault folder. Run meru mcp fix obsidian to set vault_path.",
		"notes      set up by hand  Notes is set up by hand, as the notes entry in [[mcp.servers]].",
		"google     needs config    Google needs you to sign in. Sign in: https://accounts.example.test/o/oauth2/auth?client_id=x",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ConnectorTable =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ConnectorTable(nil) != nil {
		t.Error("no connectors should give no lines")
	}
}

// TestConnHeadingForConnectors checks the /mcp box's heading for a
// connector merud runs and for a server set up by hand in its place.
func TestConnHeadingForConnectors(t *testing.T) {
	managed := rpc.Connection{Name: "obsidian", Kind: "mcp", Transport: "stdio", State: rpc.MCPNotConnected,
		Connector: rpc.ConnectorNeedsConfig, Sentence: "Obsidian needs your vault folder.", Fix: []string{"vault_path"},
		Tools: []rpc.ToolPolicy{{Name: "obsidian_read_note", Policy: rpc.PolicyAllow}}}
	// The fix goes on the connector's own row, where f asks for it.
	want := "obsidian · connector, stdio · needs config · 1 of 1 tools on · Obsidian needs your vault folder."
	if got := connHeading(managed); got != want {
		t.Errorf("connHeading =\n%s\nwant\n%s", got, want)
	}
	hand := rpc.Connection{Name: "obsidian", Kind: "mcp", Transport: "stdio", State: rpc.MCPConnected, Connector: rpc.ConnectorByHand}
	if got := connHeading(hand); !strings.HasSuffix(got, "· connected · 0 of 0 tools on · set up by hand") {
		t.Errorf("connHeading = %q", got)
	}
	// With merud's sentence, the heading says how to adopt it.
	hand.Sentence = "Obsidian is set up by hand, as the obsidian entry in [[mcp.servers]]. To have Meru run it, run meru mcp adopt obsidian."
	if got := connHeading(hand); !strings.HasSuffix(got, "· set up by hand · To have Meru run it, run meru mcp adopt obsidian.") {
		t.Errorf("connHeading = %q", got)
	}
	// A connector that waits for a sign-in shows the link.
	google := rpc.Connection{Name: "google", Kind: "mcp", Transport: "http", State: rpc.MCPNotConnected,
		Connector: rpc.ConnectorNeedsConfig, Sentence: "Google needs you to sign in.", Link: "https://accounts.example.test/o/oauth2/auth?client_id=x"}
	if got := connHeading(google); !strings.HasSuffix(got, "Google needs you to sign in. Sign in: https://accounts.example.test/o/oauth2/auth?client_id=x") {
		t.Errorf("connHeading = %q", got)
	}
	// The built-in tools' heading carries web search's state.
	own := rpc.Connection{Name: "meru", Kind: "builtin", State: rpc.MCPConnected,
		Web: rpc.ConnectorNeedsConfig, WebSentence: "Web search can't start: Docker isn't running.",
		Tools: []rpc.ToolPolicy{{Name: "web_search", Policy: rpc.PolicyAllow}}}
	if got, want := connHeading(own), "meru · built in · 1 of 1 tools on · web search needs config: Web search can't start: Docker isn't running."; got != want {
		t.Errorf("connHeading =\n%s\nwant\n%s", got, want)
	}
}

// connsFixture is merud's answer to OpConnections: merud's own tools, an
// MCP server that is connected, one that isn't, and the local commands,
// whose policies config.toml fixes.
var connsFixture = []rpc.Connection{
	{Name: "meru", Kind: "builtin", State: rpc.MCPConnected, Offered: 3, Tools: []rpc.ToolPolicy{
		{Name: "configure", Policy: rpc.PolicyAlways}, {Name: "web_search", Policy: rpc.PolicyAllow}, {Name: "write_file", Policy: rpc.PolicyAsk}}},
	{Name: "google", Kind: "mcp", Transport: "http", State: rpc.MCPConnected, Offered: 124, Tools: []rpc.ToolPolicy{
		{Name: "search_gmail_messages", Policy: rpc.PolicyAllow}, {Name: "send_gmail_message", Policy: rpc.PolicyAsk}, {Name: "list_calendars", Policy: rpc.PolicyOff}}},
	{Name: "notes", Kind: "mcp", Transport: "stdio", State: rpc.MCPNotConnected, Err: `exec: "npx" not found`, Offered: -1, Tools: []rpc.ToolPolicy{
		{Name: "search", Policy: rpc.PolicyAllow}}},
	{Name: "commands", Kind: "command", State: rpc.MCPConnected, Fixed: true, Note: "Edit [[commands]] in config.toml to change these.", Tools: []rpc.ToolPolicy{
		{Name: "backup", Policy: rpc.PolicyAsk}}},
}

// catalogFixture is the catalog: one server added already, one that needs
// an API key, and one that needs nothing.
var catalogFixture = []rpc.CatalogEntry{
	{Name: "google", Title: "Google Workspace", Added: true},
	{Name: "search", Title: "Brave Search", Needs: []rpc.CatalogNeed{{Kind: "api_key", Prompt: "Brave Search API key", Secret: "brave_api_key"}}},
	{Name: "obsidian", Title: "Obsidian notes"},
}

// connsReply is the reply that fills a waiting /mcp box.
var connsReply = replyMsg{tag: tagConns, ev: rpc.Event{Type: rpc.EventConnections, Connections: connsFixture, Catalog: catalogFixture}}

// openMCP opens the /mcp box against merud and fills it with connsReply.
func openMCP(t *testing.T, merud *fakeMerud) Model {
	t.Helper()
	m, cmd := update(t, testModel(merud.ask, newFakeSender()), typeText("/mcp"), press(tea.KeyEnter))
	if m.mcpBox == nil || !m.mcpBox.loading || cmd == nil {
		t.Fatalf("mcp box = %+v after /mcp, want it open and asking", m.mcpBox)
	}
	m, _ = update(t, m, connsReply)
	return m
}

func TestSlashMCPOpensBox(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventConnections, Connections: connsFixture}, {Type: rpc.EventDone}}}
	// A tall screen shows every source without scrolling.
	m, cmd := update(t, testModel(merud.ask, newFakeSender()), tea.WindowSizeMsg{Width: 80, Height: 40}, typeText("/mcp"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if len(merud.reqs) != 1 || merud.reqs[0].Op != rpc.OpConnections {
		t.Errorf("requests = %+v, want one connections", merud.reqs)
	}
	if len(m.turns) != 0 || m.input.Value() != "" {
		t.Errorf("turns = %d, input = %q; /mcp must not ask the model", len(m.turns), m.input.Value())
	}
	if b := m.mcpBox; b == nil || b.loading || len(b.conns) != 4 {
		t.Fatalf("mcp box = %+v, want the four sources", b)
	}
	view := m.View()
	for _, want := range []string{"Connections", "google · MCP server, http · connected · 2 of 124 tools on", "Ask", "not connected", "esc/q close"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	m, _ = update(t, m, typeText("q"))
	if m.mcpBox != nil {
		t.Error("q didn't close the /mcp box")
	}
}

func TestMCPBoxShowsError(t *testing.T) {
	merud := &fakeMerud{}
	m, _ := update(t, testModel(merud.ask, newFakeSender()), typeText("/mcp"), press(tea.KeyEnter),
		replyMsg{tag: tagConns, err: errors.New(`unknown op "connections"`)})
	if m.mcpBox == nil || m.mcpBox.loading || !strings.Contains(m.mcpBox.err, "unknown op") {
		t.Errorf("box = %+v, want the error shown", m.mcpBox)
	}
}

// TestMCPPolicyKeys checks the policy keys: → and ← step the marked tool
// through Off, Ask and Allow and send the change to merud; a tool that
// always asks never reaches Allow; the local commands take no change.
func TestMCPPolicyKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.Msg
		want *rpc.PolicyChange // nil: nothing sent
	}{
		// The marker starts on configure, merud's first tool.
		{"configure stays below allow", []tea.Msg{press(tea.KeyRight)}, nil},
		{"configure goes off", []tea.Msg{press(tea.KeyLeft)}, &rpc.PolicyChange{Kind: "builtin", Server: "meru", Tool: "configure", Policy: rpc.PolicyOff}},
		{"web_search can't go past allow", []tea.Msg{press(tea.KeyDown), press(tea.KeyRight)}, nil},
		{"write_file to allow", []tea.Msg{press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyRight)},
			&rpc.PolicyChange{Kind: "builtin", Server: "meru", Tool: "write_file", Policy: rpc.PolicyAllow}},
		// Down past merud's three tools lands on google's heading, then
		// its tools.
		{"send_gmail_message to off", []tea.Msg{press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyLeft)},
			&rpc.PolicyChange{Kind: "mcp", Server: "google", Tool: "send_gmail_message", Policy: rpc.PolicyOff}},
		{"a heading takes no policy", []tea.Msg{press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyRight)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventConnections, Connections: connsFixture}}}
			m := openMCP(t, merud)
			var cmd tea.Cmd
			for _, k := range tt.keys {
				m, cmd = update(t, m, k)
			}
			if tt.want == nil {
				if cmd != nil {
					t.Fatalf("keys sent a change; want none")
				}
				return
			}
			if cmd == nil || m.mcpBox.busy == "" {
				t.Fatalf("keys sent nothing; want %+v", tt.want)
			}
			m, _ = update(t, m, cmd())
			got := merud.reqs[len(merud.reqs)-1]
			if got.Op != rpc.OpToolPolicy || got.Policy == nil || *got.Policy != *tt.want {
				t.Errorf("request = %+v, want tool_policy %+v", got, tt.want)
			}
			if m.mcpBox.busy != "" || m.notice != "saved to config.toml" {
				t.Errorf("busy = %q, notice = %q after the reply", m.mcpBox.busy, m.notice)
			}
		})
	}
}

// TestMCPAddAndRemove checks Enter on a catalog server, the key field for
// one that needs an API key, and d d on a server.
func TestMCPAddAndRemove(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventConnections, Connections: connsFixture}, {Type: rpc.EventDone}}}
	m := openMCP(t, merud)
	n := m.mcpBox.actCount()
	// End marks obsidian, the last catalog server, which needs nothing.
	m, cmd := update(t, m, press(tea.KeyEnd), press(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("enter on obsidian sent nothing")
	}
	m, _ = update(t, m, cmd())
	if got := merud.reqs[len(merud.reqs)-1]; got.Op != rpc.OpMCPAdd || got.ID != "obsidian" {
		t.Errorf("request = %+v, want mcp_add obsidian", got)
	}
	if !strings.Contains(m.notice, "added obsidian") {
		t.Errorf("notice = %q", m.notice)
	}

	// Search needs a key: Enter opens the field, q types into it rather
	// than closing the box, and Enter sends the key, then the add.
	m, _ = update(t, openMCP(t, merud), press(tea.KeyEnd), press(tea.KeyUp), press(tea.KeyEnter))
	if m.mcpBox.keyFor == nil || m.mcpBox.keyFor.Name != "search" {
		t.Fatalf("key field = %+v, want it open for search", m.mcpBox.keyFor)
	}
	m, _ = update(t, m, typeText("q"), typeText("k-123"))
	if m.mcpBox == nil || m.mcpBox.key.Value() != "qk-123" {
		t.Fatalf("typing went elsewhere: box %+v", m.mcpBox)
	}
	if strings.Contains(m.View(), "qk-123") {
		t.Error("the view shows the key")
	}
	before := len(merud.reqs)
	m, cmd = update(t, m, press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	reqs := merud.reqs[before:]
	if len(reqs) != 2 || reqs[0].Op != rpc.OpSecretSet || reqs[0].ID != "brave_api_key" || reqs[0].Text != "qk-123" ||
		reqs[1].Op != rpc.OpMCPAdd || reqs[1].ID != "search" {
		t.Errorf("requests = %+v, want secret_set then mcp_add", reqs)
	}

	// d on google's heading asks first, and a second d removes it.
	m = openMCP(t, merud)
	m, _ = update(t, m, press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown))
	m, cmd = update(t, m, typeText("d"))
	if cmd != nil || m.mcpBox.confirm != "google" || !strings.Contains(m.View(), "Press d again") {
		t.Fatalf("first d: cmd %v, confirm %q", cmd != nil, m.mcpBox.confirm)
	}
	m, cmd = update(t, m, typeText("d"))
	if cmd == nil {
		t.Fatal("second d sent nothing")
	}
	m, _ = update(t, m, cmd())
	if got := merud.reqs[len(merud.reqs)-1]; got.Op != rpc.OpMCPRemove || got.ID != "google" {
		t.Errorf("request = %+v, want mcp_remove google", got)
	}
	if n == 0 {
		t.Error("no row takes the marker")
	}
}
