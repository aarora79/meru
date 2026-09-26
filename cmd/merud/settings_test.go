// This file tests the desktop app's settings ops over the socket, against a
// whole merud on a fake engine: tool policies, catalog servers, secrets,
// folders, skills, saving a file, attaching one and the models. Each
// change is checked in config.toml, where comments and unrelated keys must
// survive.

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// settingsHeader opens every config these tests write: a comment and a
// setting no op touches, to check both survive each edit.
const settingsHeader = "# my own note, keep me\n[agent]\nhistory_turns = 3 # three is plenty\n\n[index]\nwatch = false\n"

// meruHome makes a home folder, points $HOME at it (os.UserHomeDir reads
// it), and makes merud's home inside it, as ~/.meru. It returns both.
func meruHome(t *testing.T) (meru, home string) {
	t.Helper()
	home = shortDir(t)
	meru = filepath.Join(home, ".meru")
	if err := os.MkdirAll(meru, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return meru, home
}

// callErr sends req, answering any approval with choice, and returns the
// events and the text of a closing "error" event, "" when there is none.
func callErr(t *testing.T, sock string, req rpc.Request, choice rpc.Choice) ([]rpc.Event, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	approve := func(context.Context, rpc.Approval) (rpc.Choice, error) { return choice, nil }
	var evs []rpc.Event
	for ev, err := range rpc.Do(ctx, sock, req, approve) {
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		evs = append(evs, ev)
	}
	if last := lastEvent(evs); last.Type == rpc.EventError {
		return evs, last.Error
	}
	return evs, ""
}

// mustCall is callErr for a request that must succeed.
func mustCall(t *testing.T, sock string, req rpc.Request) []rpc.Event {
	t.Helper()
	evs, msg := callErr(t, sock, req, rpc.ChoiceOnce)
	if msg != "" {
		t.Fatalf("%s: %s", req.Op, msg)
	}
	return evs
}

// eventOf returns the first event of type typ.
func eventOf(t *testing.T, evs []rpc.Event, typ rpc.EventType) rpc.Event {
	t.Helper()
	for _, ev := range evs {
		if ev.Type == typ {
			return ev
		}
	}
	t.Fatalf("no %s event in %+v", typ, evs)
	return rpc.Event{}
}

// connection returns the connection named name of kind.
func connection(t *testing.T, ev rpc.Event, kind, name string) rpc.Connection {
	t.Helper()
	for _, c := range ev.Connections {
		if c.Kind == kind && c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s connection %q in %+v", kind, name, ev.Connections)
	return rpc.Connection{}
}

// policy returns the policy of tool in c, or "" when c doesn't list it.
func policy(c rpc.Connection, tool string) string {
	for _, p := range c.Tools {
		if p.Name == tool {
			return p.Policy
		}
	}
	return ""
}

// loadConfig reads config.toml in dir and checks the header survived.
func loadConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"# my own note, keep me", "history_turns = 3 # three is plenty"} {
		if !strings.Contains(string(raw), keep) {
			t.Errorf("config.toml lost %q:\n%s", keep, raw)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.toml doesn't load: %v", err)
	}
	return cfg
}

func TestToolPolicyOp(t *testing.T) {
	dir, _ := meruHome(t)
	pid := filepath.Join(dir, "srv.pid")
	d := startDaemon(t, dir, settingsHeader+stdioServerEntry(t, "local", pid, ""), &fakeEngine{version: "0.13.0"})

	// The server offers echo; config allows nothing, so it shows off.
	ev := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpConnections}), rpc.EventConnections)
	local := connection(t, ev, "mcp", "local")
	if local.State != rpc.MCPConnected || local.Offered != 1 || policy(local, "echo") != rpc.PolicyOff {
		t.Fatalf("local = %+v; want connected, 1 tool, echo off", local)
	}
	if len(ev.Catalog) == 0 {
		t.Error("no catalog entries")
	}

	steps := []struct {
		policy      string
		wantAllow   []string
		wantConfirm []string
		wantTool    bool // the model gets local.echo
	}{
		{rpc.PolicyAsk, []string{"echo"}, []string{"echo"}, true},
		{rpc.PolicyAllow, []string{"echo"}, nil, true},
		{rpc.PolicyOff, nil, nil, false},
	}
	for _, st := range steps {
		t.Run(st.policy, func(t *testing.T) {
			req := rpc.Request{Op: rpc.OpToolPolicy, Policy: &rpc.PolicyChange{Kind: "mcp", Server: "local", Tool: "echo", Policy: st.policy}}
			ev := eventOf(t, mustCall(t, d.sock, req), rpc.EventConnections)
			if got := policy(connection(t, ev, "mcp", "local"), "echo"); got != st.policy {
				t.Errorf("echo is %q, want %q", got, st.policy)
			}
			cfg := loadConfig(t, dir)
			srv := cfg.MCP.Servers[0]
			if !slices.Equal(srv.Allow, st.wantAllow) || !slices.Equal(srv.Confirm, st.wantConfirm) {
				t.Errorf("allow %v confirm %v, want %v and %v", srv.Allow, srv.Confirm, st.wantAllow, st.wantConfirm)
			}
			names := toolNamesOf(mcpServers(t, call(t, d.sock, rpc.Request{Op: rpc.OpTools})))
			if got := slices.Contains(names, "local.echo"); got != st.wantTool {
				t.Errorf("the model gets local.echo: %v, want %v (tools %v)", got, st.wantTool, names)
			}
		})
	}

	t.Run("built-in", func(t *testing.T) {
		req := rpc.Request{Op: rpc.OpToolPolicy, Policy: &rpc.PolicyChange{Kind: "builtin", Server: "meru", Tool: "web_fetch", Policy: rpc.PolicyAsk}}
		ev := eventOf(t, mustCall(t, d.sock, req), rpc.EventConnections)
		if got := policy(connection(t, ev, "builtin", "meru"), "web_fetch"); got != rpc.PolicyAsk {
			t.Errorf("web_fetch is %q, want ask", got)
		}
		cfg := loadConfig(t, dir)
		if !slices.Contains(cfg.Builtin.Tools, "web_fetch") || !slices.Contains(cfg.Builtin.Confirm, "web_fetch") {
			t.Errorf("[builtin] = %+v, want web_fetch in tools and confirm", cfg.Builtin)
		}
		req.Policy.Policy = rpc.PolicyOff
		mustCall(t, d.sock, req)
		for _, s := range call(t, d.sock, rpc.Request{Op: rpc.OpTools})[0].Servers {
			if s.Kind == "builtin" && slices.ContainsFunc(s.Tools, func(ti rpc.ToolInfo) bool { return ti.Name == "web_fetch" }) {
				t.Error("web_fetch still offered after it was turned off")
			}
		}
	})

	refusals := []struct {
		name   string
		change rpc.PolicyChange
		want   string
	}{
		{"a tool the server doesn't offer", rpc.PolicyChange{Kind: "mcp", Server: "local", Tool: "nope", Policy: rpc.PolicyAllow}, "doesn't offer"},
		{"a server config doesn't have", rpc.PolicyChange{Kind: "mcp", Server: "ghost", Tool: "echo", Policy: rpc.PolicyAllow}, "no mcp source"},
		{"a policy that isn't one", rpc.PolicyChange{Kind: "mcp", Server: "local", Tool: "echo", Policy: "always"}, "isn't one of"},
		{"configure to allow", rpc.PolicyChange{Kind: "builtin", Server: "meru", Tool: "configure", Policy: rpc.PolicyAllow}, "always asks"},
		{"a local command", rpc.PolicyChange{Kind: "command", Server: "meru", Tool: "x", Policy: rpc.PolicyAllow}, "edit config.toml"},
	}
	for _, r := range refusals {
		t.Run(r.name, func(t *testing.T) {
			before, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
			change := r.change
			_, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpToolPolicy, Policy: &change}, rpc.ChoiceOnce)
			if !strings.Contains(msg, r.want) {
				t.Errorf("error = %q, want it to hold %q", msg, r.want)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
			if string(after) != string(before) {
				t.Error("a refused change wrote config.toml")
			}
		})
	}
}

