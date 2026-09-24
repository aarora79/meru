// This file tests the MCP backend against a real MCP server over
// Streamable HTTP on 127.0.0.1, and the conversion of [[mcp.servers]]
// entries, secrets included.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/mcp"
	"github.com/aarora79/meru/internal/rpc"
)

// echoArgs is the argument object of the test server's tools.
type echoArgs struct {
	Text string `json:"text"`
}

// startMCPServer runs an MCP server with three tools (echo, fail and
// delete, which carries a destructive hint) over Streamable HTTP, and
// stops it when the test ends. It returns the server's URL.
func startMCPServer(t *testing.T) string {
	t.Helper()
	url, _ := startMCPServerAuth(t)
	return url
}

// startMCPServerAuth is startMCPServer that also returns a function that
// reports the last Authorization header the server received.
func startMCPServerAuth(t *testing.T) (url string, lastAuth func() string) {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "echo", Description: "Say it back."},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}}, nil, nil
		})
	sdk.AddTool(srv, &sdk.Tool{Name: "fail", Description: "Always fails."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ echoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "nope"}}}, nil, nil
		})
	yes := true
	sdk.AddTool(srv, &sdk.Tool{Name: "delete", Description: "Delete a thing.",
		Annotations: &sdk.ToolAnnotations{DestructiveHint: &yes}},
		func(_ context.Context, _ *sdk.CallToolRequest, _ echoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "deleted"}}}, nil, nil
		})
	var mu sync.Mutex // guards auth
	var auth string
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp", func() string {
		mu.Lock()
		defer mu.Unlock()
		return auth
	}
}

