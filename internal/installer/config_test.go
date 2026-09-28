// This file tests the steps that write config.toml: the first write from
// the template, [index] folders, the skills and the sample commands. Each
// runs on a config in a temporary folder and checks that the comments
// survive.

package installer

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// templateComment is a line of the template that every write must keep.
const templateComment = "# Folders merud reads into its search index. Empty indexes nothing; merud"

// TestEnsureConfig checks the first write and that a second call leaves
// the file alone.
func TestEnsureConfig(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	wrote, err := EnsureConfig(p.Config())
	if err != nil || !wrote {
		t.Fatalf("EnsureConfig = %v, %v", wrote, err)
	}
	if readFile(t, p.Config()) != config.Template() {
		t.Error("the first config isn't the template")
	}
	writeFile(t, p.Config(), config.Template()+"\n# mine\n")
	if wrote, err := EnsureConfig(p.Config()); err != nil || wrote {
		t.Errorf("second EnsureConfig = %v, %v; want the file kept", wrote, err)
	}
	if !strings.HasSuffix(readFile(t, p.Config()), "# mine\n") {
		t.Error("EnsureConfig changed an existing file")
	}
	writeFile(t, p.Config(), "no_such_key = 1\n")
	if _, err := EnsureConfig(p.Config()); err == nil {
		t.Error("EnsureConfig passed a config that doesn't load")
	}
}

// TestSaveFolders writes [index] folders twice and checks the result and
// the comments.
func TestSaveFolders(t *testing.T) {
	p := tempHome(t)
	for _, folders := range [][]string{{"~/Documents", "~/Notes"}, {"~/Documents"}, nil} {
		if _, err := SaveFolders(p, append(folders, " ", "~/Documents")); err != nil {
			t.Fatalf("SaveFolders(%v): %v", folders, err)
		}
		cfg, err := config.Load(p.Config())
		if err != nil {
			t.Fatal(err)
		}
		want := folders
		if len(want) == 0 {
			want = []string{"~/Documents"}
		}
		if !slices.Equal(cfg.Index.Folders, want) {
			t.Errorf("folders = %v, want %v", cfg.Index.Folders, want)
		}
		if !strings.Contains(readFile(t, p.Config()), templateComment) {
			t.Error("SaveFolders dropped the template's comments")
		}
	}
	if _, err := SaveFolders(p, []string{"notes"}); err == nil {
		t.Error("SaveFolders took a relative folder")
	}
}

// TestSuggestFolders checks the suggestions: the listed folders chosen, and
// Documents and Notes counted, with hidden files left out.
func TestSuggestFolders(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	writeFile(t, filepath.Join(p.Home, "Documents", "a.md"), "a")
	writeFile(t, filepath.Join(p.Home, "Documents", "sub", "b.md"), "b")
	writeFile(t, filepath.Join(p.Home, "Documents", ".hidden", "c.md"), "c")
	writeFile(t, filepath.Join(p.Home, "Documents", ".env"), "SECRET=1")
	writeFile(t, filepath.Join(p.Home, "Notes", "n.md"), "n")

	got := SuggestFolders(t.Context(), p, nil)
	if len(got) != 2 || got[0].Path != "~/Documents" || got[0].Files != 2 || !got[0].Chosen || got[1].Path != "~/Notes" {
		t.Errorf("first run: %+v", got)
	}
	got = SuggestFolders(t.Context(), p, []string{"~/Notes"})
	if len(got) != 2 || got[0].Path != "~/Notes" || !got[0].Chosen || got[1].Chosen {
		t.Errorf("with ~/Notes listed: %+v", got)
	}
}

// TestSampleCommands checks that every sample in the template parses, that
// the read-only snapshots start ticked, and that a sample whose program is
// missing can't be turned on.
func TestSampleCommands(t *testing.T) {
	blocks := SampleBlocks(config.Template())
	if len(blocks) < 20 {
		t.Fatalf("found %d samples in the template, want 20 or more", len(blocks))
	}
	for _, b := range blocks {
		var parsed struct {
			Commands []config.Command `toml:"commands"`
		}
		if _, err := toml.Decode(b, &parsed); err != nil || len(parsed.Commands) != 1 {
			t.Errorf("sample doesn't parse as one command (%v):\n%s", err, b)
		}
	}
	p := tempHome(t)
	cfg, _ := config.Load(p.Config())
	opts := SampleCommands(p, cfg)
	byName := map[string]CommandOption{}
	for _, o := range opts {
		byName[o.Name] = o
	}
	for _, name := range defaultCommands() {
		if _, ok := byName[name]; !ok {
			t.Errorf("default command %q isn't a template sample", name)
		}
	}
	// git-log needs ~/repos, which the temporary home lacks.
	if o := byName["git-log"]; o.Missing == "" || o.Chosen {
		t.Errorf("git-log without ~/repos: %+v", o)
	}
}

// TestSaveSkillsAndCommands turns two samples on, twice, and checks that
// each lands once, uncommented, with the comments kept, and that disabled
// built-in skills come back on while other disabled skills stay off.
func TestSaveSkillsAndCommands(t *testing.T) {
	p := tempHome(t)
	if err := catalog.SetTableLists(p.Config(), "skills", map[string][]string{"disabled": {"explainer", "my-own"}}, nil); err != nil {
		t.Fatal(err)
	}
	// disk-free (df) and uptime run from /bin and /usr/bin on macOS and
	// Linux alike, so this test runs in CI on both.
	chosen := []string{"disk-free", "uptime"}
	for range 2 {
		if _, err := SaveSkillsAndCommands(p, chosen); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(p.Config())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cfg.Commands {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, chosen) {
		t.Errorf("commands = %v, want %v", names, chosen)
	}
	if !slices.Equal(cfg.Skills.Disabled, []string{"my-own"}) {
		t.Errorf("[skills] disabled = %v, want [my-own]", cfg.Skills.Disabled)
	}
	text := readFile(t, p.Config())
	if !strings.Contains(text, templateComment) || !strings.Contains(text, "\n[[commands]]\nname        = \"disk-free\"") {
		t.Errorf("config lost its comments or the command isn't uncommented:\n%s", text[len(text)-400:])
	}
	if _, err := SaveSkillsAndCommands(p, []string{"rm-everything"}); err == nil {
		t.Error("an unknown command passed")
	}
}

// TestCheckSampleFullPath checks that a program outside launchd's PATH gets
// its full path in argv, so merud finds it.
func TestCheckSampleFullPath(t *testing.T) {
	dir := "/opt/homebrew/bin"
	if _, err := os.Stat(filepath.Join(dir, "gh")); err != nil {
		t.Skip("needs gh from Homebrew; the rewrite is checked where it is installed")
	}
	block := "[[commands]]\nname = \"gh-x\"\nargv = [\"gh\", \"--version\"]\n"
	c := config.Command{Name: "gh-x", Argv: []string{"gh", "--version"}}
	missing, got := checkSample(Paths{Home: t.TempDir()}, c, block)
	if missing != "" || !strings.Contains(got, `argv = ["/opt/homebrew/bin/gh", "--version"]`) {
		t.Errorf("checkSample = %q, %q", missing, got)
	}
}
