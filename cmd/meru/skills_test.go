// This file tests `meru skills` against an in-process rpc server that plays
// merud's skill ops.

package main

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// fakeSkills plays merud's skill ops over a fixed list, and records which
// skills it reset.
type fakeSkills struct {
	list     []rpc.SkillInfo
	warnings string

	mu     sync.Mutex // guards resets; the server runs handlers on its own goroutines
	resets []string
}

// handle answers one request the way merud does.
func (f *fakeSkills) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	switch req.Op {
	case rpc.OpSkills:
		return emit(rpc.Event{Type: rpc.EventSkills, Skills: f.list, Text: f.warnings})
	case rpc.OpSkillShow:
		i := slices.IndexFunc(f.list, func(s rpc.SkillInfo) bool { return s.Name == req.ID })
		if i < 0 {
			return emit(rpc.Event{Type: rpc.EventError, Error: fmt.Sprintf("no skill %q; meru skills list shows them", req.ID)})
		}
		s := f.list[i]
		s.Body = "---\nname: " + s.Name + "\n---\nBody."
		return emit(rpc.Event{Type: rpc.EventSkills, Skills: []rpc.SkillInfo{s}})
	case rpc.OpSkillReset:
		if req.ID != "writing" && req.ID != "explainer" {
			return emit(rpc.Event{Type: rpc.EventError, Error: fmt.Sprintf("%q isn't a built-in skill", req.ID)})
		}
		f.mu.Lock()
		f.resets = append(f.resets, req.ID)
		f.mu.Unlock()
		return nil
	}
	return emit(rpc.Event{Type: rpc.EventError, Error: fmt.Sprintf("unknown op %q", req.Op)})
}

// resetsDone returns the skills the fake reset.
func (f *fakeSkills) resetsDone() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resets)
}

// someSkills is what the fake lists: both built-ins, writing edited, and
// one skill of the user's own.
var someSkills = []rpc.SkillInfo{
	{Name: "explainer", Description: "Build a self-contained HTML explainer for a technical topic, one page with diagrams and sources.", Builtin: true},
	{Name: "notes", Description: "Take meeting\nnotes."},
	{Name: "writing", Description: "Write prose people will read.", Builtin: true, Edited: true},
}

func TestSkillsList(t *testing.T) {
	f := &fakeSkills{list: someSkills, warnings: "skill broken: SKILL.md: no frontmatter"}
	sock := startServer(t, f.handle)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "skills", "list"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code = %d (stderr %q)", code, errOut.String())
	}
	want := "explainer  Build a self-contained HTML explainer for a technical topic, one page w…  [built-in]\n" +
		"notes      Take meeting notes.\n" +
		"writing    Write prose people will read.                                             [built-in] [edited]\n" +
		"\nSkipped:\n  skill broken: SKILL.md: no frontmatter\n"
	if out.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestSkillsListEmpty(t *testing.T) {
	f := &fakeSkills{}
	sock := startServer(t, f.handle)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "skills", "list"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "No skills") {
		t.Errorf("stdout = %q, want the empty note", out.String())
	}
}

func TestSkillsShow(t *testing.T) {
	f := &fakeSkills{list: someSkills}
	sock := startServer(t, f.handle)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "skills", "show", "notes"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code = %d (stderr %q)", code, errOut.String())
	}
	if out.String() != "---\nname: notes\n---\nBody.\n" {
		t.Errorf("stdout = %q", out.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"-socket", sock, "skills", "show", "nope"}, &out, &errOut); code != exitError ||
		!strings.Contains(errOut.String(), "meru skills list") {
		t.Errorf("show nope: code %d, stderr %q", code, errOut.String())
	}
}

func TestSkillsReset(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		input       string
		interactive bool
		wantErr     string // "" means success
		wantOut     string
		wantResets  []string
	}{
		{name: "edited, terminal, yes", args: []string{"reset", "writing"}, input: "y\n", interactive: true,
			wantOut: "Replace your copy of writing with the shipped one? [y/N] Reset writing to the shipped copy.\n", wantResets: []string{"writing"}},
		{name: "edited, terminal, no", args: []string{"reset", "writing"}, input: "\n", interactive: true,
			wantOut: "Replace your copy of writing with the shipped one? [y/N] Kept your copy.\n"},
		{name: "edited, no terminal", args: []string{"reset", "writing"},
			wantErr: "meru skills reset --yes writing"},
		{name: "edited, --yes", args: []string{"reset", "--yes", "writing"},
			wantOut: "Reset writing to the shipped copy.\n", wantResets: []string{"writing"}},
		{name: "--yes after the name", args: []string{"reset", "writing", "-y"},
			wantResets: []string{"writing"}},
		{name: "not edited", args: []string{"reset", "explainer"},
			wantOut: "Reset explainer to the shipped copy.\n", wantResets: []string{"explainer"}},
		{name: "not built-in", args: []string{"reset", "notes"}, wantErr: "isn't a built-in skill"},
		{name: "unknown, --yes", args: []string{"reset", "--yes", "poetry"}, wantErr: "isn't a built-in skill"},
		{name: "no name", args: []string{"reset", "--yes"}, wantErr: "usage: meru skills"},
		{name: "two names", args: []string{"reset", "a", "b"}, wantErr: "usage: meru skills"},
		{name: "unknown word", args: []string{"remove", "a"}, wantErr: "usage: meru skills"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSkills{list: someSkills}
			sock := startServer(t, f.handle)
			var out bytes.Buffer
			err := skillsCmd(context.Background(), sock, tt.args, strings.NewReader(tt.input), &out, tt.interactive)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want one containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("skillsCmd: %v", err)
			}
			if tt.wantOut != "" && out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if got := f.resetsDone(); !slices.Equal(got, tt.wantResets) {
				t.Errorf("resets = %q, want %q", got, tt.wantResets)
			}
		})
	}
}
