// This file tests how merud joins the connectors to the MCP pool: which
// connectors get a pool entry, the rule that a server added by hand wins,
// what the connectors op reports, and the rule that picks the SearXNG
// connector's mode from config. No test here installs or starts a
// connector: each config leaves Obsidian off, set up by hand, or short of
// its vault folder, and leaves SearXNG off or only checked, so no test
// runs docker.

package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// testConnectors returns merud's connectors over a temporary Meru home,
// closed when the test ends.
func testConnectors(t *testing.T) *connectorSet {
	t.Helper()
	c, err := newConnectorSet(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// obsidianRow returns the connectors op's row for obsidian.
func obsidianRow(t *testing.T, c *connectorSet) rpc.ConnectorStatus {
	t.Helper()
	for _, st := range c.statuses() {
		if st.ID == "obsidian" {
			return st
		}
	}
	t.Fatal("no obsidian connector")
	return rpc.ConnectorStatus{}
}

// TestConnectorsJoinThePool checks each case of the join, table-driven.
// The owner's own setup, an [[mcp.servers]] entry named obsidian started
// with npx and no [connectors.obsidian] table, must stay exactly as it
// is: the entry runs, and the connector only reports "set up by hand".
func TestConnectorsJoinThePool(t *testing.T) {
	npx := config.MCPServer{Name: "obsidian", Command: "npx", Args: []string{"-y", "obsidian-mcp", "serve"},
		Allow: []string{"obsidian_read_note"}}
	tests := []struct {
		name    string
		cfg     config.Config
		state   string
		managed bool // obsidian gets a managed pool entry
	}{
		{"no table: off", config.Config{}, rpc.ConnectorOff, false},
		{"hand-added, no table: by hand", config.Config{MCP: config.MCP{Servers: []config.MCPServer{npx}}}, rpc.ConnectorByHand, false},
		{"hand-added and turned on: the hand-added one wins", config.Config{
			MCP:        config.MCP{Servers: []config.MCPServer{npx}},
			Connectors: map[string]config.Connector{"obsidian": {"enabled": true, "vault_path": t.TempDir()}},
		}, rpc.ConnectorByHand, false},
		{"turned on without a vault: needs config", config.Config{
			Connectors: map[string]config.Connector{"obsidian": {"enabled": true}},
		}, rpc.ConnectorNeedsConfig, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConnectors(t)
			c.configure(tt.cfg, &secrets.Secrets{})
			row := obsidianRow(t, c)
			if row.State != tt.state {
				t.Errorf("state = %s %q, want %s", row.State, row.Sentence, tt.state)
			}
			var managed []string
			for _, sc := range c.serverConfigs() {
				managed = append(managed, sc.Name)
				if sc.Spawn == nil || sc.Command != "" {
					t.Errorf("pool entry %s isn't a managed one: %+v", sc.Name, sc)
				}
			}
			if got := slices.Contains(managed, "obsidian"); got != tt.managed {
				t.Errorf("managed pool entries = %v, want obsidian %v", managed, tt.managed)
			}

			// The hand-added entry reaches the pool unchanged, and the pool
			// holds one obsidian at most.
			hand, err := mcpServerConfigs(tt.cfg.MCP.Servers, (&secrets.Secrets{}).Resolve)
			if err != nil {
				t.Fatal(err)
			}
			for _, sc := range hand {
				if sc.Spawn != nil || sc.Command != "npx" || !slices.Equal(sc.Args, npx.Args) {
					t.Errorf("the hand-added entry changed: %+v", sc)
				}
			}
			// Exactly one obsidian reaches the pool, the router and its
			// prompt, unless the connector is off with no entry at all.
			want := 1
			if tt.state == rpc.ConnectorOff {
				want = 0
			}
			entries := 0
			for _, sc := range append(hand, c.serverConfigs()...) {
				if sc.Name == "obsidian" {
					entries++
				}
			}
			named := 0
			for _, s := range agent.ConnectedTools(c.routerServers(tt.cfg)) {
				if strings.HasPrefix(s, "obsidian") {
					named++
				}
			}
			if entries != want || named != want {
				t.Errorf("obsidian: %d pool entries and %d router names, want %d of each", entries, named, want)
			}
		})
	}
}

// TestConnectorsOp checks the connectors op's reply for a connector that
// needs config: the sentence, the field to fix, and the fields with their
// values.
func TestConnectorsOp(t *testing.T) {
	c := testConnectors(t)
	c.configure(config.Config{Connectors: map[string]config.Connector{"obsidian": {"enabled": true, "vault_name": "notes"}}},
		&secrets.Secrets{})
	s := &toolService{conns: c}
	ollama, err := newOllamaWatch(config.Config{Ollama: config.Ollama{BaseURL: "http://127.0.0.1:11434"}}, &fakeEngine{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	var got []rpc.Event
	if err := s.handleConnectors(ollama, func(ev rpc.Event) error { got = append(got, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != rpc.EventConnectors {
		t.Fatalf("events = %+v", got)
	}
	var ids []string
	for _, row := range got[0].Connectors {
		ids = append(ids, row.ID)
	}
	if !slices.Equal(ids, []string{"obsidian", "ollama", "searxng"}) {
		t.Errorf("connectors = %v, want obsidian, ollama and searxng in manifest order", ids)
	}
	row := obsidianRow(t, c)
	if row.Name != "Obsidian" || row.Kind != "stdio" || row.Sentence != "Obsidian needs your vault folder." ||
		!slices.Equal(row.Fix, []string{"vault_path"}) {
		t.Errorf("row = %+v", row)
	}
	if len(row.Fields) != 2 || row.Fields[0].ID != "vault_path" || row.Fields[1].Value != "notes" {
		t.Errorf("fields = %+v", row.Fields)
	}
}

// closedURL returns a loopback URL where nothing listens: a port the
// system handed out and took back.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

// TestWebConnectorRule checks the SearXNG connector against the config
// rule: a config from before the connectors, with web_search listed and
// no [connectors.searxng] table, only has its URL checked; a SearXNG
// already there is used as it is; and web_search goes to the model only
// while the connector is ok. None of these configs lets Meru run docker.
func TestWebConnectorRule(t *testing.T) {
	// A fake SearXNG that refuses the empty query in JSON, as a real one
	// with JSON on does.
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "No query"}`))
	}))
	defer searx.Close()
	down := closedURL(t)
	listed := []string{"web_search", "web_fetch"}

	tests := []struct {
		name     string
		cfg      config.Config
		state    string
		sentence string
	}{
		{"old config, SearXNG answers: used as it is", config.Config{
			Web: config.Web{SearXNGURL: searx.URL}, Builtin: config.Builtin{Tools: listed},
		}, rpc.ConnectorOK, "Web search uses the SearXNG already running at " + searx.URL + "."},
		{"old config, nothing answers: says so", config.Config{
			Web: config.Web{SearXNGURL: down}, Builtin: config.Builtin{Tools: listed},
		}, rpc.ConnectorNeedsConfig, "Web search can't use SearXNG at " + down + ": nothing answers there."},
		{"web_search not listed: off", config.Config{
			Web: config.Web{SearXNGURL: searx.URL}, Builtin: config.Builtin{Tools: []string{"web_fetch"}},
		}, rpc.ConnectorOff, "Web search is off."},
		{"turned off: off", config.Config{
			Web: config.Web{SearXNGURL: searx.URL}, Builtin: config.Builtin{Tools: listed},
			Connectors: map[string]config.Connector{"searxng": {"enabled": false}},
		}, rpc.ConnectorOff, "Web search is off."},
		{"no URL: off", config.Config{Builtin: config.Builtin{Tools: listed}}, rpc.ConnectorOff, "Web search is off."},
		{"turned on at another address: checked, not run", config.Config{
			Web: config.Web{SearXNGURL: searx.URL}, Builtin: config.Builtin{Tools: listed},
			Connectors: map[string]config.Connector{"searxng": {"enabled": true}},
		}, rpc.ConnectorOK, "Web search uses the SearXNG already running at " + searx.URL + "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConnectors(t)
			c.configure(tt.cfg, &secrets.Secrets{})
			deadline := time.Now().Add(5 * time.Second)
			var row connectors.Status
			for {
				row = c.web.Status()
				if row.State == tt.state && row.Sentence == tt.sentence {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("searxng = %s %q, want %s %q", row.State, row.Sentence, tt.state, tt.sentence)
				}
				time.Sleep(5 * time.Millisecond)
			}
			ok, sentence := c.webOK()
			if ok != (tt.state == rpc.ConnectorOK) || sentence != tt.sentence {
				t.Errorf("webOK = %v, %q", ok, sentence)
			}
			// The Settings card shows the same state and sentence.
			conn := builtinConnection(tt.cfg, c.web.Status())
			if conn.Web != tt.state || conn.WebSentence != tt.sentence {
				t.Errorf("builtin connection web = %s %q", conn.Web, conn.WebSentence)
			}
		})
	}
}
