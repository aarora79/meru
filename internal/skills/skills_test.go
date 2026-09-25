// This file tests Load, the Registry methods, and the built-in skills:
// installing them, resetting them, and checking they match the repo copies.

package skills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeSkill creates <dir>/<folder>/SKILL.md holding content, and fails the
// test if it can't.
func writeSkill(t *testing.T, dir, folder, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, folder, fileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// skillText builds a SKILL.md with the given name, description and body.
func skillText(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body + "\n"
}

// TestLoad checks which folders Load accepts and which it skips with a
// warning, one folder per case.
func TestLoad(t *testing.T) {
	tests := []struct {
		name     string
		folder   string
		content  string // "" means make the folder but no SKILL.md
		wantOK   bool
		wantWarn string
	}{
		{name: "good", folder: "good-one", content: skillText("good-one", "Does a thing.", "Steps."), wantOK: true},
		{name: "missing frontmatter", folder: "plain", content: "# Plain\n\ntext\n", wantWarn: "no frontmatter"},
		{name: "name differs from folder", folder: "folder-a", content: skillText("folder-b", "d", "b"), wantWarn: "doesn't match its folder"},
		{name: "capital letters", folder: "Bad", content: skillText("Bad", "d", "b"), wantWarn: "lowercase"},
		{name: "path in name", folder: "x", content: skillText("../x", "d", "b"), wantWarn: "lowercase"},
		{name: "no name", folder: "anon", content: "---\ndescription: d\n---\nb\n", wantWarn: "no name"},
		{name: "long name", folder: strings.Repeat("a", 65), content: skillText(strings.Repeat("a", 65), "d", "b"), wantWarn: "over the 64-byte"},
		{name: "empty description", folder: "nodesc", content: "---\nname: nodesc\ndescription:\n---\nb\n", wantWarn: "no description"},
		{name: "long description", folder: "longdesc", content: skillText("longdesc", strings.Repeat("d", maxDescriptionBytes+1), "b"), wantWarn: "over the 1024-byte"},
		{name: "empty body", folder: "nobody", content: skillText("nobody", "d", ""), wantWarn: "body"},
		{name: "huge file", folder: "huge", content: skillText("huge", "d", strings.Repeat("x", maxFileBytes)), wantWarn: "KiB limit"},
		{name: "no SKILL.md", folder: "empty", content: "", wantWarn: "open SKILL.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content == "" {
				if err := os.Mkdir(filepath.Join(dir, tt.folder), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeSkill(t, dir, tt.folder, tt.content)
			}
			r, err := Load(dir, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := r.Has(tt.folder); got != tt.wantOK {
				t.Errorf("Has(%q) = %v, want %v", tt.folder, got, tt.wantOK)
			}
			warnings := r.Warnings()
			if tt.wantWarn == "" {
				if len(warnings) != 0 {
					t.Errorf("unexpected warnings: %v", warnings)
				}
				return
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), tt.wantWarn) {
				t.Errorf("warnings = %v, want one containing %q", warnings, tt.wantWarn)
			}
		})
	}
}

// TestLoadMixed checks that one bad skill doesn't hide the good ones, that
// List comes back sorted, and that hidden folders and plain files are skipped
// without a warning.
func TestLoadMixed(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "zeta", skillText("zeta", "Last.", "z"))
	writeSkill(t, dir, "alpha", skillText("alpha", "First.", "a"))
	writeSkill(t, dir, "broken", "no header")
	writeSkill(t, dir, ".tmp-123", skillText("tmp", "hidden", "x"))
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []Summary{{Name: "alpha", Description: "First."}, {Name: "zeta", Description: "Last."}}
	if got := r.List(); !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
	if w := r.Warnings(); len(w) != 1 || !strings.Contains(w[0].Error(), "broken") {
		t.Errorf("Warnings = %v, want one naming broken", w)
	}
	if r.Dir() != dir {
		t.Errorf("Dir = %q, want %q", r.Dir(), dir)
	}
}

// TestLoadMissingDir checks that a skills directory that doesn't exist yet
// gives an empty registry, not an error.
func TestLoadMissingDir(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "nope"), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.List()) != 0 {
		t.Errorf("List = %v, want empty", r.List())
	}
}

