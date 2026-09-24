// This file tests the memory syncer against a real memory folder and the
// real SQLite store: adding, hand edits, deletes, a file it can't read, an
// embedding model change, and the watcher.

package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/store"
)

// openMemories opens a memory folder in a fresh temporary folder.
func openMemories(t *testing.T) *memory.Store {
	t.Helper()
	mem, err := memory.Open(filepath.Join(tempRoot(t), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	return mem
}

// storedTexts returns the text of every memory the store holds, sorted, by
// asking for the 100 most recent.
func storedTexts(t *testing.T, st *store.Store) []string {
	t.Helper()
	mems, err := st.RecentMemories(t.Context(), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, m := range mems {
		out = append(out, m.Text)
	}
	slices.Sort(out)
	return out
}

// syncOK runs Sync and fails the test on error.
func syncOK(t *testing.T, ms *Memories) MemoryReport {
	t.Helper()
	rep, err := ms.Sync(t.Context())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return rep
}

// TestMemorySync walks a memory through add, a second sync with nothing to
// do, a hand edit, and a delete, checking what gets embedded each time.
func TestMemorySync(t *testing.T) {
	mem := openMemories(t)
	st := openStore(t, t.TempDir(), "m1")
	defer st.Close()
	eng := &fakeEngine{}
	ms := NewMemories(mem, st, eng, nil)

	sam, err := mem.Add("people", "Sam is the user's manager", "session s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Add("projects", "Plans a vegetable garden", ""); err != nil {
		t.Fatal(err)
	}
	if rep := syncOK(t, ms); rep.Seen != 2 || rep.Indexed != 2 || rep.Unchanged != 0 {
		t.Errorf("first sync = %+v, want 2 indexed", rep)
	}
	if got := eng.calls(); !slices.Equal(got, []int{2}) {
		t.Errorf("Embed batches = %v, want one batch of 2", got)
	}

	// Nothing changed: nothing embedded.
	if rep := syncOK(t, ms); rep.Indexed != 0 || rep.Unchanged != 2 {
		t.Errorf("second sync = %+v, want 2 unchanged", rep)
	}

	// A hand edit: new text and a new mtime.
	edited := "---\ncreated: 2026-09-20\n---\nSam moved to the Denver office\n"
	if err := os.WriteFile(sam.Path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(sam.Path, later, later); err != nil {
		t.Fatal(err)
	}
	if rep := syncOK(t, ms); rep.Indexed != 1 || rep.Unchanged != 1 {
		t.Errorf("sync after the edit = %+v, want 1 indexed", rep)
	}
	hits, err := st.SearchMemoryKeyword(t.Context(), "Denver", 5, nil)
	if err != nil || len(hits) != 1 || hits[0].MemID != sam.ID || hits[0].Source != "" {
		t.Fatalf("keyword search after the edit = %+v, %v", hits, err)
	}
	if !hits[0].Created.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("created = %v, want the edited date", hits[0].Created)
	}

	// A new mtime with the same content still counts as a change, as the
	// indexer counts it.
	if err := os.Chtimes(sam.Path, later.Add(time.Hour), later.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rep := syncOK(t, ms); rep.Indexed != 1 {
		t.Errorf("sync after touching the file = %+v, want 1 indexed", rep)
	}

	// Forget: the row goes.
	if err := mem.Forget(sam.ID); err != nil {
		t.Fatal(err)
	}
	if rep := syncOK(t, ms); rep.Removed != 1 || rep.Unchanged != 1 {
		t.Errorf("sync after forget = %+v, want 1 removed", rep)
	}
	if got := storedTexts(t, st); !slices.Equal(got, []string{"Plans a vegetable garden"}) {
		t.Errorf("stored = %q", got)
	}
}

// TestMemorySyncKeepsRowsItCantCheck checks that a file Sync can't read
// doesn't lose its row, and that Sync still reports the problem.
func TestMemorySyncKeepsRowsItCantCheck(t *testing.T) {
	mem := openMemories(t)
	st := openStore(t, t.TempDir(), "m1")
	defer st.Close()
	ms := NewMemories(mem, st, &fakeEngine{}, nil)

	big, err := mem.Add("other", "Soon too big to read", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Add("other", "Stays readable", ""); err != nil {
		t.Fatal(err)
	}
	syncOK(t, ms)

	// memory reads a file only up to 64 KiB.
	if err := os.WriteFile(big.Path, []byte(strings.Repeat("x", 65<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ms.Sync(t.Context())
	if err == nil || rep.Removed != 0 {
		t.Errorf("Sync = %+v, %v; want an error and nothing removed", rep, err)
	}
	if got := storedTexts(t, st); len(got) != 2 {
		t.Errorf("stored = %q, want both memories kept", got)
	}
}

// TestMemorySyncNewEmbedModel checks that memories whose vectors a model
// change dropped get embedded again, though their files didn't change.
func TestMemorySyncNewEmbedModel(t *testing.T) {
	mem := openMemories(t)
	dbDir := t.TempDir()
	st := openStore(t, dbDir, "m1")
	if _, err := mem.Add("people", "Sam is the user's manager", ""); err != nil {
		t.Fatal(err)
	}
	syncOK(t, NewMemories(mem, st, &fakeEngine{}, nil))
	st.Close()

	st = openStore(t, dbDir, "m2")
	defer st.Close()
	if rep := syncOK(t, NewMemories(mem, st, &fakeEngine{}, nil)); rep.Indexed != 1 {
		t.Errorf("sync after the model change = %+v, want 1 indexed", rep)
	}
	stamps, err := st.MemoryIDs(t.Context())
	if err != nil || len(stamps) != 1 {
		t.Fatalf("MemoryIDs = %v, %v", stamps, err)
	}
	for id, s := range stamps {
		if !s.HasVector {
			t.Errorf("%s still has no vector", id)
		}
	}
}

// TestMemoryWatch checks the watcher picks up files written, deleted and
// added in a new kind folder by hand.
func TestMemoryWatch(t *testing.T) {
	mem := openMemories(t)
	st := openStore(t, t.TempDir(), "m1")
	defer st.Close()
	if _, err := mem.Add("people", "Sam is the user's manager", ""); err != nil {
		t.Fatal(err)
	}
	ms := NewMemories(mem, st, &fakeEngine{}, nil)
	ms.debounce = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ms.Watch(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch returned %v", err)
		}
	}()

	has := func(texts ...string) func() bool {
		return func() bool { return slices.Equal(storedTexts(t, st), texts) }
	}
	// Watch syncs once when it starts.
	eventually(t, "the first sync", has("Sam is the user's manager"), nil)

	write := func(rel, body string) func() {
		return func() { writeFiles(t, mem.Dir(), map[string]string{rel: body}) }
	}
	write("projects/garden.md", "Plans a vegetable garden\n")()
	eventually(t, "a new file", has("Plans a vegetable garden", "Sam is the user's manager"),
		write("projects/garden.md", "Plans a vegetable garden\n"))

	if err := os.Remove(filepath.Join(mem.Dir(), "projects", "garden.md")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a deleted file", has("Sam is the user's manager"), nil)

	// A new kind folder: its file shows up once the folder gets a watch.
	write("health/tea.md", "Drinks green tea\n")()
	eventually(t, "a file in a new folder", has("Drinks green tea", "Sam is the user's manager"),
		write("health/tea.md", "Drinks green tea\n"))
}

func TestCutText(t *testing.T) {
	tests := []struct {
		text  string
		limit int
		want  string
	}{
		{"short", 10, "short"},
		{"abcdef", 3, "abc"},
		// "é" is two bytes; a cut after its first byte backs up before it.
		{"aé", 2, "a"},
		{"", 3, ""},
	}
	for _, tt := range tests {
		if got := cutText(tt.text, tt.limit); got != tt.want {
			t.Errorf("cutText(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
		}
	}
}
