// This file tests the `meru mcp` commands against a fake merud: the status
// table and its JSON, each shape of `meru mcp add`, the probe step (accept,
// edit, failure), a server the user runs, the reload after a write, `meru
// mcp list` and `meru mcp remove`.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// fakeMerud answers the ops `meru mcp` sends. It speaks the socket protocol
// by hand (one JSON request line in, event lines out), so it needs nothing
// from merud's own server.
type fakeMerud struct {
	// probe answers OpMCPProbe; a nil probe fails every probe.
	probe func(rpc.ProbeServer) (*rpc.ProbeResult, error)
	// servers is what OpTools and OpMCPReload report.
	servers []rpc.ServerInfo
	// status is what OpMCPStatus reports, and connectors what OpConnectors
	// reports; a nil connectors answers OpConnectors with an error, as a
	// merud from before the op does.
	status     []rpc.MCPStatus
	connectors []rpc.ConnectorStatus

	mu   sync.Mutex    // guards reqs
	reqs []rpc.Request // every request, in order
}

// ops returns the op of each request the fake received, in order.
func (f *fakeMerud) ops() []rpc.Op {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ops []rpc.Op
	for _, r := range f.reqs {
		ops = append(ops, r.Op)
	}
	return ops
}

// start listens on a new socket and serves until the test ends. It returns
// the socket's path; config.toml goes next to it.
func (f *fakeMerud) start(t *testing.T) string {
	t.Helper()
	// A short folder name, because a Unix socket path has a length limit.
	dir, err := os.MkdirTemp("", "meru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // the listener closed
			}
			f.serve(conn)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return sock
}

// serve answers one request on conn and closes it.
func (f *fakeMerud) serve(conn net.Conn) {
	defer conn.Close()
	var req rpc.Request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()

	enc := json.NewEncoder(conn)
	switch req.Op {
	case rpc.OpMCPProbe:
		if f.probe == nil {
			_ = enc.Encode(rpc.Event{Type: rpc.EventError, Error: "no probe"})
			return
		}
		res, err := f.probe(*req.Server)
		if err != nil {
			_ = enc.Encode(rpc.Event{Type: rpc.EventError, Error: err.Error()})
			return
		}
		_ = enc.Encode(rpc.Event{Type: rpc.EventProbe, Probe: res})
	case rpc.OpMCPReload, rpc.OpTools:
		_ = enc.Encode(rpc.Event{Type: rpc.EventTools, Servers: f.servers})
	case rpc.OpMCPStatus:
		_ = enc.Encode(rpc.Event{Type: rpc.EventMCPStatus, MCP: f.status})
	case rpc.OpConnectors:
		if f.connectors == nil {
			_ = enc.Encode(rpc.Event{Type: rpc.EventError, Error: `unknown op "connectors"`})
			return
		}
		_ = enc.Encode(rpc.Event{Type: rpc.EventConnectors, Connectors: f.connectors})
	}
	_ = enc.Encode(rpc.Event{Type: rpc.EventDone})
}

// hintOf returns a pointer to b, for the hints on a ProbeTool.
func hintOf(b bool) *bool { return &b }

// notesTools is what the fake "notes" server offers: one tool per kind of
// hint.
var notesTools = []rpc.ProbeTool{
	{Name: "search", Description: "Searches the notes.\nSecond line.", ReadOnly: hintOf(true)},
	{Name: "write_note", Description: "Writes a note.", ReadOnly: hintOf(false), Destructive: hintOf(false)},
	{Name: "delete_note", Description: "Deletes a note.", ReadOnly: hintOf(false), Destructive: hintOf(true)},
	{Name: "mystery", Description: "Does something."},
}

// probeNotes answers a probe with notesTools.
func probeNotes(rpc.ProbeServer) (*rpc.ProbeResult, error) {
	return &rpc.ProbeResult{ServerName: "notes-mcp", ServerVersion: "1.2.0", Tools: notesTools}, nil
}

// serverIn returns the server called name from the config.toml next to
// sock, and fails the test when there is none.
func serverIn(t *testing.T, sock, name string) config.MCPServer {
	t.Helper()
	for _, s := range loadServers(t, filepath.Dir(sock)) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("config.toml has no server %q", name)
	return config.MCPServer{}
}

