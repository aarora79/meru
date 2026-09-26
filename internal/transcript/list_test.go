// This file tests List against the session files in testdata/sessions: the
// order, the titles, the turn counts, the limit, and the files it skips.

package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"
)

// copySessions copies testdata/sessions into a new temporary folder and
// sets each file's modification time from mods, keyed by session ID, so a
// test controls which session changed last. It returns the folder.
func copySessions(t *testing.T, mods map[string]time.Time) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	// os.CopyFS copies a whole folder tree; os.DirFS reads the one in testdata.
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "sessions"))); err != nil {
		t.Fatal(err)
	}
	for id, mod := range mods {
		if err := os.Chtimes(sessionPath(dir, id), mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestList(t *testing.T) {
	now := time.Now()
	dir := copySessions(t, map[string]time.Time{
		"2026-09-20T090000-a1b2": now.Add(-time.Hour),
		"2026-09-22T181500-c3d4": now.Add(-48 * time.Hour),
		"2026-09-23T070000-e5f6": now, // newest, but it holds no question
	})

	tests := []struct {
		name    string
		limit   int
		wantIDs []string
	}{
		{"all, newest change first", 0, []string{"2026-09-20T090000-a1b2", "2026-09-22T181500-c3d4"}},
		{"limit of one", 1, []string{"2026-09-20T090000-a1b2"}},
		{"limit above the count", 10, []string{"2026-09-20T090000-a1b2", "2026-09-22T181500-c3d4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := List(dir, tt.limit)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var ids []string
			for _, s := range got {
				ids = append(ids, s.ID)
			}
			if len(ids) != len(tt.wantIDs) {
				t.Fatalf("List IDs = %v, want %v", ids, tt.wantIDs)
			}
			for i := range ids {
				if ids[i] != tt.wantIDs[i] {
					t.Errorf("List IDs = %v, want %v", ids, tt.wantIDs)
				}
			}
		})
	}

	got, err := List(dir, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("List = %v, %v; want two sessions", got, err)
	}
	lisbon, garden := got[0], got[1]
	if lisbon.Title != "Which hotel did I book in Lisbon?" || lisbon.Turns != 2 {
		t.Errorf("first session = %q with %d turns, want the Lisbon question with 2", lisbon.Title, lisbon.Turns)
	}
	if want := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC); !lisbon.Started.Equal(want) {
		t.Errorf("Started = %v, want %v", lisbon.Started, want)
	}
	// The garden question runs over two lines and past the title length.
	if n := utf8.RuneCountInString(garden.Title); n > titleRunes {
		t.Errorf("title has %d characters, want at most %d: %q", n, titleRunes, garden.Title)
	}
	if want := "Draft a note to Dana Reyes about the garden plan: the tomatoes go in on 12 Apri…"; garden.Title != want {
		t.Errorf("title = %q, want %q", garden.Title, want)
	}
}

func TestListMissingFolder(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "none"), 0)
	if err != nil || got != nil {
		t.Errorf("List of a missing folder = %v, %v; want nothing and no error", got, err)
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"  plan\n the   week ", "plan the week"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := title(tt.in); got != tt.want {
			t.Errorf("title(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