func TestMCPBackend(t *testing.T) {
	url := startMCPServer(t)
	pool, err := mcp.NewPool(context.Background(), []mcp.ServerConfig{{
		Name: "files", URL: url,
		Allow:         []string{"echo", "fail", "delete", "missing"},
		Confirm:       []string{"delete"},
		AlwaysConfirm: []string{"fail"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	b := mcpBackend{pool: pool}

	if b.Kind() != dispatch.KindMCP {
		t.Errorf("Kind() = %q", b.Kind())
	}
	if server, tool := b.Locate("files.read.v2"); server != "files" || tool != "read.v2" {
		t.Errorf("Locate = %q, %q; want files, read.v2", server, tool)
	}
	if b.Confirm("files.delete") != dispatch.ConfirmAsk || b.Confirm("files.echo") != dispatch.ConfirmNever {
		t.Error("Confirm doesn't follow the confirm list")
	}
	if b.Confirm("files.fail") != dispatch.ConfirmAlways {
		t.Error("a tool in always_confirm should ask every time, with no session approval")
	}

	calls := []struct {
		tool    string
		text    string
		isError bool
	}{
		{"files.echo", "hi", false},
		{"files.fail", "nope", true},
	}
	for _, c := range calls {
		res, err := b.Call(context.Background(), c.tool, json.RawMessage(`{"text":"hi"}`))
		if err != nil || res.Text != c.text || res.IsError != c.isError {
			t.Errorf("Call(%s) = %+v, %v; want %q, IsError %v", c.tool, res, err, c.text, c.isError)
		}
	}
	if _, err := b.Call(context.Background(), "files.secret", nil); !errors.Is(err, mcp.ErrNotAllowed) {
		t.Errorf("Call of a tool outside allow = %v, want ErrNotAllowed", err)
	}

	st := b.Status()
	if len(st) != 1 {
		t.Fatalf("Status() has %d servers, want 1", len(st))
	}
	s := st[0]
	if s.Name != "files" || s.Kind != "mcp" || s.Transport != "http" || !s.Connected || s.Offered != 3 {
		t.Errorf("Status() = %+v", s)
	}
	if strings.Join(s.Unknown, ",") != "missing" {
		t.Errorf("Unknown = %v, want [missing]", s.Unknown)
	}
	var names []string
	for _, tool := range s.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
		if tool.Confirm != (tool.Name == "files.delete" || tool.Name == "files.fail") {
			t.Errorf("tool %s Confirm = %v", tool.Name, tool.Confirm)
		}
		if tool.AlwaysAsks != (tool.Name == "files.fail") {
			t.Errorf("tool %s AlwaysAsks = %v", tool.Name, tool.AlwaysAsks)
		}
	}
	if got := strings.Join(names, ","); got != "files.delete,files.echo,files.fail" {
		t.Errorf("Tools = %s", got)
	}
}

func TestMCPServerConfigs(t *testing.T) {
	// resolve stands in for the secrets package: it knows one secret.
	resolve := func(v string) (string, error) {
		name, ok := strings.CutPrefix(v, "secret:")
		if !ok {
			return v, nil
		}
		if name == "brave" {
			return "sk-123", nil
		}
		return "", errors.New("no secret named " + name)
	}

	tests := []struct {
		name    string
		servers []config.MCPServer
		wantErr string // "" means no error
		check   func(t *testing.T, got []mcp.ServerConfig)
	}{
		{
			name: "stdio with a secret in env",
			servers: []config.MCPServer{{Name: "search", Command: "brave-mcp", Args: []string{"--stdio"},
				Env: map[string]string{"BRAVE_KEY": "secret:brave", "MODE": "plain"}, Allow: []string{"web_search"},
				Timeout: "90s"}},
			check: func(t *testing.T, got []mcp.ServerConfig) {
				c := got[0]
				if c.Env["BRAVE_KEY"] != "sk-123" || c.Env["MODE"] != "plain" {
					t.Errorf("Env = %v", c.Env)
				}
				if c.Timeout != 90*time.Second || c.Command != "brave-mcp" || c.Args[0] != "--stdio" {
					t.Errorf("config = %+v", c)
				}
			},
		},
		{
			name: "http with a secret header",
			servers: []config.MCPServer{{Name: "cal", URL: "http://127.0.0.1:8123/mcp",
				Headers: map[string]string{"Authorization": "secret:brave"}, Allow: []string{"list"}, Confirm: []string{"list"}}},
			check: func(t *testing.T, got []mcp.ServerConfig) {
				c := got[0]
				if c.Headers["Authorization"] != "sk-123" || c.Timeout != 0 || c.Confirm[0] != "list" {
					t.Errorf("config = %+v", c)
				}
			},
		},
		{
			name:    "unknown secret",
			servers: []config.MCPServer{{Name: "x", Command: "x", Env: map[string]string{"K": "secret:gone"}}},
			wantErr: `"K": no secret named gone`,
		},
		{
			name:    "bad timeout",
			servers: []config.MCPServer{{Name: "x", Command: "x", Timeout: "soon"}},
			wantErr: "timeout",
		},
		{
			name:    "the pool's rules apply",
			servers: []config.MCPServer{{Name: "x", URL: "https://mcp.example.com/mcp"}},
			wantErr: "remote = true",
		},
		{
			name:    "no servers",
			servers: nil,
			check: func(t *testing.T, got []mcp.ServerConfig) {
				if len(got) != 0 {
					t.Errorf("got %d configs, want none", len(got))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mcpServerConfigs(tt.servers, resolve)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "sk-123") {
					t.Errorf("error holds a secret value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("mcpServerConfigs: %v", err)
			}
			tt.check(t, got)
		})
	}
}

// TestMCPStatusOp checks the mcp_status reply against a pool with one
// server up and one down. The rows join config with the pool's state, and
// building them sends no request to either server.
func TestMCPStatusOp(t *testing.T) {
	upURL := startMCPServer(t)
	// count counts the requests the down server receives. It answers 503, as a
	// server that isn't ready would.
	var mu sync.Mutex // guards count
	count := 0
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	pool, err := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "files", URL: upURL, Allow: []string{"echo", "delete"}, Confirm: []string{"delete"}},
		{Name: "google", URL: down.URL + "/mcp", Allow: []string{"a", "b", "c"}, Confirm: []string{"c"}, AlwaysConfirm: []string{"b"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mu.Lock()
	before := count
	mu.Unlock()

	s := &toolService{pool: pool}
	var events []rpc.Event
	if err := s.handleMCPStatus(func(ev rpc.Event) error { events = append(events, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != rpc.EventMCPStatus {
		t.Fatalf("events = %+v, want one mcp_status", events)
	}
	rows := events[0].MCP
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	up := rpc.MCPStatus{Name: "files", Transport: "http", State: "connected", URL: upURL, Tools: 3, Allowed: 2, Confirm: 1}
	if rows[0] != up {
		t.Errorf("row 1 = %+v, want %+v", rows[0], up)
	}
	got := rows[1]
	if got.Name != "google" || got.State != "not connected" || got.Tools != -1 || got.Allowed != 3 || got.Confirm != 2 || got.Err == "" {
		t.Errorf("row 2 = %+v, want not connected, tools -1, 3 allowed, 2 confirm, a reason", got)
	}
	mu.Lock()
	after := count
	mu.Unlock()
	if after != before {
		t.Errorf("the status op sent %d requests to the down server, want none", after-before)
	}
}
