// This file tests the skill service: the first-run install, picking up
// hand edits, and the three skill ops.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
)

// collect calls op with an emit function that keeps each event, and returns the events and the error.
func collect(t *testing.T, op func(emit func(rpc.Event) error) error) ([]rpc.Event, error) {
	t.Helper()
	var evs []rpc.Event
	err := op(func(ev rpc.Event) error {
		evs = append(evs, ev)
		return nil
	})
	return evs, err
}

// writeSkillFile writes <dir>/<name>/SKILL.md with the given description
// and body.
func writeSkillFile(t *testing.T, dir, name, description, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillService(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "skills")
	s, err := newSkillService(dir, obs.Discard())
	if err != nil {
		t.Fatalf("newSkillService: %v", err)
	}

	// First run: the two built-ins, neither edited.
	evs, err := collect(t, func(emit func(rpc.Event) error) error { return s.handleList(ctx, emit) })
	if err != nil || len(evs) != 1 {
		t.Fatalf("list = %v, %v", evs, err)
	}
	got := evs[0].Skills
	if len(got) != 2 || got[0].Name != "explainer" || got[1].Name != "writing" ||
		!got[0].Builtin || got[0].Edited || got[1].Edited || got[0].Description == "" {
		t.Fatalf("skills = %+v, want explainer and writing, built-in and not edited", got)
	}

	// Hand edits count on the next call: a new skill, a broken one, and an
	// edit to a built-in.
	writeSkillFile(t, dir, "notes", "Take meeting notes.", "Use bullet points.")
	writeSkillFile(t, dir, "broken", "", "x")
	writeSkillFile(t, dir, "writing", "My own rules.", "Be brief.")
	if reg := s.Registry(ctx); !reg.Has("notes") {
		t.Error("Registry didn't pick up a skill added by hand")
	}
	evs, err = collect(t, func(emit func(rpc.Event) error) error { return s.handleList(ctx, emit) })
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]rpc.SkillInfo{}
	for _, sk := range evs[0].Skills {
		byName[sk.Name] = sk
	}
	if w := byName["writing"]; !w.Builtin || !w.Edited || w.Description != "My own rules." {
		t.Errorf("writing = %+v, want built-in, edited, with the new description", w)
	}
	if n := byName["notes"]; n.Builtin || n.Edited {
		t.Errorf("notes = %+v, want neither mark", n)
	}
	if !strings.Contains(evs[0].Text, "broken") {
		t.Errorf("warnings = %q, want one naming broken", evs[0].Text)
	}

	// show sends the whole file.
	evs, err = collect(t, func(emit func(rpc.Event) error) error {
		return s.handleShow(ctx, rpc.Request{Op: rpc.OpSkillShow, ID: "notes"}, emit)
	})
	if err != nil || len(evs) != 1 || !strings.HasPrefix(evs[0].Skills[0].Body, "---\nname: notes") {
		t.Errorf("show notes = %+v, %v", evs, err)
	}
	if _, err := collect(t, func(emit func(rpc.Event) error) error {
		return s.handleShow(ctx, rpc.Request{Op: rpc.OpSkillShow, ID: "nope"}, emit)
	}); err == nil || !strings.Contains(err.Error(), "meru skills list") {
		t.Errorf("show nope error = %v, want a pointer to meru skills list", err)
	}

	// reset puts the shipped writing back and reloads.
	if err := s.handleReset(ctx, rpc.Request{Op: rpc.OpSkillReset, ID: "writing"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	reg := s.Registry(ctx)
	if sum, _ := reg.Get("writing"); sum.Description == "My own rules." {
		t.Error("after reset writing still has the edited description")
	}
	if err := s.handleReset(ctx, rpc.Request{Op: rpc.OpSkillReset, ID: "notes"}); err == nil ||
		!strings.Contains(err.Error(), "explainer and writing") {
		t.Errorf("reset notes error = %v, want one naming the built-ins", err)
	}
}

// TestSkillServiceKeepsEdits checks that a restart doesn't overwrite a
// built-in you edited.
func TestSkillServiceKeepsEdits(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "writing", "Mine.", "Mine.")
	s, err := newSkillService(dir, obs.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if sum, _ := s.Registry(context.Background()).Get("writing"); sum.Description != "Mine." {
		t.Errorf("writing description = %q, want your copy", sum.Description)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	abs := filepath.Join(t.TempDir(), "out")
	tests := []struct{ in, want string }{
		{"~", home},
		{"~/meru-output", filepath.Join(home, "meru-output")},
		{abs + string(filepath.Separator), abs},
	}
	for _, tt := range tests {
		got, err := expandHome(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("expandHome(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}
