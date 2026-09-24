// This file tests the server config checks: which entries Validate and
// ValidateAll accept, and which they refuse.

package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	// ok returns a valid stdio entry; each row changes one thing.
	ok := func() ServerConfig {
		return ServerConfig{Name: "files", Command: "files-server", Allow: []string{"read", "search"}, Confirm: []string{"read"}}
	}
	tests := []struct {
		name    string
		edit    func(c *ServerConfig)
		wantErr string // "" means valid; otherwise a piece of the error text
	}{
		{"valid stdio", func(c *ServerConfig) {}, ""},
		{"valid http loopback", func(c *ServerConfig) { c.Command = ""; c.URL = "http://127.0.0.1:8123/mcp" }, ""},
		{"valid http localhost", func(c *ServerConfig) { c.Command = ""; c.URL = "http://localhost:8123/mcp" }, ""},
		{"valid http network", func(c *ServerConfig) { c.Command = ""; c.URL = "https://mcp.example.com/mcp"; c.Network = true }, ""},
		{"empty allow is valid and gives nothing", func(c *ServerConfig) { c.Allow = nil; c.Confirm = nil }, ""},
		{"tool name with dots", func(c *ServerConfig) { c.Allow = []string{"files.read_v2"}; c.Confirm = nil }, ""},
		{"both command and url", func(c *ServerConfig) { c.URL = "http://127.0.0.1:8123/mcp" }, "not both"},
		{"neither command nor url", func(c *ServerConfig) { c.Command = "" }, "set command"},
		{"non-loopback url without network", func(c *ServerConfig) { c.Command = ""; c.URL = "https://mcp.example.com/mcp" }, "network = true"},
		{"private address without network", func(c *ServerConfig) { c.Command = ""; c.URL = "http://192.168.1.5:8123/mcp" }, "not a loopback"},
		{"url with bad scheme", func(c *ServerConfig) { c.Command = ""; c.URL = "ftp://127.0.0.1/mcp" }, "scheme"},
		{"network url with bad scheme", func(c *ServerConfig) { c.Command = ""; c.URL = "ftp://mcp.example.com"; c.Network = true }, "http or https"},
		{"network on stdio", func(c *ServerConfig) { c.Network = true }, "network applies only"},
		{"args on http", func(c *ServerConfig) { c.Command = ""; c.URL = "http://127.0.0.1:1/mcp"; c.Args = []string{"x"} }, "args and env"},
		{"empty name", func(c *ServerConfig) { c.Name = "" }, "name is empty"},
		{"name with dot", func(c *ServerConfig) { c.Name = "my.files" }, "letters, digits"},
		{"name with space", func(c *ServerConfig) { c.Name = "my files" }, "letters, digits"},
		{"name with slash", func(c *ServerConfig) { c.Name = "../files" }, "letters, digits"},
		{"name non-ascii", func(c *ServerConfig) { c.Name = "fílés" }, "letters, digits"},
		{"name too long", func(c *ServerConfig) { c.Name = strings.Repeat("a", 65) }, "longer than"},
		{"wildcard allow", func(c *ServerConfig) { c.Allow = []string{"*"}; c.Confirm = nil }, "wildcards"},
		{"glob allow", func(c *ServerConfig) { c.Allow = []string{"read_*"}; c.Confirm = nil }, "wildcards"},
		{"empty allow entry", func(c *ServerConfig) { c.Allow = []string{""}; c.Confirm = nil }, "not a tool name"},
		{"allow entry with space", func(c *ServerConfig) { c.Allow = []string{"read file"}; c.Confirm = nil }, "not a tool name"},
		{"confirm not in allow", func(c *ServerConfig) { c.Confirm = []string{"delete"} }, "not in allow"},
		{"bad env name", func(c *ServerConfig) { c.Env = map[string]string{"A=B": "x"} }, "variable name"},
		{"negative timeout", func(c *ServerConfig) { c.Timeout = -time.Second }, "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok()
			tt.edit(&c)
			err := c.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAllRefusesDuplicateNames(t *testing.T) {
	servers := []ServerConfig{
		{Name: "files", Command: "a"},
		{Name: "files", Command: "b"},
	}
	err := ValidateAll(servers)
	if err == nil || !strings.Contains(err.Error(), "share this name") {
		t.Fatalf("ValidateAll() = %v, want a duplicate-name error", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	c := ServerConfig{Name: "bad name", Allow: []string{"*"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want errors")
	}
	for _, want := range []string{"letters, digits", "set command", "wildcards"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() = %v, want it to mention %q", err, want)
		}
	}
}
