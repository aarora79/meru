// This file tests the About you step: the checks, saving to memory files,
// reading them back on a second run, and changing one answer.

package installer

import (
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/memory"
)

// TestProfileCheck checks what the step accepts. The name is always
// required, and the email only after the Google step.
func TestProfileCheck(t *testing.T) {
	tests := []struct {
		name          string
		in            Profile
		emailRequired bool
		want          string // "" means no error
	}{
		{"name only", Profile{Name: "Dana Reyes"}, false, ""},
		{"no name", Profile{Email: "dana@example.com"}, false, "type your name"},
		{"a blank name", Profile{Name: "   "}, false, "type your name"},
		{"no email after Google", Profile{Name: "Dana"}, true, "email address"},
		{"email after Google", Profile{Name: "Dana", Email: "dana@example.com"}, true, ""},
		{"email without @", Profile{Name: "Dana", Email: "dana"}, false, "needs an @"},
		{"two lines", Profile{Name: "Dana\nReyes"}, false, "one line"},
		{"too long", Profile{Name: "Dana", Answers: strings.Repeat("a", 501)}, false, "longer than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Check(tt.emailRequired)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one holding %q", err, tt.want)
			}
		})
	}
}

// TestSaveProfile saves a profile, reads it back, saves it again unchanged,
// then changes one answer and clears another, and checks the memory files
// after each.
func TestSaveProfile(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	if got, err := LoadProfile(p); err != nil || got != (Profile{}) {
		t.Fatalf("empty LoadProfile = %+v, %v", got, err)
	}
	first := Profile{Name: " Dana Reyes ", Email: "dana@example.com", Answers: "short"}
	if _, err := SaveProfile(p, first, false); err != nil {
		t.Fatal(err)
	}
	want := Profile{Name: "Dana Reyes", Email: "dana@example.com", Answers: "short"}
	if got, _ := LoadProfile(p); got != want {
		t.Errorf("LoadProfile = %+v, want %+v", got, want)
	}
	texts := memoryTexts(t, p)
	if strings.Join(texts, "|") != "Name: Dana Reyes|Email: dana@example.com|Answers: short" {
		t.Errorf("memories = %q", texts)
	}

	// The same answers again change nothing.
	if _, err := SaveProfile(p, want, false); err != nil {
		t.Fatal(err)
	}
	if again := memoryTexts(t, p); strings.Join(again, "|") != strings.Join(texts, "|") {
		t.Errorf("an unchanged save changed the memories: %q", again)
	}

	// A new name replaces the old file; a cleared answer removes its file.
	if _, err := SaveProfile(p, Profile{Name: "Dana R.", Email: "dana@example.com"}, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(memoryTexts(t, p), "|"); got != "Name: Dana R.|Email: dana@example.com" {
		t.Errorf("after the change: %q", got)
	}

	// No name, no save.
	if _, err := SaveProfile(p, Profile{Email: "x@example.com"}, false); err == nil {
		t.Error("a profile with no name saved")
	}
}

// memoryTexts returns the text of every memory of kind me, then
// preferences, sorted by file name within each kind: Email sorts before
// Name, so the result puts Name first by hand.
func memoryTexts(t *testing.T, p Paths) []string {
	t.Helper()
	store, err := memory.Open(p.Memory())
	if err != nil {
		t.Fatal(err)
	}
	var name, email, answers []string
	for _, kind := range []string{"me", "preferences"} {
		mems, err := store.ListKind(kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mems {
			switch {
			case strings.HasPrefix(m.Text, "Name: "):
				name = append(name, m.Text)
			case strings.HasPrefix(m.Text, "Email: "):
				email = append(email, m.Text)
			default:
				answers = append(answers, m.Text)
			}
		}
	}
	return append(append(name, email...), answers...)
}