func TestCatalogOps(t *testing.T) {
	dir, _ := meruHome(t)
	// An empty PATH keeps merud from starting the obsidian server's uvx,
	// which would fetch it from the network; the server just shows as not
	// connected.
	t.Setenv("PATH", dir)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})

	// obsidian needs an API key: without one nothing is written.
	for _, bad := range []struct {
		req  rpc.Request
		want string
	}{
		{rpc.Request{Op: rpc.OpMCPAdd, ID: "obsidian"}, "needs a key first"},
		{rpc.Request{Op: rpc.OpMCPAdd, ID: "nothing-here"}, "isn't in the catalog"},
	} {
		if _, msg := callErr(t, d.sock, bad.req, rpc.ChoiceOnce); !strings.Contains(msg, bad.want) {
			t.Errorf("add %s: %q, want %q", bad.req.ID, msg, bad.want)
		}
	}
	if n := len(loadConfig(t, dir).MCP.Servers); n != 0 {
		t.Fatalf("a refused add wrote %d servers", n)
	}

	// The app saves the key, then adds the server.
	const key = "k-123456"
	evs := mustCall(t, d.sock, rpc.Request{Op: rpc.OpSecretSet, ID: "obsidian_api_key", Text: key})
	evs = append(evs, mustCall(t, d.sock, rpc.Request{Op: rpc.OpMCPAdd, ID: "obsidian"})...)
	ev := eventOf(t, evs, rpc.EventConnections)
	o := connection(t, ev, "mcp", "obsidian")
	if o.State != rpc.MCPNotConnected || o.Offered != -1 || policy(o, "obsidian_simple_search") != rpc.PolicyAllow ||
		policy(o, "obsidian_append_content") != rpc.PolicyAsk {
		t.Errorf("obsidian = %+v", o)
	}
	cfg := loadConfig(t, dir)
	if len(cfg.MCP.Servers) != 1 || cfg.MCP.Servers[0].Name != "obsidian" {
		t.Fatalf("servers = %+v", cfg.MCP.Servers)
	}
	for _, c := range ev.Catalog {
		if c.Name == "obsidian" && (!c.Added || len(c.Needs) == 0 || !c.Needs[0].Saved) {
			t.Errorf("the catalog entry = %+v; want it added, with its key saved", c)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "secrets.toml"))
	if err != nil || !strings.Contains(string(raw), key) {
		t.Errorf("secrets.toml = %q, %v", raw, err)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpMCPAdd, ID: "obsidian"}, rpc.ChoiceOnce); !strings.Contains(msg, "already has") {
		t.Errorf("second add: %q", msg)
	}

	// A new key replaces the old one, only under a name something uses,
	// and no reply ever holds a key.
	const newKey = "k-654321"
	evs = append(evs, mustCall(t, d.sock, rpc.Request{Op: rpc.OpSecretSet, ID: "obsidian_api_key", Text: newKey})...)
	for _, ev := range evs {
		for _, k := range []string{key, newKey} {
			if strings.Contains(ev.Text, k) || strings.Contains(ev.Error, k) {
				t.Errorf("an event holds a key: %+v", ev)
			}
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "secrets.toml")); !strings.Contains(string(raw), newKey) || strings.Contains(string(raw), key) {
		t.Errorf("secrets.toml after the change = %q", raw)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpSecretSet, ID: "made_up", Text: "x"}, rpc.ChoiceOnce); !strings.Contains(msg, "no server uses") {
		t.Errorf("a made-up secret: %q", msg)
	}

	ev = eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpMCPRemove, ID: "obsidian"}), rpc.EventConnections)
	for _, c := range ev.Connections {
		if c.Name == "obsidian" {
			t.Error("obsidian still listed after its removal")
		}
	}
	if n := len(loadConfig(t, dir).MCP.Servers); n != 0 {
		t.Errorf("config has %d servers after the removal", n)
	}
}