// TestGetAndBody checks Get, and that Body reads the file on demand: an edit
// after Load shows up, and a rename inside the file is caught.
func TestGetAndBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "notes", "---\nname: notes\ndescription: Take notes.\nlicense: MIT\n---\nVersion one.\n")
	r, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	s, ok := r.Get("notes")
	if !ok || s.Description != "Take notes." || s.Extra["license"] != "MIT" {
		t.Fatalf("Get = %+v, %v", s, ok)
	}
	s.Extra["license"] = "changed"
	if again, _ := r.Get("notes"); again.Extra["license"] != "MIT" {
		t.Error("changing the Extra map from Get changed the registry")
	}
	if _, ok := r.Get("other"); ok {
		t.Error("Get found a skill that doesn't exist")
	}

	body, err := r.Body("notes")
	if err != nil || body != "Version one." {
		t.Fatalf("Body = %q, %v", body, err)
	}
	writeSkill(t, dir, "notes", skillText("notes", "Take notes.", "Version two."))
	if body, err := r.Body("notes"); err != nil || body != "Version two." {
		t.Errorf("Body after edit = %q, %v; want the new text", body, err)
	}
	writeSkill(t, dir, "notes", skillText("renamed", "Take notes.", "x"))
	if _, err := r.Body("notes"); err == nil {
		t.Error("Body accepted a file whose name no longer matches")
	}
	if _, err := r.Body("other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Body(unknown) error = %v, want ErrNotFound", err)
	}
}

// repoSkill reads a skill from the repo's .claude/skills folder, two levels
// above this package.
func repoSkill(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".claude", "skills", name, fileName)) // #nosec G304 -- a fixed path in the repo
	if err != nil {
		t.Fatalf("read repo copy of %s: %v", name, err)
	}
	return data
}

// fromAssets lists the built-in skills copied from the owner's
// my-ai-assets repo, which .claude/skills also holds. web-research is
// Meru's own and lives only under internal/skills/builtin.
var fromAssets = []string{"explainer", "writing"}

// TestBuiltinsMatchRepo checks the list of built-in skills, and that each
// one copied from my-ai-assets is byte-for-byte the copy in .claude/skills.
// AGENTS.md says those are copies, never edited in place; this test
// catches drift in either direction.
func TestBuiltinsMatchRepo(t *testing.T) {
	names := Builtins()
	if !slices.Equal(names, []string{"explainer", "web-research", "writing"}) {
		t.Fatalf("Builtins = %v, want [explainer web-research writing]", names)
	}
	for _, name := range fromAssets {
		t.Run(name, func(t *testing.T) {
			shipped, err := builtinFS.ReadFile("builtin/" + name + "/" + fileName)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(shipped, repoSkill(t, name)) {
				t.Errorf("internal/skills/builtin/%s/SKILL.md differs from .claude/skills/%s/SKILL.md; copy it again", name, name)
			}
		})
	}
}

// TestInstallBuiltins checks the first-run install: all three skills land
// and load cleanly with private permissions, and a second run changes
// nothing.
func TestInstallBuiltins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	installed, err := InstallBuiltins(dir, nil)
	if err != nil {
		t.Fatalf("InstallBuiltins: %v", err)
	}
	if !slices.Equal(installed, []string{"explainer", "web-research", "writing"}) {
		t.Errorf("installed = %v", installed)
	}

	r, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w := r.Warnings(); len(w) != 0 {
		t.Errorf("built-in skills load with warnings: %v", w)
	}
	for _, name := range installed {
		if !r.Has(name) {
			t.Errorf("built-in %s didn't load", name)
		}
		if body, err := r.Body(name); err != nil || body == "" {
			t.Errorf("Body(%s) = %d bytes, %v", name, len(body), err)
		}
	}
	checkMode(t, filepath.Join(dir, "writing"), 0o700)
	checkMode(t, filepath.Join(dir, "writing", fileName), 0o600)

	again, err := InstallBuiltins(dir, nil)
	if err != nil || len(again) != 0 {
		t.Errorf("second InstallBuiltins = %v, %v; want nothing installed", again, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Errorf("skills dir holds %d entries, want 3 (no temp folders left)", len(entries))
	}
}

