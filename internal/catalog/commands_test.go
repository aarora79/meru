// This file tests AppendCommand, which adds one [[commands]] entry to
// config.toml.

package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// TestAppendCommand adds a command to a commented config, then checks the
// refusals: a second copy, a block with two commands, and bad TOML. A
// refusal leaves the file as it was.
func TestAppendCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	start := "# my notes\n[index]\nfolders = []   # none yet\n"
	if err := os.WriteFile(path, []byte(start), 0o600); err != nil {
		t.Fatal(err)
	}
	block := "[[commands]]\nname = \"disk-free\"\ndescription = \"Free space\"\nargv = [\"df\", \"-h\"]\n"
	if err := AppendCommand(path, block); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path)
	if want := start + "\n" + block; string(text) != want {
		t.Errorf("file =\n%s\nwant\n%s", text, want)
	}
	cfg, err := config.Load(path)
	if err != nil || len(cfg.Commands) != 1 || cfg.Commands[0].Name != "disk-free" {
		t.Fatalf("loaded %+v, %v", cfg.Commands, err)
	}

	tests := []struct {
		name  string
		block string
		want  string
	}{
		{"same name twice", block, "already has a command"},
		{"two commands", block + strings.Replace(block, "disk-free", "other", 1), "holds 2 commands"},
		{"no name", "[[commands]]\nargv = [\"df\"]\n", "no name"},
		{"bad TOML", "[[commands]\n", "command block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, _ := os.ReadFile(path)
			err := AppendCommand(path, tt.block)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one holding %q", err, tt.want)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Error("a refused append changed the file")
			}
		})
	}
}
