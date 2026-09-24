// This file runs the Indexer against the real SQLite store instead of the
// fake sink, to check the contract between the two packages, which were
// written side by side: folder prefixes, deleted files, empty files and
// re-embedding after a change of embedding model.

package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aarora79/meru/internal/store"
)

// openStore opens a store in a fresh folder with the fake engine's vector
// size (1) and the given model name.
func openStore(t *testing.T, dir, model string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(dir, "meru.db"), EmbedModel: model, Dims: 1,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func TestIndexerWithStore(t *testing.T) {
	ctx := t.Context()
	base := tempRoot(t)
	notes := filepath.Join(base, "notes")
	// notes2 shares a prefix with notes. Deleting files from notes must
	// never touch it.
	notes2 := filepath.Join(base, "notes2")
	writeFiles(t, notes, map[string]string{
		"a.md":     "# A\n\nfirst note",
		"empty.md": "",
		"sub/b.md": "# B\n\nsecond note",
	})
	writeFiles(t, notes2, map[string]string{"c.md": "# C\n\nthird note"})

	st := openStore(t, t.TempDir(), "m1")
	defer st.Close()
	cfg := testConfig(notes)
	cfg.Folders = []string{notes, notes2}
	eng := &fakeEngine{}
	ix, err := New(cfg, st, eng, nil)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := ix.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Indexed != 4 || rep.Failed != 0 {
		t.Errorf("first scan = %+v; want 4 indexed (the empty file too), none failed", rep)
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Documents != 4 || stats.Chunks != 3 || stats.Vectors != 3 {
		t.Errorf("stats = %+v; want 4 documents (one empty), 3 chunks, 3 vectors", stats)
	}

	// Delete a file in notes; notes2/c.md must stay.
	if err := os.Remove(filepath.Join(notes, "sub", "b.md")); err != nil {
		t.Fatal(err)
	}
	rep, err = ix.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 1 || rep.Unchanged != 3 {
		t.Errorf("second scan = %+v; want 1 removed, 3 unchanged", rep)
	}
	paths, err := st.Paths(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(notes, "a.md"), filepath.Join(notes, "empty.md"), filepath.Join(notes2, "c.md")}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths = %v, want %v", paths, want)
			break
		}
	}

	// IndexPaths on the whole of notes removes nothing from notes2 either.
	if _, err := ix.IndexPaths(ctx, []string{notes}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Document(ctx, filepath.Join(notes2, "c.md")); !ok {
		t.Error("IndexPaths(notes) removed notes2/c.md")
	}
}

func TestReembedAfterModelChange(t *testing.T) {
	ctx := t.Context()
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"a.md": "# A\n\none", "b.md": "# B\n\ntwo"})
	dbDir := t.TempDir()

	st := openStore(t, dbDir, "m1")
	ix, err := New(testConfig(root), st, &fakeEngine{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// A new embedding model: Open drops every vector.
	st = openStore(t, dbDir, "m2")
	defer st.Close()
	need, err := st.NeedsReembed(ctx)
	if err != nil || !need {
		t.Fatalf("NeedsReembed = %v, %v; want true after a model change", need, err)
	}
	eng := &fakeEngine{}
	ix, err = New(testConfig(root), st, eng, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A plain Scan sees unchanged files and leaves the vectors missing.
	rep, err := ix.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unchanged != 2 || len(eng.calls()) != 0 {
		t.Errorf("Scan = %+v with %d Embed calls; want 2 unchanged, no embedding", rep, len(eng.calls()))
	}

	// Reembed embeds every file again.
	rep, err = ix.Reembed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 2 || rep.Unchanged != 0 {
		t.Errorf("Reembed = %+v; want 2 indexed", rep)
	}
	if need, err := st.NeedsReembed(ctx); err != nil || need {
		t.Errorf("NeedsReembed after Reembed = %v, %v; want false", need, err)
	}
}
