//go:build e2e

// This file tests the connector supervisor end to end: a real merud runs
// the Obsidian connector, with cmd/fakemcp standing in for the Obsidian
// server. The test lays out ~/.meru/runtime as a finished install would,
// so nothing downloads, and checks what the connectors op, `meru mcp` and
// a tool call see: needs_config, then ok after the vault is set, then a
// killed program coming back on its own.

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// fakeObsidianInstall lays out what installing the Obsidian connector
// leaves under dir/runtime: the pinned Node's folder, whose bin/node is
// cmd/fakemcp, and the package folder with its marker and package.json.
// merud then counts the connector as installed and starts fakemcp in
// place of Node.
func fakeObsidianInstall(t *testing.T, dir string) {
	t.Helper()
	all, err := connectors.Load()
	if err != nil {
		t.Fatal(err)
	}
	var m connectors.Manifest
	for _, one := range all {
		if one.ID == "obsidian" {
			m = one
		}
	}
	node := connectors.Runtimes()[connectors.RuntimeNode].DirName()
	runtime := filepath.Join(dir, "runtime")
	bin := filepath.Join(runtime, node, "bin")
	pkg := filepath.Join(runtime, "pkg", "obsidian-"+m.Install.Version)
	script := filepath.Join(pkg, "node_modules", m.Install.Package, "build")
	for _, d := range []string{bin, script} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(binDir, "fakemcp"), filepath.Join(bin, "node")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(pkg, ".meru-installed"):                                 fmt.Sprintf(`{"id":"obsidian","version":%q,"runtime":%q}`, m.Install.Version, node),
		filepath.Join(pkg, "node_modules", m.Install.Package, "package.json"): `{"bin":{"obsidian-mcp":"build/main.js"}}`,
		filepath.Join(script, "main.js"):                                      "",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// obsidianStatus returns merud's connectors-op row for obsidian.
func obsidianStatus(t *testing.T, socket string) rpc.ConnectorStatus {
	t.Helper()
	for _, ev := range op(t, socket, rpc.Request{Op: rpc.OpConnectors}) {
		for _, c := range ev.Connectors {
			if c.ID == "obsidian" {
				return c
			}
		}
	}
	t.Fatal("the connectors op has no obsidian")
	return rpc.ConnectorStatus{}
}

// waitSentence waits up to 20 seconds for obsidian's sentence to be want.
func waitSentence(t *testing.T, h *home, want string) rpc.ConnectorStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := obsidianStatus(t, h.socket)
		if st.Sentence == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("obsidian = %s %q, want %q\nmerud.log:\n%s", st.State, st.Sentence, want, h.log())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// listVaults asks one question whose answer calls
// obsidian.obsidian_list_vaults, and returns the process ID fakemcp put in
// the result.
func listVaults(t *testing.T, s *stack) int {
	t.Helper()
	before := len(s.fake.chatRequests(t, mainModel))
	s.fake.enqueue(t, fastModel, toolsRoute())
	s.fake.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "obsidian.obsidian_list_vaults", Arguments: map[string]any{}}}},
		fakeollama.Reply{Text: "You have one vault."})
	res := runMeru(t, s.home, "which obsidian vaults do I have?")
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s\nmerud.log:\n%s", res.code, res.stderr, s.home.log())
	}
	pidRE := regexp.MustCompile(`vault notes \(pid (\d+)\)`)
	for _, r := range s.fake.chatRequests(t, mainModel)[before:] {
		if m := pidRE.FindStringSubmatch(string(r.Body)); m != nil {
			pid, _ := strconv.Atoi(m[1])
			return pid
		}
	}
	t.Fatalf("no request to the model carried the tool's result\nmerud.log:\n%s", s.home.log())
	return 0
}

// TestConnectorLifecycle runs the Obsidian connector through merud: it
// needs its vault folder, is ready once config names one, starts on the
// first call, and comes back after its program is killed.
func TestConnectorLifecycle(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	fakeObsidianInstall(t, h.dir)
	base := fakeConfig(f.url, "[router]\ntemperature = 1.0\n\n[connectors.obsidian]\nenabled = true\n")
	h.writeConfig(t, base)
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)
	s := &stack{home: h, fake: f, merud: m}

	st := obsidianStatus(t, h.socket)
	if st.State != rpc.ConnectorNeedsConfig || st.Sentence != "Obsidian needs your vault folder." ||
		len(st.Fix) != 1 || st.Fix[0] != "vault_path" {
		t.Fatalf("obsidian = %+v, want needs_config for vault_path", st)
	}
	status := runMeru(t, h, "mcp", "status")
	for _, want := range []string{"needs config", "Set vault_path under [connectors.obsidian]"} {
		if !strings.Contains(status.stdout, want) {
			t.Errorf("meru mcp status lacks %q:\n%s", want, status.stdout)
		}
	}

	// Name the vault and reload: merud checks the program once and keeps
	// its tools, and the connector is ready without running.
	vault := t.TempDir()
	h.writeConfig(t, strings.Replace(base, "enabled = true\n", fmt.Sprintf("enabled = true\nvault_path = %q\n", vault), 1))
	op(t, h.socket, rpc.Request{Op: rpc.OpMCPReload})
	st = waitSentence(t, h, "Obsidian is ready. It starts when a question needs it.")
	if st.State != rpc.ConnectorOK {
		t.Errorf("state = %s, want ok", st.State)
	}
	tools := runMeru(t, h, "tools")
	if !strings.Contains(tools.stdout, "obsidian.obsidian_list_vaults") {
		t.Errorf("meru tools doesn't offer the ready connector's tools:\n%s", tools.stdout)
	}

	// The first call starts the program.
	pid := listVaults(t, s)
	waitSentence(t, h, "Obsidian is running.")

	// Kill it: merud notices, waits a second and starts it again.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "merud to see the crash", func() bool {
		return strings.Contains(h.log(), `msg="connector stopped on its own"`)
	})
	waitSentence(t, h, "Obsidian is running.")
	if again := listVaults(t, s); again == pid {
		t.Errorf("the tool answered from the killed process %d", pid)
	}
}

// TestConnectorSetUpByHand checks the rule that keeps a working setup
// working: an [[mcp.servers]] entry named obsidian wins over the
// connector, runs as before, and the connector says it is set up by hand.
func TestConnectorSetUpByHand(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	fakeObsidianInstall(t, h.dir)
	h.writeConfig(t, fakeConfig(f.url, fmt.Sprintf(`[router]
temperature = 1.0

[connectors.obsidian]
enabled    = true
vault_path = %q

[[mcp.servers]]
name    = "obsidian"
command = %q
allow   = ["search"]
`, t.TempDir(), filepath.Join(binDir, "fakemcp"))))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	st := obsidianStatus(t, h.socket)
	if st.State != rpc.ConnectorByHand || st.Sentence != "Obsidian is set up by hand, as the obsidian entry in [[mcp.servers]]." {
		t.Errorf("obsidian = %s %q, want by_hand", st.State, st.Sentence)
	}
	tools := runMeru(t, h, "tools")
	if !strings.Contains(tools.stdout, "obsidian.search") || strings.Contains(tools.stdout, "obsidian.obsidian_list_vaults") {
		t.Errorf("meru tools should show the hand-added server's tools only:\n%s", tools.stdout)
	}
	status := runMeru(t, h, "mcp", "status")
	if !strings.Contains(status.stdout, "connected") || !strings.Contains(status.stdout, "set up by hand") {
		t.Errorf("meru mcp status:\n%s", status.stdout)
	}
}
