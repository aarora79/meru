// This file tests Stamp, Edited, IsBuiltin and Registry.File: the pieces
// merud uses to notice hand edits and to answer `meru skills`.

package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestStamp checks that the stamp changes when a skill is added, edited or
// removed, and stays put when nothing changes or a hidden folder appears.
func TestStamp(t *testing.T) {
	dir := t.TempDir()
	missing, err := Stamp(filepath.Join(dir, "nope"))
	if err != nil || missing != "" {
		t.Fatalf("Stamp(missing) = %q, %v; want empty", missing, err)
	}

	writeSkill(t, dir, "notes", skillText("notes", "d", "one"))
	first, err := Stamp(dir)
	if err != nil || first == "" {
		t.Fatalf("Stamp = %q, %v", first, err)
	}
	if again, _ := Stamp(dir); again != first {
		t.Error("Stamp changed with no edit")
	}
	writeSkill(t, dir, ".tmp-1", skillText("tmp", "d", "x"))
	if again, _ := Stamp(dir); again != first {
		t.Error("a hidden folder changed the stamp")
	}

	writeSkill(t, dir, "notes", skillText("notes", "d", "one and more"))
	edited, _ := Stamp(dir)
	if edited == first {
		t.Error("editing SKILL.md didn't change the stamp")
	}
	writeSkill(t, dir, "zeta", skillText("zeta", "d", "z"))
	added, _ := Stamp(dir)
	if added == edited {
		t.Error("adding a skill didn't change the stamp")
	}
	if err := os.RemoveAll(filepath.Join(dir, "zeta")); err != nil {
		t.Fatal(err)
	}
	if removed, _ := Stamp(dir); removed != edited {
		t.Error("removing the skill didn't bring the stamp back")
	}
}

// TestEditedAndFile checks the [edited] mark and File on a fresh install,
// after an edit, and for a skill Meru doesn't ship.
func TestEditedAndFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := InstallBuiltins(dir); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "notes", skillText("notes", "d", "mine"))

	if !IsBuiltin("writing") || IsBuiltin("notes") {
		t.Error("IsBuiltin is wrong about writing or notes")
	}
	if e, err := Edited(dir, "writing"); err != nil || e {
		t.Errorf("Edited(writing) on a fresh install = %v, %v", e, err)
	}
	if e, err := Edited(dir, "notes"); err != nil || e {
		t.Errorf("Edited(notes) = %v, %v; want false for a skill Meru doesn't ship", e, err)
	}
	writeSkill(t, dir, "writing", skillText("writing", "Mine.", "Be brief."))
	if e, err := Edited(dir, "writing"); err != nil || !e {
		t.Errorf("Edited(writing) after an edit = %v, %v", e, err)
	}
	if _, err := Edited(t.TempDir(), "writing"); err == nil {
		t.Error("Edited succeeded with no file to compare")
	}

	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.File("notes")
	if err != nil || text != skillText("notes", "d", "mine") {
		t.Errorf("File(notes) = %q, %v", text, err)
	}
	if _, err := r.File("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("File(nope) error = %v, want ErrNotFound", err)
	}
}
