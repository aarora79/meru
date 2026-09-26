// This file tests adding an MCP server of the user's own through OpMCPAdd:
// the checks customEntry makes, and the whole op over the socket, where
// the block lands in config.toml with no tools allowed, a secret lands in
// secrets.toml and never in config.toml or a reply, and comments and
// other keys survive.

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

func TestCustomEntry(t *testing.T) {
	sec, err := secrets.Load(filepath.Join(t.TempDir(), "secrets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   rpc.CustomServer
		want string // part of the error; "" means it must pass
	}{
		{"stdio with args", rpc.CustomServer{Name: "notes", Command: "npx", Args: []string{"-y", "some-mcp"}}, ""},
		{"uvx is fine", rpc.CustomServer{Name: "notes", Command: "uvx", Args: []string{"notes-mcp"}}, ""},
		{"loopback url", rpc.CustomServer{Name: "notes", URL: "http://127.0.0.1:8000/mcp"}, ""},
		{"remote url, ticked", rpc.CustomServer{Name: "far", URL: "https://mcp.example.invalid/mcp", Remote: true}, ""},
		{"remote url, not ticked", rpc.CustomServer{Name: "far", URL: "https://mcp.example.invalid/mcp"}, "isn't on this computer"},
		{"bad name", rpc.CustomServer{Name: "my notes", Command: "npx"}, "use only letters"},
		{"no name", rpc.CustomServer{Command: "npx"}, "1 to 64"},
		{"neither", rpc.CustomServer{Name: "notes"}, "give the command"},
		{"both", rpc.CustomServer{Name: "notes", Command: "npx", URL: "http://127.0.0.1:8000/mcp"}, "not both"},
		{"command is a url", rpc.CustomServer{Name: "notes", Command: "http://127.0.0.1:8000/mcp"}, "is a URL"},
		{"url without scheme", rpc.CustomServer{Name: "notes", URL: "127.0.0.1:8000/mcp"}, "must start with"},
		{"env on a url", rpc.CustomServer{Name: "notes", URL: "http://127.0.0.1:8000/mcp",
			Env: []rpc.EnvVar{{Name: "TOKEN", Value: "x"}}}, "only to a server merud starts"},
		{"bad env name", rpc.CustomServer{Name: "notes", Command: "npx", Env: []rpc.EnvVar{{Name: "1ABC", Value: "x"}}}, "isn't a variable name"},
		{"env twice", rpc.CustomServer{Name: "notes", Command: "npx",
			Env: []rpc.EnvVar{{Name: "A", Value: "x"}, {Name: "A", Value: "y"}}}, "appears twice"},
		{"empty value", rpc.CustomServer{Name: "notes", Command: "npx", Env: []rpc.EnvVar{{Name: "A", Value: " "}}}, "has no value"},
		{"unknown secret ref", rpc.CustomServer{Name: "notes", Command: "npx",
			Env: []rpc.EnvVar{{Name: "A", Value: "secret:nowhere"}}}, "doesn't hold"},
		{"secret name too long", rpc.CustomServer{Name: strings.Repeat("n", 60), Command: "npx",
			Env: []rpc.EnvVar{{Name: "TOKEN", Value: "x", Secret: true}}}, "over 64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := customEntry(c.in, sec)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("customEntry: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("customEntry error = %v, want %q", err, c.want)
			}
		})
	}

	// A secret variable becomes a reference, and its value goes to the
	// secrets to save; a plain one stays as it is.
	e, keys, err := customEntry(rpc.CustomServer{Name: "Notes", Command: "npx", Env: []rpc.EnvVar{
		{Name: "NOTES_TOKEN", Value: " t-123 ", Secret: true}, {Name: "LOG_LEVEL", Value: "info"},
	}}, sec)
	if err != nil {
		t.Fatal(err)
	}
	if e.Env["NOTES_TOKEN"] != "secret:notes_notes_token" || e.Env["LOG_LEVEL"] != "info" || keys["notes_notes_token"] != "t-123" {
		t.Errorf("env = %v, keys = %v", e.Env, keys)
	}
	if len(e.Allow) != 0 {
		t.Errorf("a new server allows %v; every tool must start off", e.Allow)
	}
}

