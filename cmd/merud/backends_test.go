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
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/mcp"
)

// echoArgs is the argument object of the test server's tools.
type echoArgs struct {
	Text string `json:"text"`
}

// startMCPServer runs an MCP server with three tools (echo, fail and
// delete) over Streamable HTTP, and stops it when the test ends. It
// returns the server's URL.
func startMCPServer(t *testing.T) string {
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
	sdk.AddTool(srv, &sdk.Tool{Name: "delete", Description: "Delete a thing."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ echoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "deleted"}}}, nil, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func TestMCPBackend(t *testing.T) {
	url := startMCPServer(t)
	pool, err := mcp.NewPool(context.Background(), []mcp.ServerConfig{{
		Name: "files", URL: url,
		Allow:   []string{"echo", "fail", "delete", "missing"},
		Confirm: []string{"delete"},
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
		if tool.Confirm != (tool.Name == "files.delete") {
			t.Errorf("tool %s Confirm = %v", tool.Name, tool.Confirm)
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
			wantErr: "network = true",
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
