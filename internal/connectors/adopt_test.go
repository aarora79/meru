// This file tests Adopt and Unadopt on real-shaped config files: an npx
// obsidian-mcp entry with --vault, and a google HTTP entry with the start
// script docs/google-setup.md has the user write and a launchd job that a
// fake launchctl plays. Every value is invented, and the Google tests
// use a manifest moved to a free port, so no test ever looks at port
// 8000, where the user's own server may run.

package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// fakeLaunchctl plays launchctl for one job: print succeeds while it is
// loaded, bootout unloads it and bootstrap loads it. It records each call.
type fakeLaunchctl struct {
	mu     sync.Mutex
	loaded bool
	calls  []string
}

// run is the fake's Runner.
func (f *fakeLaunchctl) run(_ context.Context, c Cmd, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Path != launchctlPath {
		return "", fmt.Errorf("the fake runs launchctl only, not %s", c.Path)
	}
	f.calls = append(f.calls, strings.Join(c.Args, " "))
	switch c.Args[0] {
	case "print":
		if !f.loaded {
			return "", errors.New("Could not find service")
		}
	case "bootout":
		f.loaded = false
	case "bootstrap":
		f.loaded = true
	}
	return "", nil
}

// testAdopter returns an Adopter over config text body, in a temporary
// home, with the fake launchctl and a fixed date.
func testAdopter(t *testing.T, body string, osName string, lc *fakeLaunchctl) *Adopter {
	t.Helper()
	home := t.TempDir()
	meruDir := filepath.Join(home, ".meru")
	if err := os.MkdirAll(meruDir, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &Adopter{
		ConfigPath:  filepath.Join(meruDir, "config.toml"),
		SecretsPath: secrets.Path(meruDir),
		Home:        home,
		Run:         lc.run,
		UID:         501,
		OS:          osName,
		Now:         func() time.Time { return time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC) },
	}
	if err := os.WriteFile(a.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return a
}

// fileText returns the file at path as a string.
func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// obsidianEntry is an [[mcp.servers]] entry that runs obsidian-mcp with
// npx, as a user who set it up by hand has it, for the vault folder
// vault. Its allow list adds a tool the manifest's leaves out.
func obsidianEntry(vault string) string {
	return fmt.Sprintf(`[models]
main = "qwen3.6:35b-a3b-mxfp8"

# My notes.
[[mcp.servers]]
name    = "obsidian"
command = "npx"
args    = ["-y", "obsidian-mcp", "serve", "--vault", "notes=%s"]
allow   = ["obsidian_list_vaults", "obsidian_search_vault", "obsidian_read_note", "obsidian_create_note"]
confirm = ["obsidian_create_note"]

[index]
folders = []
`, vault)
}

// TestAdoptObsidian adopts an npx obsidian-mcp entry, checks the table it
// writes and that the supervisor accepts it, and undoes it.
func TestAdoptObsidian(t *testing.T) {
	vault := t.TempDir()
	body := obsidianEntry(vault)
	lc := &fakeLaunchctl{}
	a := testAdopter(t, body, "darwin", lc)
	m := obsidian(t)
	ctx := context.Background()

	plan, err := a.PlanAdopt(ctx, m, nil)
	if err != nil {
		t.Fatalf("PlanAdopt: %v", err)
	}
	text := strings.Join(plan.Changes, "\n")
	for _, want := range []string{
		"Comment out the obsidian entry",
		"[connectors.obsidian]",
		`vault_path = "` + vault + `"`,
		`vault_name = "notes"`,
		`allow = ["obsidian_list_vaults", "obsidian_search_vault", "obsidian_read_note", "obsidian_create_note"]`,
		`confirm = ["obsidian_create_note"]`,
		"merud installs Obsidian 2.0.1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan doesn't say %q:\n%s", want, text)
		}
	}
	if fileText(t, a.ConfigPath) != body {
		t.Fatal("PlanAdopt changed config.toml")
	}

	reloads := 0
	if err := a.Adopt(ctx, plan, func() error { reloads++; return nil }); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if reloads != 1 {
		t.Errorf("reloaded %d times, want 1", reloads)
	}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(cfg.MCP.Servers, func(s config.MCPServer) bool { return s.Name == "obsidian" }) {
		t.Error("the obsidian entry is still live")
	}
	// The supervisor takes the table as it is, with the entry's lists.
	st := checkSettings(m, cfg.Connectors["obsidian"], nil, a.Home)
	if !st.enabled || st.problem != "" || st.values["vault_name"] != "notes" ||
		!slices.Contains(st.tools.Allow, "obsidian_create_note") || !slices.Equal(st.tools.Confirm, []string{"obsidian_create_note"}) {
		t.Errorf("the supervisor sees %+v", st)
	}
	if len(lc.calls) != 0 {
		t.Errorf("Obsidian's adopt ran launchctl: %v", lc.calls)
	}

	// Again: nothing to do.
	again, err := a.PlanAdopt(ctx, m, nil)
	if err != nil || !again.Nothing {
		t.Errorf("a second PlanAdopt = %+v, %v; want nothing to do", again, err)
	}

	undo, err := a.PlanUnadopt(ctx, m)
	if err != nil {
		t.Fatalf("PlanUnadopt: %v", err)
	}
	if err := a.Unadopt(ctx, undo, func() error { reloads++; return nil }); err != nil {
		t.Fatalf("Unadopt: %v", err)
	}
	if got := fileText(t, a.ConfigPath); got != body {
		t.Errorf("after unadopt config.toml differs:\n%s\nwant:\n%s", got, body)
	}
	if again, err := a.PlanUnadopt(ctx, m); err != nil || !again.Nothing {
		t.Errorf("a second PlanUnadopt = %+v, %v; want nothing to do", again, err)
	}
}

