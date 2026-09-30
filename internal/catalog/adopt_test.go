// This file tests AdoptServer and UnadoptServer on real-shaped config
// files: an npx obsidian-mcp entry with --vault and a google HTTP entry,
// with comments around them, and checks that an adopt followed by an
// unadopt gives back the file byte for byte.

package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// handConfig is a config.toml as a user who set up both servers by hand
// might have it, with invented paths and names.
const handConfig = `# My Meru config.
[models]
main = "qwen3.6:35b-a3b-mxfp8"

# Notes, through obsidian-mcp.
[[mcp.servers]]
name    = "obsidian"
command = "npx"
args    = ["-y", "obsidian-mcp", "serve", "--vault", "notes=/Users/dana/Notes"]
allow   = ["obsidian_list_vaults", "obsidian_search_vault", "obsidian_read_note", "obsidian_create_note"]
confirm = ["obsidian_create_note"]   # writing asks first

# Mail and calendar.
[[mcp.servers]]
name    = "google"
url     = "http://127.0.0.1:8000/mcp"
allow   = ["search_gmail_messages", "get_events"]
confirm = []
timeout = "120s"

# A commented example that belongs to no table:
# [[mcp.servers]]
# name = "other"

[index]
folders = ["~/Documents"]
`

// writeFile writes body to config.toml in a new temporary folder and
// returns its path.
func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// readFile returns the file at path as a string.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAdoptRoundTrip adopts each entry of handConfig and then restores
// it, and checks the file in between and after.
func TestAdoptRoundTrip(t *testing.T) {
	day := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, id, body string
		table          string
	}{
		{"obsidian", "obsidian", handConfig, ConnectorTable("obsidian",
			map[string]string{"vault_name": "notes", "vault_path": "/Users/dana/Notes"},
			map[string][]string{"allow": {"obsidian_list_vaults", "obsidian_search_vault", "obsidian_read_note", "obsidian_create_note"}, "confirm": {"obsidian_create_note"}})},
		{"google", "google", handConfig, ConnectorTable("google",
			map[string]string{"email": "dana@example.com", "client_id": "123-invented.apps.googleusercontent.com"},
			map[string][]string{"allow": {"search_gmail_messages", "get_events"}, "confirm": {}})},
		{"the last entry, with no line break at the end", "obsidian",
			"[models]\nmain = \"m\"\n\n[[mcp.servers]]\nname = \"obsidian\"\ncommand = \"npx\"\nargs = [\"obsidian-mcp\", \"serve\", \"--vault\", \"n=/x\"]",
			ConnectorTable("obsidian", map[string]string{"vault_path": "/x"}, nil)},
		{"a blank line inside the entry", "obsidian",
			"[[mcp.servers]]\nname = \"obsidian\"\n\n\ncommand = \"npx\"\n\n[index]\nfolders = []\n",
			ConnectorTable("obsidian", map[string]string{"vault_path": "/x"}, nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, tt.body)
			block, err := AdoptServer(path, tt.id, day, tt.table)
			if err != nil {
				t.Fatalf("AdoptServer: %v", err)
			}
			if !strings.Contains(block, `name = "`+tt.id+`"`) && !strings.Contains(block, `name    = "`+tt.id+`"`) {
				t.Errorf("AdoptServer returned %q, not the entry", block)
			}
			adopted := readFile(t, path)
			if !strings.Contains(adopted, "# adopted by merud on 2026-09-30; meru mcp unadopt "+tt.id+" restores it\n") {
				t.Errorf("no marker line:\n%s", adopted)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("the adopted file doesn't load: %v\n%s", err, adopted)
			}
			for _, s := range cfg.MCP.Servers {
				if s.Name == tt.id {
					t.Errorf("the %s entry is still live:\n%s", tt.id, adopted)
				}
			}
			if on, _ := cfg.Connectors[tt.id].Enabled(); !on {
				t.Errorf("[connectors.%s] isn't on:\n%s", tt.id, adopted)
			}

			// A second adopt finds nothing to do and changes nothing.
			if _, err := AdoptServer(path, tt.id, day, tt.table); err == nil {
				t.Error("a second AdoptServer worked")
			}
			if readFile(t, path) != adopted {
				t.Error("a second AdoptServer changed the file")
			}

			if _, err := UnadoptServer(path, tt.id); err != nil {
				t.Fatalf("UnadoptServer: %v\n%s", err, adopted)
			}
			if got := readFile(t, path); got != tt.body {
				t.Errorf("after unadopt the file differs.\ngot:\n%q\nwant:\n%q", got, tt.body)
			}
			// And a second unadopt says there is nothing to restore.
			if _, err := UnadoptServer(path, tt.id); !errors.Is(err, ErrNotAdopted) {
				t.Errorf("a second UnadoptServer = %v, want ErrNotAdopted", err)
			}
		})
	}
}

// TestAdoptedFileShape checks the adopted text for one entry line by
// line: the markers, the entry turned into comments, and the table after
// it, with the comments that followed the entry still after the table.
func TestAdoptedFileShape(t *testing.T) {
	path := writeFile(t, handConfig)
	table := ConnectorTable("google", map[string]string{"email": "dana@example.com"}, nil)
	if _, err := AdoptServer(path, "google", time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), table); err != nil {
		t.Fatal(err)
	}
	want := `# adopted by merud on 2026-09-30; meru mcp unadopt google restores it
# # Mail and calendar.
# [[mcp.servers]]
# name    = "google"
# url     = "http://127.0.0.1:8000/mcp"
# allow   = ["search_gmail_messages", "get_events"]
# confirm = []
# timeout = "120s"
# end of the adopted google entry
[connectors.google]
enabled = true
email = "dana@example.com"

# A commented example that belongs to no table:
`
	if got := readFile(t, path); !strings.Contains(got, want) {
		t.Errorf("adopted file:\n%s\nwant it to hold:\n%s", got, want)
	}
}

// TestAdoptRefusals checks what AdoptServer and UnadoptServer refuse.
func TestAdoptRefusals(t *testing.T) {
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	table := ConnectorTable("obsidian", map[string]string{"vault_path": "/x"}, nil)

	// No entry to adopt.
	path := writeFile(t, "[index]\nfolders = []\n")
	if _, err := AdoptServer(path, "obsidian", day, table); err == nil || !strings.Contains(err.Error(), "no [[mcp.servers]] entry") {
		t.Errorf("AdoptServer with no entry = %v", err)
	}

	// A [connectors.obsidian] table already there.
	body := handConfig + "\n[connectors.obsidian]\nvault_path = \"/x\"\n"
	path = writeFile(t, body)
	if _, err := AdoptServer(path, "obsidian", day, table); err == nil || !strings.Contains(err.Error(), "already has [connectors.obsidian]") {
		t.Errorf("AdoptServer with a table = %v", err)
	}
	if readFile(t, path) != body {
		t.Error("a refused AdoptServer changed the file")
	}

	// A marked entry whose comment was edited can't be put back.
	path = writeFile(t, handConfig)
	if _, err := AdoptServer(path, "obsidian", day, table); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(readFile(t, path), "# name    = \"obsidian\"\n", "name    = \"obsidian\"  \n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UnadoptServer(path, "obsidian"); err == nil {
		t.Error("UnadoptServer put back an edited entry")
	}
}
