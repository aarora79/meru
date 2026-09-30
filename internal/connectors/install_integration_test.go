//go:build integration

// This file holds the one test that downloads for real: the pinned Node
// from nodejs.org, then obsidian-mcp 2.0.1 from the npm registry, into a
// temporary Meru home. It runs only with the integration build tag:
//
//	go test -tags integration -run TestIntegrationObsidian ./internal/connectors/
//
// CI never runs it, since it needs the internet and about 60 MB.

package connectors

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestIntegrationObsidian installs the real obsidian connector, then runs
// its program with the pinned Node and asks it for its help text.
func TestIntegrationObsidian(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m Manifest) bool { return m.ID == "obsidian" })
	m := ms[i]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	meruDir := filepath.Join(t.TempDir(), ".meru")
	in := NewInstaller(meruDir, t.TempDir())
	inst, err := in.Install(ctx, m, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Logf("installed %+v", inst)

	vault := t.TempDir()
	path, args, env, err := in.LaunchCommand(m, inst, map[string]string{"vault_path": vault, "vault_name": "notes"})
	if err != nil {
		t.Fatalf("LaunchCommand: %v", err)
	}
	// Run the script with --help in place of serve and its arguments,
	// so it prints and exits instead of waiting on stdin for MCP.
	c := Cmd{Path: path, Args: []string{args[0], "--help"}, Env: []string{"HOME=" + t.TempDir(), "PATH=" + env["PATH"]}}
	out, err := in.Run(ctx, c, nil)
	if err != nil {
		t.Fatalf("run %s: %v\n%s", path, err, out)
	}
	if !strings.Contains(out, "serve") {
		t.Errorf("--help printed %q, want it to mention serve", out)
	}
	// Nothing landed outside the temporary Meru home.
	if _, err := os.Stat(filepath.Join(meruDir, "runtime", "cache", "npm")); err != nil {
		t.Errorf("npm's cache isn't inside the runtime folder: %v", err)
	}
}
