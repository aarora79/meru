// This file tests Probe against the test server over stdio and Streamable
// HTTP: the tool list, the hints, and the configs it refuses.

package mcp

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProbe(t *testing.T) {
	configs := []struct {
		name string
		cfg  func(t *testing.T) ServerConfig
	}{
		{"stdio", func(t *testing.T) ServerConfig { return stdioConfig(t, "t") }},
		{"http", func(t *testing.T) ServerConfig { return httpConfig(t, "t") }},
	}
	for _, tc := range configs {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg(t)
			// Probe needs no allow list, and ignores a bad one.
			cfg.Allow, cfg.Confirm = []string{"*"}, []string{"nope"}
			info, err := Probe(context.Background(), cfg, nil)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if info.ServerName != "meru-test" || info.ServerVersion != "1" {
				t.Errorf("server = %q %q, want meru-test 1", info.ServerName, info.ServerVersion)
			}

			var names []string
			byName := map[string]ProbeTool{}
			for _, tool := range info.Tools {
				names = append(names, tool.Name)
				byName[tool.Name] = tool
			}
			want := []string{"add", "crash", "echo", "fail", "getenv", "pid", "secret", "slow"}
			if !slices.Equal(names, want) {
				t.Errorf("tools = %v, want %v", names, want)
			}
			if byName["echo"].Description != "Echo the text back." {
				t.Errorf("echo description = %q", byName["echo"].Description)
			}

			hints := []struct {
				tool                  string
				readOnly, destructive string // "true", "false" or "nil"
			}{
				{"echo", "true", "nil"},
				{"crash", "false", "true"},
				{"add", "nil", "nil"},
			}
			for _, h := range hints {
				got := byName[h.tool]
				if show(got.ReadOnly) != h.readOnly || show(got.Destructive) != h.destructive {
					t.Errorf("%s hints = read-only %s, destructive %s; want %s, %s", h.tool,
						show(got.ReadOnly), show(got.Destructive), h.readOnly, h.destructive)
				}
			}
		})
	}
}

// show prints a hint for a test message: "nil", "true" or "false".
func show(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}

func TestProbeRefuses(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServerConfig
		want string // a piece of the error
	}{
		{"no name", ServerConfig{Command: "x"}, "name is empty"},
		{"neither command nor url", ServerConfig{Name: "t"}, "set command"},
		{"both command and url", ServerConfig{Name: "t", Command: "x", URL: "http://127.0.0.1:1/mcp"}, "not both"},
		{"url off this machine", ServerConfig{Name: "t", URL: "http://example.com/mcp"}, "remote = true"},
		{"bad header name", ServerConfig{Name: "t", URL: "http://127.0.0.1:1/mcp", Headers: map[string]string{"a b": "c"}}, "not a valid header name"},
		{"command missing", ServerConfig{Name: "t", Command: filepath.Join(t.TempDir(), "no-such-server")}, `mcp server "t": connect`},
		{"nothing listening", ServerConfig{Name: "t", URL: "http://127.0.0.1:1/mcp"}, `mcp server "t": connect`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Probe(context.Background(), tt.cfg, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Probe error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}
