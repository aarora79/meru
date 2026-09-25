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
		{"valid http remote", func(c *ServerConfig) { c.Command = ""; c.URL = "https://mcp.example.com/mcp"; c.Remote = true }, ""},
		{"empty allow is valid and gives nothing", func(c *ServerConfig) { c.Allow = nil; c.Confirm = nil }, ""},
		{"tool name with dots", func(c *ServerConfig) { c.Allow = []string{"files.read_v2"}; c.Confirm = nil }, ""},
		{"both command and url", func(c *ServerConfig) { c.URL = "http://127.0.0.1:8123/mcp" }, "not both"},
		{"neither command nor url", func(c *ServerConfig) { c.Command = "" }, "set command"},
		{"non-loopback url without remote", func(c *ServerConfig) { c.Command = ""; c.URL = "https://mcp.example.com/mcp" }, "remote = true"},
		{"private address without remote", func(c *ServerConfig) { c.Command = ""; c.URL = "http://192.168.1.5:8123/mcp" }, "not a loopback"},
		{"url with bad scheme", func(c *ServerConfig) { c.Command = ""; c.URL = "ftp://127.0.0.1/mcp" }, "scheme"},
		{"remote url with bad scheme", func(c *ServerConfig) { c.Command = ""; c.URL = "ftp://mcp.example.com"; c.Remote = true }, "http or https"},
		{"remote on stdio", func(c *ServerConfig) { c.Remote = true }, "remote applies only"},
		{"args on http", func(c *ServerConfig) { c.Command = ""; c.URL = "http://127.0.0.1:1/mcp"; c.Args = []string{"x"} }, "args apply only"},
		{"env on http", func(c *ServerConfig) {
			c.Command = ""
			c.URL = "http://127.0.0.1:8000/mcp"
			c.Env = map[string]string{"GOOGLE_OAUTH_CLIENT_ID": "x"}
		}, `mcp server "files": env does nothing on a url server, because merud doesn't start it. Set the variables where you start the server, or send a key with headers`},
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
		{"always_confirm not in allow", func(c *ServerConfig) { c.AlwaysConfirm = []string{"delete"} }, "always_confirm: \"delete\" is not in allow"},
		{"always_confirm wildcard", func(c *ServerConfig) { c.AlwaysConfirm = []string{"*"} }, "always_confirm"},
		{"bad env name", func(c *ServerConfig) { c.Env = map[string]string{"A=B": "x"} }, "variable name"},
		{"negative timeout", func(c *ServerConfig) { c.Timeout = -time.Second }, "negative"},
		{"valid http headers", func(c *ServerConfig) {
			c.Command = ""
			c.URL = "http://127.0.0.1:1/mcp"
			c.Headers = map[string]string{"Authorization": "Bearer x"}
		}, ""},
		{"headers on stdio", func(c *ServerConfig) { c.Headers = map[string]string{"X-Key": "k"} }, "headers apply only"},
		{"bad header name", func(c *ServerConfig) {
			c.Command = ""
			c.URL = "http://127.0.0.1:1/mcp"
			c.Headers = map[string]string{"X Key": "k"}
		}, "not a valid header name"},
		{"header value with newline", func(c *ServerConfig) {
			c.Command = ""
			c.URL = "http://127.0.0.1:1/mcp"
			c.Headers = map[string]string{"X-Key": "k\r\nX-Other: y"}
		}, "line break"},
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