func TestFolderOps(t *testing.T) {
	dir, home := meruHome(t)
	for _, sub := range []string{"Documents", "Notes"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"Notes/garden.md": "# Garden\n\nSow the tomatoes in April.", "Notes/.secret": "x", "Documents/a.txt": "alpha"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})

	ev := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpFolders}), rpc.EventFolders)
	if len(ev.Folders) != 0 {
		t.Errorf("folders = %+v, want none", ev.Folders)
	}
	var notes *rpc.FolderInfo
	for i, f := range ev.Suggested {
		if f.Path == "~/Notes" {
			notes = &ev.Suggested[i]
		}
	}
	if notes == nil || notes.Files != 1 || notes.More {
		t.Fatalf("suggested = %+v; want ~/Notes with 1 file (the hidden one skipped)", ev.Suggested)
	}

	ev = eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpFolderAdd, Path: filepath.Join(home, "Notes")}), rpc.EventFolders)
	if len(ev.Folders) != 1 || ev.Folders[0].Path != "~/Notes" {
		t.Fatalf("folders after the add = %+v", ev.Folders)
	}
	if got := loadConfig(t, dir).Index.Folders; !slices.Equal(got, []string{"~/Notes"}) {
		t.Errorf("[index] folders = %v", got)
	}
	waitUntil(t, "the new folder is indexed", func() bool { return status(t, d.sock).Documents == 1 })
	ev = eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpFolders}), rpc.EventFolders)
	if ev.Folders[0].Files != 1 {
		t.Errorf("~/Notes holds %d files in the index, want 1", ev.Folders[0].Files)
	}
	// The file tools come on with the first folder.
	var builtinTools []string
	for _, s := range call(t, d.sock, rpc.Request{Op: rpc.OpTools})[0].Servers {
		if s.Kind == "builtin" {
			builtinTools = toolNamesOf([]rpc.ServerInfo{s})
		}
	}
	if !slices.Contains(builtinTools, "read_file") {
		t.Errorf("built-in tools = %v, want read_file on", builtinTools)
	}

	for _, bad := range []struct{ path, want string }{
		{filepath.Join(home, "Notes"), "already indexed"},
		{filepath.Join(home, "Notes", "garden.md"), "isn't a folder"},
		{filepath.Join(home, "nowhere"), "doesn't exist"},
		{home, "whole home folder"},
		{dir, "Meru's own home"},
		{"relative/path", "isn't a full path"},
	} {
		if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpFolderAdd, Path: bad.path}, rpc.ChoiceOnce); !strings.Contains(msg, bad.want) {
			t.Errorf("add %s: %q, want %q", bad.path, msg, bad.want)
		}
	}

	mustCall(t, d.sock, rpc.Request{Op: rpc.OpFolderRemove, Path: "~/Notes"})
	if got := loadConfig(t, dir).Index.Folders; len(got) != 0 {
		t.Errorf("[index] folders after the removal = %v", got)
	}
	waitUntil(t, "the removed folder's files leave the index", func() bool { return status(t, d.sock).Documents == 0 })
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpFolderRemove, Path: "~/Notes"}, rpc.ChoiceOnce); !strings.Contains(msg, "isn't one of") {
		t.Errorf("second removal: %q", msg)
	}
}

