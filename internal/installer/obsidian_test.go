// This file tests the Obsidian step: the vault it hands to the Start Meru
// step, the refusals, and the Adopt choice for an entry set up by hand.

package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

func TestSetUpObsidian(t *testing.T) {
	p := tempHome(t)
	vault := filepath.Join(p.Home, "Notes")
	if err := os.Mkdir(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, p.Config())

	for _, v := range []string{vault, "~/Notes"} {
		msg, hand, err := SetUpObsidian(p, ByHand{}, ObsidianInput{Vault: v})
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if hand == nil || hand.ID != "obsidian" || hand.Change == nil || hand.Change.Values["vault_path"] != v ||
			hand.Change.Enabled == nil || !*hand.Change.Enabled {
			t.Errorf("%s: hand-off = %+v", v, hand)
		}
		if !strings.Contains(msg, "~/Notes") {
			t.Errorf("%s: result = %q", v, msg)
		}
	}
	for _, bad := range []string{"", "~/Nowhere", filepath.Join(p.Home, ".meru", "config.toml")} {
		if _, hand, err := SetUpObsidian(p, ByHand{}, ObsidianInput{Vault: bad}); err == nil || hand != nil {
			t.Errorf("vault %q passed", bad)
		}
	}
	if readFile(t, p.Config()) != before {
		t.Error("the step wrote config.toml; merud writes it on the hand-off")
	}

	msg, hand, err := SetUpObsidian(p, ByHand{Entry: true}, ObsidianInput{})
	if err != nil || hand != nil || !strings.Contains(msg, "stays as it is") {
		t.Errorf("keep: %q, %+v, %v", msg, hand, err)
	}
	if _, hand, _ = SetUpObsidian(p, ByHand{Entry: true}, ObsidianInput{Adopt: true}); hand == nil || !hand.Adopt || hand.Change != nil {
		t.Errorf("adopt: %+v", hand)
	}
}

func TestObsidianFound(t *testing.T) {
	byHand := config.Config{MCP: config.MCP{Servers: []config.MCPServer{{Name: "obsidian", Command: "npx"}}}}
	if got := ObsidianFound(byHand); !strings.Contains(got, "set up by hand") {
		t.Errorf("by hand: %q", got)
	}
	on := config.Config{Connectors: map[string]config.Connector{"obsidian": {"enabled": true, "vault_path": "~/Notes"}}}
	if got := ObsidianFound(on); !strings.Contains(got, "~/Notes") {
		t.Errorf("on: %q", got)
	}
	if got := ObsidianFound(config.Config{}); got != "" {
		t.Errorf("nothing: %q", got)
	}
}
