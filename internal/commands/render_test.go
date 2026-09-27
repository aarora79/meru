// This file tests ArgsFromJSON and Render: values land in exactly one argv
// element each, bad values are refused with a reason, and a path must
// resolve inside its folder, symbolic links included.

package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// mustCommand builds the one command in d through New, so tests render
// exactly what merud would.
func mustCommand(t *testing.T, d config.Command) Command {
	t.Helper()
	s, err := New([]config.Command{d}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s.cmds[0]
}

func TestRender(t *testing.T) {
	testHome(t)
	search := mustCommand(t, config.Command{
		Name: "search",
		Argv: []string{"rg", "--max-count={n}", "--", "{pattern}", "{mode}"},
		Params: map[string]config.CommandParam{
			"pattern": {Type: TypeString, MaxLen: 20},
			"n":       {Type: TypeInt, Min: i64(1), Max: i64(100)},
			"mode":    {Type: TypeEnum, Values: []string{"fast", "slow"}},
		},
	})
	tests := []struct {
		name    string
		args    map[string]string
		want    []string // nil when Render must fail
		wantErr string
	}{
		{"plain", map[string]string{"pattern": "garden", "n": "5", "mode": "fast"},
			[]string{"rg", "--max-count=5", "--", "garden", "fast"}, ""},
		{"spaces, quotes and a semicolon stay one element",
			map[string]string{"pattern": `a "b"; rm -rf ~`, "n": "5", "mode": "fast"},
			[]string{"rg", "--max-count=5", "--", `a "b"; rm -rf ~`, "fast"}, ""},
		{"shell syntax stays text", map[string]string{"pattern": "$(id) `id` | x", "n": "5", "mode": "fast"},
			[]string{"rg", "--max-count=5", "--", "$(id) `id` | x", "fast"}, ""},
		{"an int in its plain form", map[string]string{"pattern": "x", "n": " 007", "mode": "slow"},
			[]string{"rg", "--max-count=7", "--", "x", "slow"}, ""},
		{"missing", map[string]string{"pattern": "x", "mode": "fast"}, nil, `needs the parameter "n"`},
		{"unknown", map[string]string{"pattern": "x", "n": "1", "mode": "fast", "flags": "-v"}, nil, `takes no parameter "flags"`},
		{"empty string", map[string]string{"pattern": "", "n": "1", "mode": "fast"}, nil, "empty"},
		{"too long", map[string]string{"pattern": strings.Repeat("x", 21), "n": "1", "mode": "fast"}, nil, "20-byte limit"},
		{"null byte", map[string]string{"pattern": "a\x00b", "n": "1", "mode": "fast"}, nil, "null byte"},
		{"a flag in a whole element", map[string]string{"pattern": "--pre=sh", "n": "1", "mode": "fast"}, nil, `can't start with "-"`},
		{"not a number", map[string]string{"pattern": "x", "n": "5.5", "mode": "fast"}, nil, "not a whole number"},
		{"below min", map[string]string{"pattern": "x", "n": "0", "mode": "fast"}, nil, "below the minimum"},
		{"above max", map[string]string{"pattern": "x", "n": "101", "mode": "fast"}, nil, "above the maximum"},
		{"not in the enum", map[string]string{"pattern": "x", "n": "1", "mode": "medium"}, nil, `"medium" is not one of`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := search.Render(tt.args)
			if tt.want == nil {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Render = %q, %v; want an error saying %q", got, err, tt.wantErr)
				}
				if err != nil && !strings.Contains(err.Error(), "cmd.search") {
					t.Errorf("error doesn't name the tool: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRenderPattern checks a string with a pattern, as the gh commands in
// the config template declare for a repository: the whole value must
// match, so text after a good start is refused, and the model's schema
// carries the same anchored pattern.
func TestRenderPattern(t *testing.T) {
	testHome(t)
	prs := mustCommand(t, config.Command{
		Name: "gh-prs",
		Argv: []string{"gh", "pr", "list", "--repo", "{repo}"},
		Params: map[string]config.CommandParam{
			"repo": {Type: TypeString, Pattern: `[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`},
		},
	})
	tests := []struct {
		repo string
		ok   bool
	}{
		{"dana-reyes/garden-planner", true},
		{"cli/cli", true},
		{"garden-planner", false},
		{"dana-reyes/garden-planner --web", false},
		{"dana-reyes/garden-planner/extra", false},
		{"https://example.com/dana-reyes/garden-planner", false},
	}
	for _, tt := range tests {
		got, err := prs.Render(map[string]string{"repo": tt.repo})
		switch {
		case tt.ok && (err != nil || got[4] != tt.repo):
			t.Errorf("Render(%q) = %q, %v; want it accepted", tt.repo, got, err)
		case !tt.ok && (err == nil || !strings.Contains(err.Error(), "doesn't match the pattern")):
			t.Errorf("Render(%q) = %q, %v; want a pattern error", tt.repo, got, err)
		}
	}
	if schema := string(prs.schema()); !strings.Contains(schema, `"pattern":"^(?:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)$"`) {
		t.Errorf("schema lacks the anchored pattern: %s", schema)
	}
}

// TestRenderEmbeddedDash checks that a value may start with "-" when its
// placeholder sits inside a longer element: "--grep=-x" is one flag.
func TestRenderEmbeddedDash(t *testing.T) {
	testHome(t)
	c := mustCommand(t, config.Command{
		Name:   "grep-log",
		Argv:   []string{"git", "log", "--grep={text}"},
		Params: map[string]config.CommandParam{"text": {Type: TypeString}},
	})
	got, err := c.Render(map[string]string{"text": "-x"})
	if err != nil || !slices.Equal(got, []string{"git", "log", "--grep=-x"}) {
		t.Errorf("Render = %q, %v", got, err)
	}
}

func TestRenderPath(t *testing.T) {
	home := testHome(t)
	repos := filepath.Join(home, "repos")
	meru := filepath.Join(repos, "meru")
	outside := filepath.Join(home, "secret")
	for _, dir := range []string{meru, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	links := runtime.GOOS != "windows" // creating links on Windows needs a privilege
	if links {
		// A link inside repos that points outside it, and one that stays in.
		if err := os.Symlink(outside, filepath.Join(repos, "escape")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(meru, filepath.Join(repos, "alias")); err != nil {
			t.Fatal(err)
		}
	}
	c := mustCommand(t, config.Command{
		Name:   "git-status",
		Argv:   []string{"git", "-C", "{repo}", "status"},
		Params: map[string]config.CommandParam{"repo": {Type: TypePath, Under: "~/repos"}},
	})
	tests := []struct {
		name    string
		repo    string
		want    string // the argv element; "" when Render must fail
		wantErr string
		links   bool // needs symbolic links
	}{
		{"absolute", meru, meru, "", false},
		{"from home", "~/repos/meru", meru, "", false},
		{"relative to under", "meru", meru, "", false},
		{"under itself", repos, repos, "", false},
		{"dot-dot escape", "~/repos/../secret", "", "outside", false},
		{"relative dot-dot escape", "../secret", "", "outside", false},
		{"missing", "~/repos/nope", "", "no such file or folder", false},
		{"elsewhere", outside, "", "outside", false},
		{"empty", "", "", "empty", false},
		{"symlink out of the tree", "~/repos/escape", "", "outside", true},
		{"symlink inside the tree", "alias", meru, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.links && !links {
				t.Skip("symbolic links need a privilege on Windows")
			}
			got, err := c.Render(map[string]string{"repo": tt.repo})
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Render = %q, %v; want an error saying %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got[2] != tt.want {
				t.Errorf("repo became %q, want %q", got[2], tt.want)
			}
		})
	}
}

func TestArgsFromJSON(t *testing.T) {
	got, err := ArgsFromJSON([]byte(`{"repo":"~/repos/meru","n":20,"big":12345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["repo"] != "~/repos/meru" || got["n"] != "20" || got["big"] != "12345678901234567890" {
		t.Errorf("ArgsFromJSON = %v", got)
	}
	if got, err := ArgsFromJSON(nil); err != nil || len(got) != 0 {
		t.Errorf("empty arguments = %v, %v", got, err)
	}
	for _, bad := range []string{`{"a":true}`, `{"a":["x"]}`, `{"a":null}`, `[1]`, `not json`} {
		if _, err := ArgsFromJSON([]byte(bad)); err == nil {
			t.Errorf("ArgsFromJSON(%s) accepted it", bad)
		}
	}
}
