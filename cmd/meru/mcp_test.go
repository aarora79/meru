// This file tests the `meru mcp` commands against a fake merud: each shape
// of `meru mcp add`, the probe step (accept, edit, failure), the reload
// after a write, `meru mcp list` and `meru mcp remove`.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
// shows as off.
func TestMCPAddCatalogProbe(t *testing.T) {
	f := &fakeMerud{probe: func(rpc.ProbeServer) (*rpc.ProbeResult, error) {
		return &rpc.ProbeResult{Tools: []rpc.ProbeTool{
			{Name: "fetch", Description: "Fetches a URL."},
			{Name: "fetch_raw", Description: "Fetches raw bytes.", ReadOnly: hintOf(true)},
		}}, nil
	}}
	sock := f.start(t)
	c, out, _ := scripted("d\n\ny\n")
	if err := mcpCmd(t.Context(), sock, []string{"add", "fetch"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	s := serverIn(t, sock, "fetch")
	if !slices.Equal(s.Allow, []string{"fetch"}) || len(s.Confirm) != 0 {
		t.Errorf("allow %q confirm %q", s.Allow, s.Confirm)
	}
	if !strings.Contains(out.String(), "off    fetch_raw") {
		t.Errorf("the unknown tool isn't shown as off:\n%s", out)
	}
}

// TestMCPAddSavesKeyBeforeProbe checks that with merud up, the key goes to
// secrets.toml before the probe, and the probe carries the reference, not
// the key.
func TestMCPAddSavesKeyBeforeProbe(t *testing.T) {
	var got rpc.ProbeServer
	f := &fakeMerud{probe: func(s rpc.ProbeServer) (*rpc.ProbeResult, error) {
		got = s
		return &rpc.ProbeResult{Tools: []rpc.ProbeTool{{Name: "brave_web_search", ReadOnly: hintOf(true)}}}, nil
	}}
	sock := f.start(t)
	c, out, _ := scripted("d\n" + fakeKey + "\n\ny\n")
	if err := mcpCmd(t.Context(), sock, []string{"add", "brave"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	if got.Env["BRAVE_API_KEY"] != "secret:brave_api_key" {
		t.Errorf("probe env = %v, want the secret: reference", got.Env)
	}
	s, err := secrets.Load(secrets.Path(filepath.Dir(sock)))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Resolve("secret:brave_api_key"); v != fakeKey {
		t.Errorf("saved key = %q", v)
	}
	if strings.Contains(out.String(), fakeKey) {
		t.Error("the key shows in the output")
	}
	// The catalog names brave_news_search, which this fake doesn't offer.
	if !strings.Contains(out.String(), "brave_news_search") || !slices.Equal(serverIn(t, sock, "brave").Allow, []string{"brave_web_search"}) {
		t.Errorf("allow = %q\n%s", serverIn(t, sock, "brave").Allow, out)
	}
}

// TestMCPAddShapes checks what each command shape writes, with merud down,
// so the flow skips the probe and writes the catalog's lists or an empty
// allow list.
func TestMCPAddShapes(t *testing.T) {
	folder := t.TempDir()
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
		{"http off this machine", []string{"add", "http", "far", "https://mcp.example.com/mcp", "--network"},
			config.MCPServer{Name: "far", URL: "https://mcp.example.com/mcp", Network: true}, "sends your data there"},
		{"filesystem", []string{"add", "filesystem", folder},
			config.MCPServer{Name: "filesystem", Command: "npx"}, "merud isn't running"},
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
			if s.Name != tt.want.Name || s.Command != tt.want.Command || s.URL != tt.want.URL || s.Network != tt.want.Network {
				t.Errorf("server = %+v, want %+v", s, tt.want)
			}
			if tt.name == "filesystem" {
				if s.Args[len(s.Args)-1] != folder || len(s.Allow) == 0 {
					t.Errorf("filesystem args %q allow %q, want the folder last and the catalog's list", s.Args, s.Allow)
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

// TestMCPAddAsksFolders runs the filesystem entry with no folders on the
// command line, as meru setup does: the flow asks for them.
func TestMCPAddAsksFolders(t *testing.T) {
	dir := t.TempDir()
	folder := t.TempDir()
	c, out, _ := scripted("d\n/no/such/folder\n" + folder + "\ny\n")
	e, _ := catalog.Find("filesystem")
	if _, err := c.offer(t.Context(), filepath.Join(dir, "merud.sock"), e); err != nil {
		t.Fatalf("offer: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "doesn't exist") {
		t.Errorf("output doesn't flag the missing folder:\n%s", out)
	}
	s := loadServers(t, dir)
	if len(s) != 1 || s[0].Args[len(s[0].Args)-1] != folder {
		t.Errorf("servers = %+v", s)
	}
}

// TestMCPAddShell adds the shell entry with merud down: Enter keeps the
// starting list of programs, or the user types their own.
func TestMCPAddShell(t *testing.T) {
	tests := []struct{ name, answer, want string }{
		{"keep the list", "", "ls,pwd,cat"},
		{"own list", "ls,git", "ls,git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			c, out, _ := scripted("d\n" + tt.answer + "\ny\n")
			if err := mcpCmd(t.Context(), filepath.Join(dir, "merud.sock"), []string{"add", "shell"}, c); err != nil {
				t.Fatalf("mcp add: %v\n%s", err, out)
			}
			s := loadServers(t, dir)
			if len(s) != 1 || !strings.HasPrefix(s[0].Env["ALLOW_COMMANDS"], tt.want) {
				t.Fatalf("servers = %+v", s)
			}
			if !slices.Equal(s[0].AlwaysConfirm, []string{"shell_execute"}) {
				t.Errorf("always_confirm = %q; the command tool must ask every time", s[0].AlwaysConfirm)
			}
			if !strings.Contains(out.String(), "no sandbox") {
				t.Errorf("output doesn't warn that commands run as you:\n%s", out)
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
		{"unknown subcommand", []string{"rename", "brave"}, "usage"},
		{"no words", nil, "usage"},
		{"bad name", []string{"add", "stdio", "a.b", "--", "x"}, "letters, digits"},
		{"older bad name", []string{"add", "a.b", "--", "x"}, "letters, digits"},
		{"url without scheme", []string{"add", "http", "x", "127.0.0.1:1"}, "http://"},
		{"off this machine without --network", []string{"add", "http", "x", "https://mcp.example.com/mcp"}, "--network"},
		{"older url off this machine", []string{"add", "x", "--url", "https://mcp.example.com/mcp"}, "--network"},
		{"stdio without --", []string{"add", "stdio", "x", "notes-mcp"}, "usage"},
		{"stdio with a URL", []string{"add", "stdio", "x", "--", "http://127.0.0.1:1/mcp"}, "meru mcp add http"},
		{"http with junk", []string{"add", "http", "x", "http://127.0.0.1:1/mcp", "--yes"}, "usage"},
		{"filesystem without folders", []string{"add", "filesystem"}, "at least one folder"},
		{"args to fetch", []string{"add", "fetch", "extra"}, "takes no arguments"},
		{"remove without a name", []string{"remove", "--yes"}, "usage"},
		{"remove unknown", []string{"remove", "--yes", "brave"}, "no MCP server named"},
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

func TestMCPAddWindowsOnly(t *testing.T) {
	_, err := addEntry([]string{"windows"}, "darwin")
	if err == nil || !strings.Contains(err.Error(), "only on Windows") {
		t.Errorf("error = %v", err)
	}
	if _, err := addEntry([]string{"windows"}, "windows"); err != nil {
		t.Errorf("on Windows: %v", err)
	}
}

// TestMCPList checks the catalog part and the servers part, with merud up
// and down.
func TestMCPList(t *testing.T) {
	f := &fakeMerud{servers: []rpc.ServerInfo{
		{Name: "fetch", Kind: "mcp", Connected: true, Offered: 2, Tools: []rpc.ToolInfo{{Name: "fetch.fetch"}}},
		{Name: "notes", Kind: "mcp", LastError: "exit status 1"},
		{Name: "meru", Kind: "builtin", Connected: true},
	}}
	sock := f.start(t)
	config := "[[mcp.servers]]\nname = \"fetch\"\ncommand = \"uvx\"\nallow = [\"fetch\"]\n\n" +
		"[[mcp.servers]]\nname = \"notes\"\ncommand = \"notes-mcp\"\n\n" +
		"[[mcp.servers]]\nname = \"fresh\"\nurl = \"http://127.0.0.1:9/mcp\"\n"
	if err := os.WriteFile(configPathFor(sock), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	c, out, _ := scripted("")
	if err := mcpCmd(t.Context(), sock, []string{"list"}, c); err != nil {
		t.Fatal(err)
	}
	want := []string{"filesystem", "shell", "google", "needs", "connected · offers 2, 1 allowed",
		"not connected: exit status 1", "not loaded yet"}
	if runtime.GOOS != "windows" {
		want = append(want, "Windows only")
	}
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
		"[[mcp.servers]]\nname = \"fetch\"\ncommand = \"uvx\"\n\n" +
		"# my notes server\n[[mcp.servers]]\nname = \"notes\"\ncommand = \"notes-mcp\"\n"
	tests := []struct {
		name    string
		args    []string
		input   string
		removed bool
	}{
		{"yes", []string{"remove", "fetch"}, "y\n", true},
		{"--yes", []string{"remove", "--yes", "fetch"}, "", true},
		{"no", []string{"remove", "fetch"}, "\n", false},
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
