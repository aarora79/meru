// This file tests SetEntryLists and SetTableLists: each changes one list
// and leaves every other line, comments included, as it was.

package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// writeConfig writes text to a config.toml in a new temporary folder and
// returns its path.
func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// readText returns the file at path as a string.
func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetEntryLists(t *testing.T) {
	tests := []struct {
		name     string
		entry    string
		lists    map[string][]string
		wantHas  []string // lines the new file must hold
		wantGone []string // lines it must no longer hold
	}{
		{
			name:     "one-line allow, confirm added",
			entry:    "obsidian",
			lists:    map[string][]string{"allow": {"obsidian_simple_search", "obsidian_append_content"}, "confirm": {"obsidian_append_content"}},
			wantHas:  []string{`allow   = ["obsidian_simple_search", "obsidian_append_content"]` + "\n", `confirm = ["obsidian_append_content"]` + "\n"},
			wantGone: []string{`allow   = ["obsidian_simple_search"]` + "\n"},
		},
		{
			name:     "multi-line allow, before the sub-table",
			entry:    "notes",
			lists:    map[string][]string{"allow": {"search"}, "confirm": {}},
			wantHas:  []string{`allow   = ["search"]` + "\n" + `confirm = []` + "\n\n[mcp.servers.env]"},
			wantGone: []string{`  "read",`},
		},
		{
			name:    "a server with no lists at all",
			entry:   "last",
			lists:   map[string][]string{"allow": {"fetch"}},
			wantHas: []string{`url  = "http://127.0.0.1:9/mcp"` + "\n" + `allow = ["fetch"]` + "\n\n[index]"},
		},
	}
	// Lines no test touches, which must survive every change.
	keep := []string{"# my settings", `profile = "lite" # the small one`, "# Obsidian: Reads the notes in your open Obsidian vault.",
		"# notes: a server you added by hand", `NOTES_DIR = "/srv/notes"`, "# keep this: it's about the last server", `folders = ["~/notes"] # mine`}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, before)
			if err := SetEntryLists(path, TableMCP, tt.entry, tt.lists); err != nil {
				t.Fatalf("SetEntryLists: %v", err)
			}
			got := readText(t, path)
			for _, s := range slices.Concat(keep, tt.wantHas) {
				if !strings.Contains(got, s) {
					t.Errorf("config lacks %q:\n%s", s, got)
				}
			}
			for _, s := range tt.wantGone {
				if strings.Contains(got, s) {
					t.Errorf("config still holds %q:\n%s", s, got)
				}
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("config doesn't load: %v", err)
			}
			if n := len(cfg.MCP.Servers); n != 3 {
				t.Errorf("config has %d servers, want 3", n)
			}
		})
	}
}

func TestSetEntryListsA2A(t *testing.T) {
	text := before + `
# travel: an agent on this machine
[[a2a.agents]]
name  = "travel"
url   = "http://127.0.0.1:9100"
allow = ["book"]   # the one skill
`
	path := writeConfig(t, text)
	err := SetEntryLists(path, TableA2A, "travel", map[string][]string{"allow": {"book", "search"}, "confirm": {"book"}})
	if err != nil {
		t.Fatalf("SetEntryLists: %v", err)
	}
	got := readText(t, path)
	for _, s := range []string{`allow = ["book", "search"]   # the one skill`, `confirm = ["book"]`, "# travel: an agent on this machine"} {
		if !strings.Contains(got, s) {
			t.Errorf("config lacks %q:\n%s", s, got)
		}
	}
}

func TestSetEntryListsRefuses(t *testing.T) {
	tests := []struct {
		name  string
		table string
		entry string
		lists map[string][]string
	}{
		{"no such server", TableMCP, "nobody", map[string][]string{"allow": {"x"}}},
		{"not an array table", "index", "obsidian", map[string][]string{"allow": {"x"}}},
		{"confirm outside allow", TableMCP, "obsidian", map[string][]string{"allow": {}, "confirm": {"obsidian_simple_search"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, before)
			if err := SetEntryLists(path, tt.table, tt.entry, tt.lists); err == nil {
				t.Fatal("SetEntryLists succeeded, want an error")
			}
			if got := readText(t, path); got != before {
				t.Errorf("config changed after a refused edit:\n%s", got)
			}
		})
	}
}

func TestSetTableLists(t *testing.T) {
	t.Run("the template's folders", func(t *testing.T) {
		path := writeConfig(t, config.Template())
		if err := SetTableLists(path, "index", map[string][]string{"folders": {"~/Notes", "~/Documents"}}, nil); err != nil {
			t.Fatalf("SetTableLists: %v", err)
		}
		got := readText(t, path)
		if !strings.Contains(got, `folders = ["~/Notes", "~/Documents"]`+"\n") {
			t.Errorf("folders not set:\n%s", got)
		}
		// Only that one line differs from the template.
		want := strings.Replace(config.Template(), "folders = []\n", `folders = ["~/Notes", "~/Documents"]`+"\n", 1)
		if got != want {
			t.Errorf("more than the folders line changed")
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.Index.Folders, []string{"~/Notes", "~/Documents"}) {
			t.Errorf("folders = %v", cfg.Index.Folders)
		}
	})
	t.Run("keeps the comment after the value", func(t *testing.T) {
		path := writeConfig(t, before)
		if err := SetTableLists(path, "index", map[string][]string{"folders": nil}, nil); err != nil {
			t.Fatal(err)
		}
		if got := readText(t, path); !strings.Contains(got, "folders = [] # mine\n") {
			t.Errorf("comment lost:\n%s", got)
		}
	})
	t.Run("adds the table when it's missing", func(t *testing.T) {
		path := writeConfig(t, `profile = "lite"`)
		if err := SetTableLists(path, "skills", map[string][]string{"disabled": {"explainer"}}, nil); err != nil {
			t.Fatal(err)
		}
		want := "profile = \"lite\"\n\n[skills]\ndisabled = [\"explainer\"]\n"
		if got := readText(t, path); got != want {
			t.Errorf("config = %q, want %q", got, want)
		}
	})
	t.Run("a failed check writes nothing", func(t *testing.T) {
		path := writeConfig(t, before)
		boom := errors.New("no")
		err := SetTableLists(path, "index", map[string][]string{"folders": {"~/x"}}, func(config.Config) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the check's error", err)
		}
		if got := readText(t, path); got != before {
			t.Errorf("config changed:\n%s", got)
		}
	})
}

func TestBracketDepth(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{`allow = [`, 1},
		{`allow = ["a", "b"]`, 0},
		{`  "a]",`, 0},
		{`] # done [`, -1},
		{`x = '[' # [`, 0},
	}
	for _, tt := range tests {
		if got := bracketDepth(tt.line); got != tt.want {
			t.Errorf("bracketDepth(%q) = %d, want %d", tt.line, got, tt.want)
		}
	}
}