func TestSkillToggleOps(t *testing.T) {
	dir, _ := meruHome(t)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})

	disabled := func(evs []rpc.Event) []string {
		var out []string
		for _, s := range eventOf(t, evs, rpc.EventSkills).Skills {
			if s.Disabled {
				out = append(out, s.Name)
			}
		}
		return out
	}
	if got := disabled(mustCall(t, d.sock, rpc.Request{Op: rpc.OpSkillDisable, ID: "explainer"})); !slices.Equal(got, []string{"explainer"}) {
		t.Errorf("disabled after disable = %v", got)
	}
	if got := loadConfig(t, dir).Skills.Disabled; !slices.Equal(got, []string{"explainer"}) {
		t.Errorf("[skills] disabled = %v", got)
	}
	if got := disabled(mustCall(t, d.sock, rpc.Request{Op: rpc.OpSkillEnable, ID: "explainer"})); len(got) != 0 {
		t.Errorf("disabled after enable = %v", got)
	}
	if got := loadConfig(t, dir).Skills.Disabled; len(got) != 0 {
		t.Errorf("[skills] disabled = %v", got)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpSkillDisable, ID: "no-such-skill"}, rpc.ChoiceOnce); !strings.Contains(msg, "no skill") {
		t.Errorf("disable an unknown skill: %q", msg)
	}
}