// TestInstallKeepsEdits checks that InstallBuiltins leaves an edited copy and
// an empty folder alone, and that Reset restores the shipped file while
// keeping extra files.
func TestInstallKeepsEdits(t *testing.T) {
	dir := t.TempDir()
	edited := skillText("writing", "My own rules.", "Be brief.")
	writeSkill(t, dir, "writing", edited)
	extra := filepath.Join(dir, "writing", "notes.txt")
	if err := os.WriteFile(extra, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "explainer"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Only the skill with no folder yet goes in. This is also how a user
	// who installed Meru before web-research shipped gets it.
	installed, err := InstallBuiltins(dir, nil)
	if err != nil || !slices.Equal(installed, []string{"web-research"}) {
		t.Fatalf("InstallBuiltins = %v, %v; want [web-research] alone", installed, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "writing", fileName)) // #nosec G304 -- a test temp dir
	if err != nil || string(got) != edited {
		t.Fatalf("edited copy changed: %q, %v", got, err)
	}

	if err := Reset(dir, "writing"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "writing", fileName)) // #nosec G304 -- a test temp dir
	if err != nil || !bytes.Equal(got, repoSkill(t, "writing")) {
		t.Errorf("after Reset the file isn't the shipped version (err %v)", err)
	}
	checkMode(t, filepath.Join(dir, "writing", fileName), 0o600)
	if _, err := os.Stat(extra); err != nil {
		t.Errorf("Reset removed a file it doesn't ship: %v", err)
	}

	// Reset also fills a folder that is empty or missing.
	if err := Reset(dir, "explainer"); err != nil {
		t.Fatalf("Reset explainer: %v", err)
	}
	r, err := Load(dir, nil)
	if err != nil || !r.Has("explainer") || !r.Has("writing") {
		t.Errorf("after Reset, Load = %v, %v", r.List(), err)
	}
}

// TestResetRefuses checks the cases Reset must turn down.
func TestResetRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := Reset(dir, "poster-making"); !errors.Is(err, ErrNotBuiltin) {
		t.Errorf("Reset(poster-making) = %v, want ErrNotBuiltin", err)
	}
	if err := Reset(dir, "../writing"); !errors.Is(err, ErrNotBuiltin) {
		t.Errorf("Reset(../writing) = %v, want ErrNotBuiltin", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "writing"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(dir, "writing"); err == nil {
		t.Error("Reset wrote over a plain file")
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "explainer")); err != nil {
		t.Skipf("can't make symbolic links here: %v", err)
	}
	if err := Reset(dir, "explainer"); err == nil {
		t.Error("Reset wrote through a symbolic link")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("Reset wrote %d files outside the skills directory", len(entries))
	}
}

// checkMode fails the test when path's permission bits aren't want. Windows
// has no Unix permission bits, so the check is skipped there.
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

// TestDisabled checks [skills] disabled: a disabled built-in isn't
// installed, so deleting it and disabling it keeps it gone; a disabled
// folder that exists doesn't load; a name that matches nothing does no
// harm; and a folder the user adds loads with no change to the list.
func TestDisabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	disabled := []string{"explainer", "mine-too", "not-yet"}
	installed, err := InstallBuiltins(dir, disabled)
	if err != nil {
		t.Fatalf("InstallBuiltins: %v", err)
	}
	if !slices.Equal(installed, []string{"web-research", "writing"}) {
		t.Errorf("installed = %v, want web-research and writing", installed)
	}
	if _, err := os.Lstat(filepath.Join(dir, "explainer")); !os.IsNotExist(err) {
		t.Errorf("the disabled explainer was installed: %v", err)
	}
	writeSkill(t, dir, "mine", skillText("mine", "My skill.", "Do it my way."))
	writeSkill(t, dir, "mine-too", skillText("mine-too", "Another.", "Off for now."))

	r, err := Load(dir, disabled)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var names []string
	for _, s := range r.List() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"mine", "web-research", "writing"}) {
		t.Errorf("loaded %v, want mine, web-research and writing", names)
	}
	if len(r.Warnings()) != 0 {
		t.Errorf("a disabled skill gave warnings: %v", r.Warnings())
	}
}
