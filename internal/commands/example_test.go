// This file checks the [[commands]] samples in the config template, the
// GitHub ones included: once uncommented, they must load and pass New's
// checks, so the examples a user copies always work.

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
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
	if strings.Join(names, " ") != "cmd.git-log cmd.git-status cmd.search-notes cmd.disk-free "+
		"cmd.gh-prs cmd.gh-pr cmd.gh-issues cmd.gh-issue cmd.gh-runs cmd.gh-repos" {
		t.Errorf("samples give %v", names)
	}

	// The GitHub samples only read, so none asks first, and each
	// repository parameter takes owner/name and nothing else.
	for _, name := range []string{"cmd.gh-prs", "cmd.gh-pr", "cmd.gh-issues", "cmd.gh-issue", "cmd.gh-runs", "cmd.gh-repos"} {
		if s.Confirm(name) != dispatch.ConfirmNever {
			t.Errorf("%s asks first; the GitHub samples only read", name)
		}
	}
	prs, _ := s.lookup("cmd.gh-prs")
	argv, err := prs.Render(map[string]string{"repo": "dana-reyes/garden-planner", "state": "merged"})
	want := "gh pr list --repo dana-reyes/garden-planner --state merged --limit 20 --json number,title,author,state,updatedAt,url"
	if err != nil || strings.Join(argv, " ") != want {
		t.Errorf("gh-prs renders %q, %v; want %q", argv, err, want)
	}
	if _, err := prs.Render(map[string]string{"repo": "garden-planner --web", "state": "open"}); err == nil {
		t.Error("gh-prs took a repository that isn't owner/name")
	}
	pr, _ := s.lookup("cmd.gh-pr")
	argv, err = pr.Render(map[string]string{"repo": "dana-reyes/garden-planner", "number": "12"})
	want = "gh pr view 12 --repo dana-reyes/garden-planner --json number,title,body,state,author,reviews,comments,url"
	if err != nil || strings.Join(argv, " ") != want {
		t.Errorf("gh-pr renders %q, %v; want %q", argv, err, want)
	}
	repos, _ := s.lookup("cmd.gh-repos")
	if _, err := repos.Render(map[string]string{"owner": "dana-reyes/garden"}); err == nil {
		t.Error("gh-repos took an owner with a slash")
	}
}
