//go:build e2e

// This file tests `meru mcp add` and `meru mcp remove` end to end: meru asks
// a real merud to probe cmd/fakemcp, proposes a split from its hints,
// writes the entry, and merud reloads, so `meru tools` shows the server with
// no restart. Removing it takes it out of config and out of merud.

package e2e

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

func TestMCPAddAndRemove(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	fakemcp := filepath.Join(binDir, "fakemcp")

	// d: do it for me; Enter: accept the proposal; y: write it.
	add := runMeruInput(t, s.home, "d\n\ny\n", "mcp", "add", "stdio", "notes", "--", fakemcp)
	if add.code != 0 {
		t.Fatalf("meru mcp add exited %d:\n%s%s\nmerud.log:\n%s", add.code, add.stdout, add.stderr, s.home.log())
	}
	for _, want := range []string{"read-only", "may delete", "No restart needed", "connected"} {
		if !strings.Contains(add.stdout, want) {
			t.Errorf("meru mcp add output lacks %q:\n%s", want, add.stdout)
		}
	}
	cfg, err := config.Load(s.home.config)
	if err != nil {
		t.Fatal(err)
	}
	notes := cfg.MCP.Servers[len(cfg.MCP.Servers)-1]
	// search says read-only, so it runs without asking. send says destructive, and
	// secret says nothing, so both ask first.
	if !slices.Equal(notes.Allow, []string{"search", "secret", "send"}) || !slices.Equal(notes.Confirm, []string{"secret", "send"}) {
		t.Errorf("allow %q confirm %q", notes.Allow, notes.Confirm)
	}

	tools := runMeru(t, s.home, "tools")
	if !strings.Contains(tools.stdout, "notes.search") {
		t.Errorf("meru tools doesn't show the new server without a restart:\n%s%s", tools.stdout, tools.stderr)
	}

	remove := runMeru(t, s.home, "mcp", "remove", "--yes", "notes")
	if remove.code != 0 || !strings.Contains(remove.stdout, "reloaded") {
		t.Fatalf("meru mcp remove exited %d:\n%s%s", remove.code, remove.stdout, remove.stderr)
	}
	tools = runMeru(t, s.home, "tools")
	if strings.Contains(tools.stdout, "notes.search") {
		t.Errorf("meru tools still shows the removed server:\n%s", tools.stdout)
	}
}