// TestAdoptObsidianTildeVault checks an entry whose vault folder starts
// with "~/": Adopt finds the folder under the home folder and writes the
// path as the entry had it, which the supervisor reads the same way.
func TestAdoptObsidianTildeVault(t *testing.T) {
	a := testAdopter(t, "", "linux", &fakeLaunchctl{})
	if err := os.MkdirAll(filepath.Join(a.Home, "Notes", "vault"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[[mcp.servers]]\nname = \"obsidian\"\ncommand = \"npx\"\nargs = [\"-y\", \"obsidian-mcp\", \"serve\", \"--vault\", \"notes=~/Notes/vault\"]\n"
	if err := os.WriteFile(a.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := a.PlanAdopt(context.Background(), obsidian(t), nil)
	if err != nil {
		t.Fatalf("PlanAdopt: %v", err)
	}
	if !strings.Contains(strings.Join(plan.Changes, "\n"), `vault_path = "~/Notes/vault"`) {
		t.Errorf("plan:\n%s", strings.Join(plan.Changes, "\n"))
	}
}

// TestAdoptObsidianRefusals checks the entries Adopt leaves alone, each
// with a reason, and that none of them changes config.toml.
func TestAdoptObsidianRefusals(t *testing.T) {
	vault := t.TempDir()
	entry := func(lines string) string {
		return "[[mcp.servers]]\nname = \"obsidian\"\n" + lines + "allow = [\"x\"]\n"
	}
	tests := []struct{ name, body, want string }{
		{"the REST API server", entry("command = \"uvx\"\nargs = [\"mcp-obsidian\"]\nenv = { OBSIDIAN_API_KEY = \"secret:obsidian_api_key\" }\n"),
			"runs mcp-obsidian, which reaches Obsidian through its Local REST API plugin"},
		{"two vaults", entry(fmt.Sprintf("command = \"npx\"\nargs = [\"obsidian-mcp\", \"serve\", \"--vault\", \"a=%s\", \"--vault\", \"b=%s\"]\n", vault, vault)),
			"names 2 vaults"},
		{"no vault", entry("command = \"npx\"\nargs = [\"-y\", \"obsidian-mcp\", \"serve\"]\n"), "names no vault"},
		{"a vault folder that isn't there", entry("command = \"npx\"\nargs = [\"obsidian-mcp\", \"serve\", \"--vault\", \"notes=/no/such/folder\"]\n"),
			"the vault folder /no/such/folder doesn't exist"},
		{"a vault name obsidian-mcp refuses", entry(fmt.Sprintf("command = \"npx\"\nargs = [\"obsidian-mcp\", \"serve\", \"--vault\", \"My Notes=%s\"]\n", vault)),
			`the vault name "My Notes" doesn't fit`},
		{"env", entry(fmt.Sprintf("command = \"npx\"\nargs = [\"obsidian-mcp\", \"serve\", \"--vault\", \"n=%s\"]\nenv = { DEBUG = \"1\" }\n", vault)),
			"sets env (DEBUG)"},
		{"some other program", entry("command = \"/usr/local/bin/notes-server\"\n"), "not obsidian-mcp through npx or node"},
		{"an entry and a table both", obsidianEntry(vault) + "\n[connectors.obsidian]\nenabled = false\n", "has both the obsidian entry"},
		{"nothing to adopt", "[index]\nfolders = []\n", "no obsidian entry in [[mcp.servers]]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testAdopter(t, tt.body, "linux", &fakeLaunchctl{})
			_, err := a.PlanAdopt(context.Background(), obsidian(t), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("PlanAdopt = %v, want an error with %q", err, tt.want)
			}
			if fileText(t, a.ConfigPath) != tt.body {
				t.Error("a refused adopt changed config.toml")
			}
		})
	}
}

// freeGoogle returns the Google manifest moved to a free loopback port,
// and that port.
func freeGoogle(t *testing.T) (Manifest, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	m := googleManifest(t)
	m.Launch.URL = fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	m.Launch.Port = port
	return m, port
}

// googleEntry is an [[mcp.servers]] entry for a Google server the user
// runs at port, as the catalog writes it.
func googleEntry(port int) string {
	return fmt.Sprintf(`# Gmail, Google Calendar, Drive and Docs
[[mcp.servers]]
name    = "google"
url     = "http://127.0.0.1:%d/mcp"
allow   = ["search_gmail_messages", "get_gmail_message_content", "get_gmail_thread_content", "send_gmail_message", "get_events", "manage_event", "search_drive_files", "get_doc_content", "get_gmail_attachment_content"]
confirm = ["send_gmail_message", "manage_event"]
`, port)
}

// startScript is a start script in the form the Mac installer writes,
// with invented values.
const startScript = `#!/bin/sh
# Starts the Google server Meru connects to. Keep this file private.
export GOOGLE_OAUTH_CLIENT_ID='123-invented.apps.googleusercontent.com'
export GOOGLE_OAUTH_CLIENT_SECRET='invented-secret'
export USER_GOOGLE_EMAIL='dana@example.com'
export WORKSPACE_ATTACHMENT_DIR="$HOME/meru-output/attachments"
exec uvx workspace-mcp==1.30.0 --transport streamable-http \
  --tool-tier extended --tools gmail calendar drive docs
`

// writeHomeFile writes body to the file rel under a's home folder.
func writeHomeFile(t *testing.T, a *Adopter, rel, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(a.Home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAdoptGoogle adopts a google entry whose server a launchd job runs:
// Adopt reads the start script, saves the secret with mode 0600, stops
// the job and sets its file aside; Unadopt puts all of it back.
func TestAdoptGoogle(t *testing.T) {
	m, port := freeGoogle(t)
	body := googleEntry(port)
	lc := &fakeLaunchctl{loaded: true}
	a := testAdopter(t, body, "darwin", lc)
	writeHomeFile(t, a, ".config/workspace-mcp/start.sh", startScript, 0o700)
	plist := writeHomeFile(t, a, "Library/LaunchAgents/com.meru.workspace-mcp.plist", "<plist/>\n", 0o644)
	ctx := context.Background()

	plan, err := a.PlanAdopt(ctx, m, nil)
	if err != nil {
		t.Fatalf("PlanAdopt: %v", err)
	}
	text := strings.Join(plan.Changes, "\n")
	for _, want := range []string{
		`email = "dana@example.com"`,
		`client_id = "123-invented.apps.googleusercontent.com"`,
		"as connector_google_client_secret",
		"Stop the launchd job com.meru.workspace-mcp: launchctl bootout gui/501/com.meru.workspace-mcp.",
		"Rename ~/Library/LaunchAgents/com.meru.workspace-mcp.plist to com.meru.workspace-mcp.plist.disabled",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan doesn't say %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "invented-secret") {
		t.Error("the plan shows the client secret")
	}
	// The entry's lists match the manifest's, so the table has none.
	if strings.Contains(text, "allow =") {
		t.Errorf("the table repeats the manifest's lists:\n%s", text)
	}

	if err := a.Adopt(ctx, plan, func() error { return nil }); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if lc.loaded {
		t.Error("the launchd job still runs")
	}
	if _, err := os.Stat(plist); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the plist is still in place: %v", err)
	}
	if _, err := os.Stat(plist + ".disabled"); err != nil {
		t.Errorf("no .disabled plist: %v", err)
	}
	info, err := os.Stat(a.SecretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets.toml has mode %o, want 600", perm)
	}
	sec, err := secrets.Load(a.SecretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := sec.Resolve("secret:connector_google_client_secret"); err != nil || v != "invented-secret" {
		t.Errorf("secret = %q, %v", v, err)
	}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	st := checkSettings(m, cfg.Connectors["google"], sec, a.Home)
	if !st.enabled || st.problem != "" || st.values["client_secret"] != "invented-secret" {
		t.Errorf("the supervisor sees enabled %v, problem %q", st.enabled, st.problem)
	}

	undo, err := a.PlanUnadopt(ctx, m)
	if err != nil {
		t.Fatalf("PlanUnadopt: %v", err)
	}
	if !strings.Contains(strings.Join(undo.Changes, "\n"), "start the launchd job com.meru.workspace-mcp again") {
		t.Errorf("the undo plan doesn't restart the job:\n%s", strings.Join(undo.Changes, "\n"))
	}
	if err := a.Unadopt(ctx, undo, func() error { return nil }); err != nil {
		t.Fatalf("Unadopt: %v", err)
	}
	if got := fileText(t, a.ConfigPath); got != body {
		t.Errorf("after unadopt config.toml differs:\n%s\nwant:\n%s", got, body)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Errorf("the plist didn't come back: %v", err)
	}
	if !lc.loaded {
		t.Error("the launchd job didn't start again")
	}
	want := []string{
		"print gui/501/com.meru.workspace-mcp",
		"bootout gui/501/com.meru.workspace-mcp",
		"print gui/501/com.meru.workspace-mcp",
		"bootstrap gui/501 " + plist,
	}
	if !slices.Equal(lc.calls, want) {
		t.Errorf("launchctl calls = %q, want %q", lc.calls, want)
	}
}

// TestAdoptGoogleByHand checks the owner's case: a server started by hand
// with no launchd job and no start script. Adopt refuses while the port
// is taken, and without the values, and works once both are dealt with.
func TestAdoptGoogleByHand(t *testing.T) {
	m, port := freeGoogle(t)
	body := googleEntry(port)
	lc := &fakeLaunchctl{}
	a := testAdopter(t, body, "darwin", lc)
	ctx := context.Background()

	// No start script and no values: say which to give.
	_, err := a.PlanAdopt(ctx, m, nil)
	if err == nil || !strings.Contains(err.Error(), "found no ~/.config/workspace-mcp/start.sh") ||
		!strings.Contains(err.Error(), "meru mcp adopt google --email <your Google address> --client-id <your client ID>") {
		t.Errorf("PlanAdopt with nothing to read = %v", err)
	}
	values := map[string]string{
		"email":         "dana@example.com",
		"client_id":     "123-invented.apps.googleusercontent.com",
		"client_secret": "invented-secret",
	}

	// The server the user started by hand holds the port.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.PlanAdopt(ctx, m, values)
	if err == nil || !strings.Contains(err.Error(), "isn't the launchd job com.meru.workspace-mcp") ||
		!strings.Contains(err.Error(), "Meru never stops a program it didn't start") {
		t.Errorf("PlanAdopt with the port taken = %v", err)
	}
	_ = l.Close()

	plan, err := a.PlanAdopt(ctx, m, values)
	if err != nil {
		t.Fatalf("PlanAdopt once the port is free: %v", err)
	}
	if strings.Contains(strings.Join(plan.Changes, "\n"), "launchctl bootout") {
		t.Error("the plan stops a launchd job that isn't there")
	}
	if err := a.Adopt(ctx, plan, func() error { return nil }); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if slices.ContainsFunc(lc.calls, func(c string) bool { return !strings.HasPrefix(c, "print") }) {
		t.Errorf("launchctl calls = %q; want only print", lc.calls)
	}
	undo, err := a.PlanUnadopt(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Unadopt(ctx, undo, func() error { return nil }); err != nil {
		t.Fatalf("Unadopt: %v", err)
	}
	if got := fileText(t, a.ConfigPath); got != body {
		t.Errorf("after unadopt config.toml differs:\n%s", got)
	}
}

// TestAdoptGoogleRefusals checks the google entries Adopt leaves alone.
func TestAdoptGoogleRefusals(t *testing.T) {
	m, port := freeGoogle(t)
	tests := []struct{ name, body, script, want string }{
		{"another address", googleEntry(port + 1), startScript, "the Google connector answers at"},
		{"a stdio entry", "[[mcp.servers]]\nname = \"google\"\ncommand = \"uvx\"\nargs = [\"workspace-mcp\"]\n", startScript, "as a stdio server"},
		{"a start script on another port", googleEntry(port), startScript + "export WORKSPACE_MCP_PORT=8001\n", "runs the server on port 8001"},
		{"a start script with no secret", googleEntry(port), strings.Replace(startScript, "export GOOGLE_OAUTH_CLIENT_SECRET='invented-secret'\n", "", 1),
			"couldn't find all of them in ~/.config/workspace-mcp/start.sh"},
		{"a client ID of the wrong shape", googleEntry(port), strings.Replace(startScript, ".apps.googleusercontent.com", "", 1), "doesn't fit the form Google needs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testAdopter(t, tt.body, "linux", &fakeLaunchctl{})
			writeHomeFile(t, a, ".config/workspace-mcp/start.sh", tt.script, 0o700)
			_, err := a.PlanAdopt(context.Background(), m, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("PlanAdopt = %v, want an error with %q", err, tt.want)
			}
			if fileText(t, a.ConfigPath) != tt.body {
				t.Error("a refused adopt changed config.toml")
			}
		})
	}
}

// TestAdoptRollsBackTheJob checks that a failed config edit starts the
// launchd job again, so the old server runs as before.
func TestAdoptRollsBackTheJob(t *testing.T) {
	m, port := freeGoogle(t)
	lc := &fakeLaunchctl{loaded: true}
	a := testAdopter(t, googleEntry(port), "darwin", lc)
	writeHomeFile(t, a, ".config/workspace-mcp/start.sh", startScript, 0o700)
	plist := writeHomeFile(t, a, "Library/LaunchAgents/com.meru.workspace-mcp.plist", "<plist/>\n", 0o644)
	plan, err := a.PlanAdopt(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Someone adds [connectors.google] between the plan and the adopt.
	if err := os.WriteFile(a.ConfigPath, []byte(googleEntry(port)+"\n[connectors.google]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Adopt(context.Background(), plan, func() error { return nil }); err == nil {
		t.Fatal("Adopt worked over a config with [connectors.google]")
	}
	if !lc.loaded {
		t.Error("the launchd job wasn't started again")
	}
	if _, err := os.Stat(plist); err != nil {
		t.Errorf("the plist wasn't put back: %v", err)
	}
}

// TestReadStartScript checks the forms of export line the start script
// reader takes, and that it runs nothing.
func TestReadStartScript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "start.sh")
	body := "#!/bin/sh\n" +
		"export A='single quoted'\n" +
		"export B=\"double\"\n" +
		"export C=bare # a comment\n" +
		"export D=\"$HOME/needs-a-shell\"\n" +
		"  export E='indented'\n" +
		"F=not-exported\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readStartScript(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "single quoted", "B": "double", "C": "bare", "E": "indented"}
	if len(got) != len(want) {
		t.Errorf("read %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
