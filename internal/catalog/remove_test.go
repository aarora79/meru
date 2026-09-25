// This file tests RemoveServer: it takes one block out and leaves every
// other line, comments included, as it was.

package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// before is a config.toml with a comment on top, a setting, three servers
// with the comments Block writes, a hand-written comment on the third, and
// a table after them.
const before = `# my settings
profile = "lite" # the small one

# Obsidian: Reads the notes in your open Obsidian vault.
# Docs: https://example.com/obsidian
[[mcp.servers]]
name    = "obsidian"
command = "uvx"
args    = ["mcp-obsidian"]
allow   = ["obsidian_simple_search"]

# notes: a server you added by hand
[[mcp.servers]]
name    = "notes"
command = "notes-mcp"
allow   = [
  "search",
  "read",
]

[mcp.servers.env]
NOTES_DIR = "/srv/notes"

# keep this: it's about the last server
[[mcp.servers]]
name = "last"
url  = "http://127.0.0.1:9/mcp"

[index]
folders = ["~/notes"] # mine
`

func TestRemoveServer(t *testing.T) {
	tests := []struct {
		name        string
		remove      string
		wantRemoved []string // lines the removed text must hold
		wantKept    []string // lines the file must still hold
	}{
		{
			name:        "first",
			remove:      "obsidian",
			wantRemoved: []string{"# Obsidian: Reads the notes in your open Obsidian vault.", "# Docs: https://example.com/obsidian", `name    = "obsidian"`},
			wantKept:    []string{"# my settings", `profile = "lite" # the small one`, "# notes: a server you added by hand", `NOTES_DIR = "/srv/notes"`},
		},
		{
			name:        "with a sub-table and a multi-line array",
			remove:      "notes",
			wantRemoved: []string{"# notes: a server you added by hand", `  "search",`, "[mcp.servers.env]", `NOTES_DIR = "/srv/notes"`},
			wantKept:    []string{"# Obsidian: Reads the notes in your open Obsidian vault.", "# keep this: it's about the last server", `name = "last"`},
		},
		{
			name:        "last before another table",
			remove:      "last",
			wantRemoved: []string{"# keep this: it's about the last server", `url  = "http://127.0.0.1:9/mcp"`},
			wantKept:    []string{"[index]", `folders = ["~/notes"] # mine`, `name    = "notes"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			removed, err := RemoveServer(path, tt.remove)
			if err != nil {
				t.Fatalf("RemoveServer: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range tt.wantRemoved {
				if !strings.Contains(removed, line) {
					t.Errorf("removed text lacks %q:\n%s", line, removed)
				}
				if strings.Contains(string(got), line+"\n") {
					t.Errorf("file still holds %q:\n%s", line, got)
				}
			}
			for _, line := range tt.wantKept {
				if !strings.Contains(string(got), line) {
					t.Errorf("file lost %q:\n%s", line, got)
				}
			}
			if strings.Contains(string(got), "\n\n\n") {
				t.Errorf("file has a double blank line:\n%s", got)
			}
			// The removed text and what's left add back up to the original,
			// give or take blank lines.
			if lines(string(got))+lines(removed) != lines(before) {
				t.Errorf("lines went missing:\nfile:\n%s\nremoved:\n%s", got, removed)
			}
		})
	}
}

// lines counts the lines of s that aren't blank.
func lines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func TestRemoveServerRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := RemoveServer(path, "obsidian"); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("missing file: error = %v", err)
	}
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveServer(path, "slack"); err == nil || !strings.Contains(err.Error(), "no MCP server named") {
		t.Errorf("unknown name: error = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != before {
		t.Errorf("a failed remove changed the file:\n%s", got)
	}
}

// TestAppendThenRemove adds a catalog block and takes it out again, and
// expects the file it started with.
func TestAppendThenRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := "# mine\nprofile = \"lite\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _ := Find("google")
	if err := AppendServer(path, Block(e)); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveServer(path, "google")
	if err != nil {
		t.Fatal(err)
	}
	if removed != Block(e) {
		t.Errorf("removed =\n%s\nwant the block\n%s", removed, Block(e))
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Errorf("file =\n%q\nwant\n%q", got, orig)
	}
}

// TestRemoveServerFromTemplate removes a server the user uncommented inside
// the config template, and one appended at its end, and checks that the
// commented examples around them stay.
func TestRemoveServerFromTemplate(t *testing.T) {
	block := Block(Entries()[0])
	commented := ""
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		commented += "# " + line + "\n"
	}
	// The user deletes the "# " in front of the google block's lines.
	text := strings.Replace(config.Template(), commented, block, 1)
	if text == config.Template() {
		t.Fatal("the template doesn't hold the google block")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendServer(path, Block(Custom("mine", "mine-mcp", nil))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"google", "mine"} {
		if _, err := RemoveServer(path, name); err != nil {
			t.Fatalf("RemoveServer(%s): %v", name, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"# MCP servers: programs that give the model tools.",
		"# The two servers in Meru's catalog", "# [[commands]]", "# [[a2a.agents]]", `# name    = "obsidian"`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("removing lost %q:\n%s", keep, got)
		}
	}
	for _, gone := range []string{`name    = "google"`, `name    = "mine"`} {
		if strings.Contains(string(got), "\n"+gone) {
			t.Errorf("file still holds %q", gone)
		}
	}
}
