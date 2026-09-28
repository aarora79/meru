// This file holds the rule that every installer offers the same answer
// model for a Mac's memory, and that the README says so. The Mac installer
// reads config.Recommendations itself; scripts/install.sh, the meru-install
// skill and the README's table can't read Go, so they carry copies, and
// these tests fail when a copy drifts.

package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// modelRows returns the rows of config.Recommendations that name their own
// answer model, as memory in GB to model. The row with no answer model is
// lite, which each installer gets without choosing.
func modelRows() map[int]string {
	rows := map[int]string{}
	for _, r := range config.Recommendations() {
		if r.Main != "" {
			rows[r.MinMemoryGB] = r.Main
		}
	}
	return rows
}

// TestInstallScriptFollowsTheModelTable reads the memory checks in
// scripts/install.sh, each `-ge N` followed by an answer_model line, and
// compares them with config.Recommendations, row for row.
func TestInstallScriptFollowsTheModelTable(t *testing.T) {
	text, err := os.ReadFile(filepath.Join(moduleRoot(t), "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Each match is one memory check and the model it picks; \s* spans
	// the line break between them.
	re := regexp.MustCompile(`"\$mem_gb" -ge (\d+) \]; then\s*answer_model="([^"]+)"`)
	got := map[int]string{}
	for _, m := range re.FindAllStringSubmatch(string(text), -1) {
		// The pattern allows only digits, so Atoi can't fail here.
		gb, _ := strconv.Atoi(m[1])
		got[gb] = m[2]
	}
	want := modelRows()
	if len(got) != len(want) {
		t.Errorf("install.sh picks a model at %v; config.Recommendations at %v", got, want)
	}
	for gb, model := range want {
		if got[gb] != model {
			t.Errorf("install.sh picks %q from %d GB; config.Recommendations says %q", got[gb], gb, model)
		}
	}
}

// TestInstallSkillFollowsTheModelTable checks that the meru-install skill's
// table has a row for each memory size in config.Recommendations, such as
// "| 48 GB", that names the same answer model.
func TestInstallSkillFollowsTheModelTable(t *testing.T) {
	text, err := os.ReadFile(filepath.Join(moduleRoot(t), ".claude", "skills", "meru-install", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(text), "\n")
	for gb, model := range modelRows() {
		prefix := "| " + strconv.Itoa(gb) + " GB"
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				found = true
				if !strings.Contains(line, "`"+model+"`") {
					t.Errorf("the skill's %d GB row doesn't name %q: %s", gb, model, line)
				}
			}
		}
		if !found {
			t.Errorf("the skill's table has no %d GB row; config.Recommendations picks %q there", gb, model)
		}
	}
}

// TestReadmeFollowsTheModelTable checks that the README's table under "How
// good are the local models?" has a row for each memory size in
// config.Recommendations, such as "| 48 to 63 GB", that names the same
// answer model. The row's figures come from docs/benchmarks/results.md and
// aren't checked here.
func TestReadmeFollowsTheModelTable(t *testing.T) {
	text, err := os.ReadFile(filepath.Join(moduleRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(text), "\n")
	for gb, model := range modelRows() {
		// "| 48 " matches "| 48 to 63 GB" and "| 64 GB or more" alike.
		prefix := "| " + strconv.Itoa(gb) + " "
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) && strings.Contains(line, "`"+model+"`") {
				found = true
			}
		}
		if !found {
			t.Errorf("README.md has no table row from %d GB naming %q, the model config.Recommendations picks", gb, model)
		}
	}
}