func TestSaveFileOp(t *testing.T) {
	dir, home := meruHome(t)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})
	session := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "What time is check-in at the Lisbon hotel?"}), rpc.EventSession).Session

	// write_file asks first; a yes saves the chat under chats/.
	evs, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpSaveFile, Kind: rpc.SaveChat, Session: session}, rpc.ChoiceOnce)
	if msg != "" {
		t.Fatalf("save chat: %s", msg)
	}
	path := eventOf(t, evs, rpc.EventSaved).Text
	want := filepath.Join(home, "meru-output", "chats", time.Now().Format("2006-01-02")+"-what-time-is-check-in-at.md")
	if path != want {
		t.Errorf("saved to %s, want %s", path, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "## What time is check-in at the Lisbon hotel?\n\npong") {
		t.Errorf("chat file = %q, %v", raw, err)
	}

	// A second save of the same chat gets a new name, never a replaced file.
	evs = mustCall(t, d.sock, rpc.Request{Op: rpc.OpSaveFile, Kind: rpc.SaveChat, Session: session})
	if got := eventOf(t, evs, rpc.EventSaved).Text; !strings.HasSuffix(got, "-at-2.md") {
		t.Errorf("second save went to %s", got)
	}

	// A note, and a no.
	evs = mustCall(t, d.sock, rpc.Request{Op: rpc.OpSaveFile, Kind: rpc.SaveNote, Session: session, Text: "# Check-in\n\nFrom 3 pm."})
	if got := eventOf(t, evs, rpc.EventSaved).Text; !strings.Contains(got, filepath.Join("notes", "")) || !strings.HasSuffix(got, "-check-in.md") {
		t.Errorf("note saved to %s", got)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpSaveFile, Kind: rpc.SaveNote, Session: session, Text: "x"}, rpc.ChoiceDeny); !strings.Contains(msg, "didn't allow") {
		t.Errorf("save after a no: %q", msg)
	}

	// Each save went through dispatch, so the audit log holds it.
	log := eventOf(t, call(t, d.sock, rpc.Request{Op: rpc.OpLog, Limit: 10}), rpc.EventLog).Log
	saves := 0
	for _, row := range log {
		if row.Tool == "write_file" {
			saves++
		}
	}
	if saves != 4 {
		t.Errorf("tool_calls holds %d write_file rows, want 4", saves)
	}

	for _, bad := range []rpc.Request{
		{Op: rpc.OpSaveFile, Kind: "pdf", Session: session},
		{Op: rpc.OpSaveFile, Kind: rpc.SaveNote, Session: session},
		{Op: rpc.OpSaveFile, Kind: rpc.SaveChat, Session: "not-a-session"},
	} {
		if _, msg := callErr(t, d.sock, bad, rpc.ChoiceOnce); msg == "" {
			t.Errorf("%+v saved, want an error", bad)
		}
	}
}

