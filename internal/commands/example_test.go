// This file checks the [[commands]] samples in the config template: once
// uncommented, they must load and pass New's checks, so the examples a
// user copies always work.

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

func TestExampleCommands(t *testing.T) {
	home := testHome(t)
	// The samples name ~/repos, which testHome made, and ~/notes.
	if err := os.Mkdir(filepath.Join(home, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A sample starts at a "# [[commands]]" line and ends at the first line
	// that isn't "# " plus text, as in config's own sample test.
	var sample strings.Builder
	in := false
	for _, line := range strings.Split(config.Template(), "\n") {
		if strings.HasPrefix(line, "# [[") {
			in = line == "# [[commands]]"
		}
		if in && !strings.HasPrefix(line, "# ") {
			in = false
		}
		if in {
			sample.WriteString(strings.TrimPrefix(line, "# ") + "\n")
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(sample.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load the samples: %v\n%s", err, sample.String())
	}
	s, err := New(cfg.Commands, nil)
	if err != nil {
		t.Fatalf("New: %v\n%s", err, sample.String())
	}
	var names []string
	for _, spec := range s.Tools() {
		names = append(names, spec.Name)
	}
	if strings.Join(names, " ") != "cmd.git-log cmd.git-status cmd.search-notes cmd.disk-free" {
		t.Errorf("samples give %v", names)
	}
}