func TestCustomServerOp(t *testing.T) {
	dir, _ := meruHome(t)
	// An empty PATH keeps merud from finding the command, so the server
	// shows as not connected and nothing runs.
	t.Setenv("PATH", dir)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})

	const token = "t-98765432"
	add := rpc.Request{Op: rpc.OpMCPAdd, Custom: &rpc.CustomServer{
		Name: "notes", Command: "notes-mcp", Args: []string{"--vault", "/home/dana/My Notes"},
		Env: []rpc.EnvVar{{Name: "NOTES_TOKEN", Value: token, Secret: true}, {Name: "LOG_LEVEL", Value: "info"}},
	}}
	evs := mustCall(t, d.sock, add)
	c := connection(t, eventOf(t, evs, rpc.EventConnections), "mcp", "notes")
	if c.Transport != "stdio" || c.State != rpc.MCPNotConnected {
		t.Errorf("notes = %+v", c)
	}
	for _, tp := range c.Tools {
		if tp.Policy != rpc.PolicyOff {
			t.Errorf("tool %s starts %s; every tool must start off", tp.Name, tp.Policy)
		}
	}

	cfg := loadConfig(t, dir)
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("servers = %+v", cfg.MCP.Servers)
	}
	srv := cfg.MCP.Servers[0]
	if srv.Command != "notes-mcp" || !slices.Equal(srv.Args, []string{"--vault", "/home/dana/My Notes"}) ||
		len(srv.Allow) != 0 || srv.Env["NOTES_TOKEN"] != "secret:notes_notes_token" || srv.Env["LOG_LEVEL"] != "info" {
		t.Errorf("config entry = %+v", srv)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(raw), token) {
		t.Error("config.toml holds the secret's value")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "secrets.toml")); !strings.Contains(string(raw), token) {
		t.Errorf("secrets.toml = %q; want the token", raw)
	}
	for _, ev := range evs {
		if strings.Contains(ev.Text, token) || strings.Contains(ev.Error, token) {
			t.Errorf("a reply holds the secret: %+v", ev)
		}
	}

	// Refusals write nothing: a name config has, and a URL off this
	// machine without the tick.
	for _, bad := range []struct {
		req  rpc.CustomServer
		want string
	}{
		{rpc.CustomServer{Name: "notes", Command: "other-mcp"}, "already has a server"},
		{rpc.CustomServer{Name: "far", URL: "https://mcp.example.invalid/mcp"}, "isn't on this computer"},
	} {
		req := rpc.Request{Op: rpc.OpMCPAdd, Custom: &bad.req}
		if _, msg := callErr(t, d.sock, req, rpc.ChoiceOnce); !strings.Contains(msg, bad.want) {
			t.Errorf("add %s: %q, want %q", bad.req.Name, msg, bad.want)
		}
	}
	if n := len(loadConfig(t, dir).MCP.Servers); n != 1 {
		t.Errorf("config has %d servers after the refusals, want 1", n)
	}

	// A loopback URL needs no tick, and one off this machine with it gets
	// remote = true. A .invalid name never resolves, so the test reaches
	// no network.
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpMCPAdd, Custom: &rpc.CustomServer{Name: "local", URL: "http://127.0.0.1:1/mcp"}})
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpMCPAdd, Custom: &rpc.CustomServer{Name: "far", URL: "https://mcp.example.invalid/mcp", Remote: true}})
	cfg = loadConfig(t, dir)
	if len(cfg.MCP.Servers) != 3 || cfg.MCP.Servers[1].Remote || !cfg.MCP.Servers[2].Remote {
		t.Errorf("servers = %+v", cfg.MCP.Servers)
	}
}