func TestModelsOp(t *testing.T) {
	dir, home := meruHome(t)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})
	m := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpModels}), rpc.EventModels).Models
	if m == nil || m.Profile != "lite" || m.Main == "" || m.Embed == "" || m.RuntimeVersion != "0.13.0" ||
		m.ConfigPath != filepath.Join(dir, "config.toml") || m.OutputDir != filepath.Join(home, "meru-output") {
		t.Errorf("models = %+v", m)
	}
}

func TestAskScope(t *testing.T) {
	dir, _ := meruHome(t)
	eng := &fakeEngine{version: "0.13.0"}
	d := startDaemon(t, dir, settingsHeader, eng)

	route := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "hello there", Scope: rpc.ScopeTalk}), rpc.EventRoute)
	if route.Route != "direct" || route.Confidence != 1 || route.Fallback {
		t.Errorf("route = %+v; want direct, set by the scope", route)
	}
	eng.mu.Lock()
	asked := eng.route
	eng.mu.Unlock()
	if asked != "" {
		t.Errorf("the router ran on a scoped turn: %q", asked)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "hi", Scope: "everywhere"}, rpc.ChoiceOnce); !strings.Contains(msg, "unknown scope") {
		t.Errorf("a bad scope: %q", msg)
	}
}

// TestAttachFileOp checks attach_file end to end: a file from outside
// every folder lands in the uploads folder, a second copy gets a new
// name, and each file merud must refuse leaves an error that says why.
func TestAttachFileOp(t *testing.T) {
	dir, home := meruHome(t)
	notes := filepath.Join(home, "Notes")
	away := filepath.Join(home, "Downloads")
	for _, d := range []string{notes, away} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"garden-plan.md": "Plant tomatoes in May.\n", ".env": "TOKEN=x\n", "big.md": ""} {
		if err := os.WriteFile(filepath.Join(away, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A sparse file one byte over the 50 MiB cap takes no disk space.
	if err := os.Truncate(filepath.Join(away, "big.md"), 50<<20+1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(away, "garden-plan.md"), filepath.Join(away, "link.md")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	header := settingsHeader + "folders = [" + strconv.Quote(notes) + "]\n"
	d := startDaemon(t, dir, header, &fakeEngine{version: "0.13.0"})
	uploads := filepath.Join(home, "meru-output", "uploads")

	tests := []struct {
		name    string
		path    string
		want    string // the copy's path; "" when merud must refuse
		refusal string
	}{
		{"a file outside every folder", filepath.Join(away, "garden-plan.md"), filepath.Join(uploads, "garden-plan.md"), ""},
		{"the same file again", filepath.Join(away, "garden-plan.md"), filepath.Join(uploads, "garden-plan-2.md"), ""},
		{"symlink", filepath.Join(away, "link.md"), "", "symbolic link"},
		{"folder", away, "", "is a folder"},
		{"too big", filepath.Join(away, "big.md"), "", "up to 50.0 MB"},
		{"secret", filepath.Join(away, ".env"), "", "keys, passwords or tokens"},
		{"missing", filepath.Join(away, "gone.md"), "", "isn't there any more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpAttachFile, Path: tt.path}, rpc.ChoiceDeny)
			if tt.refusal != "" {
				if !strings.Contains(msg, tt.refusal) {
					t.Errorf("attach %s: error %q, want %q", tt.path, msg, tt.refusal)
				}
				return
			}
			if msg != "" {
				t.Fatalf("attach %s: %s", tt.path, msg)
			}
			if got := eventOf(t, evs, rpc.EventSaved).Text; got != tt.want {
				t.Errorf("copied to %s, want %s", got, tt.want)
			}
			if raw, err := os.ReadFile(tt.want); err != nil || string(raw) != "Plant tomatoes in May.\n" {
				t.Errorf("copy = %q, %v", raw, err)
			}
		})
	}
}