// TestMCPAddProbe walks the probe step for a stdio server of your own:
// accept the proposal, or edit it first.
func TestMCPAddProbe(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantAllow   []string
		wantConfirm []string
		wantOut     []string
	}{
		{
			name:        "accept",
			input:       "d\n\ny\n",
			wantAllow:   []string{"search", "write_note", "delete_note", "mystery"},
			wantConfirm: []string{"write_note", "delete_note", "mystery"},
			wantOut:     []string{"notes-mcp 1.2.0 offers 4 tools", "read-only", "may delete", "changes things", "no hint", "Searches the notes.", "No restart needed"},
		},
		{
			name:        "edit",
			input:       "d\n-delete_note +mystery ?search\nsearch\n+nope\n\ny\n",
			wantAllow:   []string{"search", "write_note", "mystery"},
			wantConfirm: []string{"search", "write_note"},
			wantOut:     []string{`"search": start each change with -, + or ?`, `no tool called "nope"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMerud{probe: probeNotes, servers: []rpc.ServerInfo{{
				Name: "notes", Kind: "mcp", Transport: "stdio", Connected: true, Offered: 4,
				Tools: []rpc.ToolInfo{{Name: "notes.search"}},
			}}}
			sock := f.start(t)
			c, out, _ := scripted(tt.input)
			args := []string{"add", "stdio", "notes", "--", "notes-mcp", "--dir", "/x"}
			if err := mcpCmd(t.Context(), sock, args, c); err != nil {
				t.Fatalf("mcp add: %v\n%s", err, out)
			}
			s := serverIn(t, sock, "notes")
			if !slices.Equal(s.Allow, tt.wantAllow) || !slices.Equal(s.Confirm, tt.wantConfirm) {
				t.Errorf("allow %q confirm %q, want %q and %q", s.Allow, s.Confirm, tt.wantAllow, tt.wantConfirm)
			}
			if s.Command != "notes-mcp" || !slices.Equal(s.Args, []string{"--dir", "/x"}) {
				t.Errorf("server = %+v", s)
			}
			if strings.Contains(out.String(), "Second line.") {
				t.Error("the table shows more than a description's first line")
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			want := []rpc.Op{rpc.OpPing, rpc.OpMCPProbe, rpc.OpMCPReload}
			if got := f.ops(); !slices.Equal(got, want) {
				t.Errorf("ops = %v, want %v", got, want)
			}
		})
	}
}

// TestMCPAddProbeFails covers a server that doesn't start: cancel, write it
// anyway, and try again.
func TestMCPAddProbeFails(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantWrite bool
		wantAllow []string
	}{
		{"cancel", "d\nc\n", false, nil},
		{"write anyway", "d\nw\ny\n", true, nil},
		{"try again", "d\nx\nr\n\ny\n", true, []string{"search", "write_note", "delete_note", "mystery"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tries := 0
			f := &fakeMerud{probe: func(s rpc.ProbeServer) (*rpc.ProbeResult, error) {
				tries++
				if tries == 1 {
					return nil, errors.New(`exec: "notes-mcp": executable file not found in $PATH`)
				}
				return probeNotes(s)
			}}
			sock := f.start(t)
			c, out, _ := scripted(tt.input)
			if err := mcpCmd(t.Context(), sock, []string{"add", "stdio", "notes", "--", "notes-mcp"}, c); err != nil {
				t.Fatalf("mcp add: %v\n%s", err, out)
			}
			if !strings.Contains(out.String(), "executable file not found") {
				t.Errorf("output doesn't say why:\n%s", out)
			}
			_, statErr := os.Stat(configPathFor(sock))
			if wrote := statErr == nil; wrote != tt.wantWrite {
				t.Fatalf("wrote config = %v, want %v\n%s", wrote, tt.wantWrite, out)
			}
			if tt.wantWrite {
				if s := serverIn(t, sock, "notes"); !slices.Equal(s.Allow, tt.wantAllow) {
					t.Errorf("allow = %q, want %q", s.Allow, tt.wantAllow)
				}
			}
		})
	}
}

// TestMCPAddCatalogProbe adds a catalog entry through the probe: the
// catalog's lists win over the hints, and a tool the catalog doesn't know
// shows as off. google is a server the user runs, so the flow prints the
// command to start it, and probes because something answers at its URL.
func TestMCPAddCatalogProbe(t *testing.T) {
	f := &fakeMerud{probe: func(rpc.ProbeServer) (*rpc.ProbeResult, error) {
		return &rpc.ProbeResult{Tools: []rpc.ProbeTool{
			{Name: "search_gmail_messages", Description: "Searches mail.", ReadOnly: hintOf(true)},
			{Name: "send_gmail_message", Description: "Sends mail."},
			{Name: "delete_gmail_label", Description: "Deletes a label.", ReadOnly: hintOf(true)},
		}}, nil
	}}
	sock := f.start(t)
	c, out, _ := scripted("d\n\ny\n")
	c.answers = func(context.Context, string) bool { return true }
	if err := mcpCmd(t.Context(), sock, []string{"add", "google"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	s := serverIn(t, sock, "google")
	if !slices.Equal(s.Allow, []string{"search_gmail_messages", "send_gmail_message"}) || !slices.Equal(s.Confirm, []string{"send_gmail_message"}) {
		t.Errorf("allow %q confirm %q", s.Allow, s.Confirm)
	}
	if s.URL != "http://127.0.0.1:8000/mcp" || s.Command != "" || len(s.Env) != 0 {
		t.Errorf("server = %+v, want the url entry with no env", s)
	}
	for _, want := range []string{"off    delete_gmail_label", "uvx workspace-mcp --transport streamable-http"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestMCPAddServerNotRunning adds google while nothing answers at its URL:
// the flow prints the start command, skips the probe, and writes the
// catalog's lists.
func TestMCPAddServerNotRunning(t *testing.T) {
	f := &fakeMerud{probe: probeNotes}
	sock := f.start(t)
	c, out, _ := scripted("d\ny\n")
	c.answers = func(context.Context, string) bool { return false }
	if err := mcpCmd(t.Context(), sock, []string{"add", "google"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	if slices.Contains(f.ops(), rpc.OpMCPProbe) {
		t.Errorf("ops = %v, want no probe while nothing answers", f.ops())
	}
	e, _ := catalog.Find("google")
	s := serverIn(t, sock, "google")
	if !slices.Equal(s.Allow, e.Allow) || !slices.Equal(s.Confirm, e.Confirm) {
		t.Errorf("allow %q confirm %q, want the catalog's lists", s.Allow, s.Confirm)
	}
	for _, want := range []string{e.Start, "Nothing answers at http://127.0.0.1:8000/mcp", "on your next question"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestMCPAddSavesKeyBeforeProbe checks that with merud up, the key goes to
// secrets.toml before the probe, and the probe carries the reference, not
// the key.
func TestMCPAddSavesKeyBeforeProbe(t *testing.T) {
	var got rpc.ProbeServer
	f := &fakeMerud{probe: func(s rpc.ProbeServer) (*rpc.ProbeResult, error) {
		got = s
		return &rpc.ProbeResult{Tools: []rpc.ProbeTool{{Name: "obsidian_simple_search", ReadOnly: hintOf(true)}}}, nil
	}}
	sock := f.start(t)
	c, out, _ := scripted("d\n" + fakeKey + "\n\ny\n")
	if err := mcpCmd(t.Context(), sock, []string{"add", "obsidian"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	if got.Env["OBSIDIAN_API_KEY"] != "secret:obsidian_api_key" {
		t.Errorf("probe env = %v, want the secret: reference", got.Env)
	}
	s, err := secrets.Load(secrets.Path(filepath.Dir(sock)))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Resolve("secret:obsidian_api_key"); v != fakeKey {
		t.Errorf("saved key = %q", v)
	}
	if strings.Contains(out.String(), fakeKey) {
		t.Error("the key shows in the output")
	}
	// The catalog names obsidian_get_file_contents, which this fake doesn't offer.
	if !strings.Contains(out.String(), "obsidian_get_file_contents") || !slices.Equal(serverIn(t, sock, "obsidian").Allow, []string{"obsidian_simple_search"}) {
		t.Errorf("allow = %q\n%s", serverIn(t, sock, "obsidian").Allow, out)
	}
}

// TestMCPAddShapes checks what each command shape writes, with merud down,
// so the flow skips the probe and writes the catalog's lists or an empty
// allow list.
func TestMCPAddShapes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    config.MCPServer
		wantOut string
	}{
		{"stdio", []string{"add", "stdio", "notes", "--", "notes-mcp", "--dir", "/x"},
			config.MCPServer{Name: "notes", Command: "notes-mcp", Args: []string{"--dir", "/x"}}, "meru tools"},
		{"older stdio", []string{"add", "notes", "--", "notes-mcp"},
			config.MCPServer{Name: "notes", Command: "notes-mcp"}, "meru tools"},
		{"http", []string{"add", "http", "cal", "http://127.0.0.1:8123/mcp"},
			config.MCPServer{Name: "cal", URL: "http://127.0.0.1:8123/mcp"}, "merud isn't running"},
		{"older url", []string{"add", "cal", "--url", "http://127.0.0.1:8123/mcp"},
			config.MCPServer{Name: "cal", URL: "http://127.0.0.1:8123/mcp"}, "merud isn't running"},
		{"http off this machine", []string{"add", "http", "far", "https://mcp.example.com/mcp", "--remote"},
			config.MCPServer{Name: "far", URL: "https://mcp.example.com/mcp", Remote: true}, "sends your data there"},
		{"catalog url entry", []string{"add", "google"},
			config.MCPServer{Name: "google", URL: "http://127.0.0.1:8000/mcp"}, "uvx workspace-mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			c, out, _ := scripted("d\ny\n")
			if err := mcpCmd(t.Context(), filepath.Join(dir, "merud.sock"), tt.args, c); err != nil {
				t.Fatalf("mcp add: %v\n%s", err, out)
			}
			servers := loadServers(t, dir)
			if len(servers) != 1 {
				t.Fatalf("servers = %+v", servers)
			}
			s := servers[0]
			if s.Name != tt.want.Name || s.Command != tt.want.Command || s.URL != tt.want.URL || s.Remote != tt.want.Remote {
				t.Errorf("server = %+v, want %+v", s, tt.want)
			}
			if tt.name == "catalog url entry" {
				if len(s.Allow) == 0 {
					t.Errorf("allow %q, want the catalog's list", s.Allow)
				}
			} else if !slices.Equal(s.Args, tt.want.Args) || len(s.Allow) != 0 {
				t.Errorf("args %q allow %q, want %q and an empty allow", s.Args, s.Allow, tt.want.Args)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output lacks %q:\n%s", tt.wantOut, out)
			}
		})
	}
}

func TestMCPErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"not in catalog", []string{"add", "slack"}, "not in the catalog"},
		{"unknown subcommand", []string{"rename", "obsidian"}, "usage"},
		{"status with junk", []string{"status", "--yaml"}, "usage"},
		{"bad name", []string{"add", "stdio", "a.b", "--", "x"}, "letters, digits"},
		{"older bad name", []string{"add", "a.b", "--", "x"}, "letters, digits"},
		{"url without scheme", []string{"add", "http", "x", "127.0.0.1:1"}, "http://"},
		{"off this machine without --remote", []string{"add", "http", "x", "https://mcp.example.com/mcp"}, "--remote"},
		{"older url off this machine", []string{"add", "x", "--url", "https://mcp.example.com/mcp"}, "--remote"},
		{"stdio without --", []string{"add", "stdio", "x", "notes-mcp"}, "usage"},
		{"stdio with a URL", []string{"add", "stdio", "x", "--", "http://127.0.0.1:1/mcp"}, "meru mcp add http"},
		{"http with junk", []string{"add", "http", "x", "http://127.0.0.1:1/mcp", "--yes"}, "usage"},
		{"args to obsidian", []string{"add", "obsidian", "extra"}, "takes no arguments"},
		{"remove without a name", []string{"remove", "--yes"}, "usage"},
		{"remove unknown", []string{"remove", "--yes", "obsidian"}, "no MCP server named"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, _ := scripted("")
			err := mcpCmd(t.Context(), filepath.Join(t.TempDir(), "merud.sock"), tt.args, c)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestMCPList checks the catalog part and the servers part, with merud up
// and down.
func TestMCPList(t *testing.T) {
	f := &fakeMerud{servers: []rpc.ServerInfo{
		{Name: "obsidian", Kind: "mcp", Connected: true, Offered: 2, Tools: []rpc.ToolInfo{{Name: "obsidian.obsidian_simple_search"}}},
		{Name: "notes", Kind: "mcp", LastError: "exit status 1"},
		{Name: "meru", Kind: "builtin", Connected: true},
	}}
	sock := f.start(t)
	config := "[[mcp.servers]]\nname = \"obsidian\"\ncommand = \"uvx\"\nallow = [\"obsidian_simple_search\"]\n\n" +
		"[[mcp.servers]]\nname = \"notes\"\ncommand = \"notes-mcp\"\n\n" +
		"[[mcp.servers]]\nname = \"fresh\"\nurl = \"http://127.0.0.1:9/mcp\"\n"
	if err := os.WriteFile(configPathFor(sock), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	c, out, _ := scripted("")
	if err := mcpCmd(t.Context(), sock, []string{"list"}, c); err != nil {
		t.Fatal(err)
	}
	want := []string{"google", "obsidian", "needs", "you start it", "connected · offers 2, 1 allowed",
		"not connected: exit status 1", "not loaded yet"}
	for _, w := range want {
		if !strings.Contains(out.String(), w) {
			t.Errorf("list lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out.String(), "  meru ") {
		t.Errorf("list shows the built-ins:\n%s", out)
	}

	// merud down: the servers from config.toml, and a note.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	c, out, _ = scripted("")
	if err := mcpCmd(t.Context(), filepath.Join(dir, "merud.sock"), []string{"list"}, c); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"merud didn't answer", "fresh", "1 allowed in config"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("list without merud lacks %q:\n%s", w, out)
		}
	}
}

// TestMCPRemove removes a server after a yes and asks merud to reload, and
// leaves the file alone after a no.
func TestMCPRemove(t *testing.T) {
	orig := "# mine\nprofile = \"lite\"\n\n" +
		"[[mcp.servers]]\nname = \"obsidian\"\ncommand = \"uvx\"\n\n" +
		"# my notes server\n[[mcp.servers]]\nname = \"notes\"\ncommand = \"notes-mcp\"\n"
	tests := []struct {
		name    string
		args    []string
		input   string
		removed bool
	}{
		{"yes", []string{"remove", "obsidian"}, "y\n", true},
		{"--yes", []string{"remove", "--yes", "obsidian"}, "", true},
		{"no", []string{"remove", "obsidian"}, "\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMerud{}
			sock := f.start(t)
			if err := os.WriteFile(configPathFor(sock), []byte(orig), 0o600); err != nil {
				t.Fatal(err)
			}
			c, out, _ := scripted(tt.input)
			if err := mcpCmd(t.Context(), sock, tt.args, c); err != nil {
				t.Fatalf("remove: %v\n%s", err, out)
			}
			got, _ := os.ReadFile(configPathFor(sock))
			if !tt.removed {
				if string(got) != orig {
					t.Errorf("config changed after a no:\n%s", got)
				}
				return
			}
			want := "# mine\nprofile = \"lite\"\n\n# my notes server\n[[mcp.servers]]\nname = \"notes\"\ncommand = \"notes-mcp\"\n"
			if string(got) != want {
				t.Errorf("config =\n%s\nwant\n%s", got, want)
			}
			if !slices.Contains(f.ops(), rpc.OpMCPReload) || !strings.Contains(out.String(), "reloaded") {
				t.Errorf("no reload: ops %v\n%s", f.ops(), out)
			}
		})
	}
}

func TestApplyEdits(t *testing.T) {
	picks := func() []pick {
		return []pick{{tool: rpc.ProbeTool{Name: "a"}, state: toolAsk}, {tool: rpc.ProbeTool{Name: "b"}, state: toolAllow}}
	}
	tests := []struct {
		line    string
		want    []int
		wantErr string
	}{
		{"-a", []int{toolOff, toolAllow}, ""},
		{"+a ?b", []int{toolAllow, toolAsk}, ""},
		{"  -b   -a ", []int{toolOff, toolOff}, ""},
		{"-a !b", []int{toolAsk, toolAllow}, "start each change"},
		{"-a -c", []int{toolAsk, toolAllow}, `no tool called "c"`},
		{"-", []int{toolAsk, toolAllow}, "start each change"},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			p := picks()
			err := applyEdits(p, tt.line)
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			for i, w := range tt.want {
				if p[i].state != w {
					t.Errorf("%s: state %d, want %d (an error must change nothing)", p[i].tool.Name, p[i].state, w)
				}
			}
		})
	}
}

// TestMCPStatus checks `meru mcp`, `meru mcp status` and --json against a
// fake merud, and the error when merud doesn't answer.
func TestMCPStatus(t *testing.T) {
	rows := []rpc.MCPStatus{
		{Name: "google", Transport: "http", State: "not connected", URL: "http://127.0.0.1:8000/mcp", Tools: -1, Allowed: 8, Confirm: 2,
			Err: "connect: dial tcp 127.0.0.1:8000: connect: connection refused"},
		{Name: "obsidian", Transport: "stdio", State: "connected", Tools: 13, Allowed: 5, Confirm: 1},
	}
	f := &fakeMerud{status: rows}
	sock := f.start(t)
	table := "SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM\n" +
		"google     http       not connected     —        8        2   connect: dial tcp 127.0.0.1:8000: connect: connection refused\n" +
		"obsidian   stdio      connected        13        5        1\n"
	for _, args := range [][]string{nil, {"status"}} {
		c, out, _ := scripted("")
		if err := mcpCmd(t.Context(), sock, args, c); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out.String() != table {
			t.Errorf("%v printed\n%s\nwant\n%s", args, out, table)
		}
	}

	// --json prints the rows under "servers", and they read back as the
	// same structs. This merud knows no connectors op, so "connectors" is
	// an empty list.
	for _, args := range [][]string{{"--json"}, {"status", "--json"}} {
		c, out, _ := scripted("")
		if err := mcpCmd(t.Context(), sock, args, c); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var got statusJSON
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if !slices.Equal(got.Servers, rows) || got.Connectors == nil || len(got.Connectors) != 0 {
			t.Errorf("%v = %+v, want the rows and no connectors", args, got)
		}
	}

	// No servers: empty JSON lists, and a line on how to add one.
	empty := (&fakeMerud{}).start(t)
	c, out, _ := scripted("")
	if err := mcpCmd(t.Context(), empty, []string{"--json"}, c); err != nil ||
		strings.Join(strings.Fields(out.String()), "") != `{"servers":[],"connectors":[]}` {
		t.Errorf("--json with no servers = %q, %v; want empty lists", out, err)
	}
	c, out, _ = scripted("")
	if err := mcpCmd(t.Context(), empty, nil, c); err != nil || !strings.Contains(out.String(), "meru mcp add") {
		t.Errorf("no servers printed %q, %v", out, err)
	}

	// merud down.
	c, _, _ = scripted("")
	if err := mcpCmd(t.Context(), filepath.Join(t.TempDir(), "merud.sock"), nil, c); err == nil {
		t.Error("meru mcp with merud down succeeded, want an error")
	}
}

// statusJSON is what `meru mcp --json` prints.
type statusJSON struct {
	Servers    []rpc.MCPStatus       `json:"servers"`
	Connectors []rpc.ConnectorStatus `json:"connectors"`
}

// TestMCPStatusConnectors checks that the text and the JSON of `meru mcp`
// both carry the connectors merud reports, with the same fields.
func TestMCPStatusConnectors(t *testing.T) {
	conns := []rpc.ConnectorStatus{
		{ID: "obsidian", Name: "Obsidian", Kind: "stdio", State: rpc.ConnectorNeedsConfig,
			Sentence: "Obsidian needs your vault folder.", Fix: []string{"vault_path"},
			Fields: []rpc.ConnectorField{{ID: "vault_path", Type: "folder", Label: "Vault folder", Required: true}}},
	}
	sock := (&fakeMerud{connectors: conns}).start(t)

	c, out, _ := scripted("")
	if err := mcpCmd(t.Context(), sock, nil, c); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CONNECTOR  STATE", "obsidian   needs config", "Set vault_path under [connectors.obsidian]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}

	c, out, _ = scripted("")
	if err := mcpCmd(t.Context(), sock, []string{"status", "--json"}, c); err != nil {
		t.Fatal(err)
	}
	var got statusJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(got.Connectors, conns) || got.Servers == nil {
		t.Errorf("JSON = %+v, want the connectors %+v and a servers list", got, conns)
	}
}
