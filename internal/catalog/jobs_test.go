// This file tests adding, editing and removing [[jobs]] entries.

package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

func TestSetAndRemoveJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	start := `# my own note
[agent]
history_turns = 3

# The brief I read with coffee.
[[jobs]]
name = "morning-brief"
schedule = "0 7 * * 1-5"
prompt = "Brief me."

[web]
max_results = 5
`
	if err := os.WriteFile(path, []byte(start), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := func() []config.Job {
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Jobs
	}

	// Add a job: it goes at the end, and everything else stays.
	off := false
	news := config.Job{Name: "go-news", Schedule: "0 9 * * 2", EveryWeeks: 2, From: "2026-10-27",
		Skill: "web-research", Prompt: "Summarize \"what's new\" in Go.", Allow: []string{"write_file"},
		Output: "news/{date}.md", Enabled: &off}
	if err := SetJob(path, "", news); err != nil {
		t.Fatal(err)
	}
	got := jobs()
	if len(got) != 2 || got[1].Name != "go-news" || got[1].On() || got[1].Prompt != news.Prompt || got[1].EveryWeeks != 2 {
		t.Fatalf("after add: %+v", got)
	}

	// Edit the first and rename it: it keeps its place and its comment.
	brief := config.Job{Name: "brief", Schedule: "30 6 * * 1-5", Prompt: "Brief me, early."}
	if err := SetJob(path, "morning-brief", brief); err != nil {
		t.Fatal(err)
	}
	got = jobs()
	if len(got) != 2 || got[0].Name != "brief" || got[0].Schedule != "30 6 * * 1-5" {
		t.Fatalf("after edit: %+v", got)
	}
	text, _ := os.ReadFile(path)
	for _, want := range []string{"# my own note", "# The brief I read with coffee.", "max_results = 5"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("lost %q:\n%s", want, text)
		}
	}

	// A name another job has is refused, and nothing changes.
	if err := SetJob(path, "", config.Job{Name: "go-news", Schedule: "0 8 * * *", Prompt: "x"}); err == nil {
		t.Error("a second go-news was saved")
	}
	// A job config refuses leaves the file as it was.
	if err := SetJob(path, "brief", config.Job{Name: "brief", Schedule: "0 25 * * *", Prompt: "x"}); err == nil {
		t.Error("a bad schedule was saved")
	}
	if after, _ := os.ReadFile(path); string(after) != string(text) {
		t.Error("a refused change changed the file")
	}

	// Remove one; its comment goes with it.
	if err := RemoveJob(path, "brief"); err != nil {
		t.Fatal(err)
	}
	got = jobs()
	if len(got) != 1 || got[0].Name != "go-news" {
		t.Fatalf("after remove: %+v", got)
	}
	text, _ = os.ReadFile(path)
	if strings.Contains(string(text), "coffee") || !strings.Contains(string(text), "# my own note") {
		t.Errorf("after remove:\n%s", text)
	}
	if err := RemoveJob(path, "brief"); err == nil {
		t.Error("removing a job twice worked")
	}
}
