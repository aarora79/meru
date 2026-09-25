// This file runs the Indexer against the real SQLite store instead of the
// fake sink, to check the contract between the two packages, which were
// written side by side: folder prefixes, deleted files, empty files,
// folders taken out of config and re-embedding after a change of embedding
// model.

package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

// TestScanDropsFoldersLeftConfig indexes two folders, then scans again with
// a different folder list, the way merud does after you edit config.toml
// and restart it. notes-old shares the prefix "notes", so a plain string
// prefix test would get it wrong in both directions.
func TestScanDropsFoldersLeftConfig(t *testing.T) {
	// Each file holds one word no other file has, so a keyword search
	// shows whether the file's keyword rows survived.
	words := map[string][]string{
		"notes":     {"alpha", "bravo"},
		"notes-old": {"charlie"},
	}
	tests := []struct {
		name        string
		keep        []string // folders in the second config
		gone        string   // a kept folder deleted from disk before the second scan
		wantRemoved int
		wantDocs    int      // documents left
		wantWords   []string // words a keyword search still finds
	}{
		{name: "drop notes-old", keep: []string{"notes"}, wantRemoved: 1, wantDocs: 2, wantWords: []string{"alpha", "bravo"}},
		{name: "drop notes", keep: []string{"notes-old"}, wantRemoved: 2, wantDocs: 1, wantWords: []string{"charlie"}},
		{name: "empty list", keep: nil, wantRemoved: 3, wantDocs: 0},
		{name: "keep both", keep: []string{"notes", "notes-old"}, wantRemoved: 0, wantDocs: 3, wantWords: []string{"alpha", "bravo", "charlie"}},
		// A folder still in config that doesn't exist now, as on an
		// unplugged drive, keeps its entries.
		{name: "missing folder kept", keep: []string{"notes", "notes-old"}, gone: "notes-old", wantRemoved: 0, wantDocs: 3, wantWords: []string{"alpha", "bravo", "charlie"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			base := tempRoot(t)
			dir := func(name string) string { return filepath.Join(base, name) }
			writeFiles(t, dir("notes"), map[string]string{
				"a.md":     "# A\n\nalpha",
				"sub/b.md": "# B\n\nbravo",
			})
			writeFiles(t, dir("notes-old"), map[string]string{"c.md": "# C\n\ncharlie"})

			st := openStore(t, t.TempDir(), "m1")
			defer st.Close()
			cfg := testConfig(dir("notes"))
			cfg.Folders = []string{dir("notes"), dir("notes-old")}
			ix, err := New(cfg, st, &fakeEngine{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ix.Scan(ctx); err != nil {
				t.Fatalf("first Scan: %v", err)
			}

			if tt.gone != "" {
				if err := os.RemoveAll(dir(tt.gone)); err != nil {
					t.Fatal(err)
				}
			}
			cfg.Folders = nil
			for _, name := range tt.keep {
				cfg.Folders = append(cfg.Folders, dir(name))
			}
			ix, err = New(cfg, st, &fakeEngine{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := ix.Scan(ctx)
			if err != nil {
				t.Fatalf("second Scan: %v", err)
			}
			if rep.Removed != tt.wantRemoved {
				t.Errorf("Removed = %d, want %d (report %+v)", rep.Removed, tt.wantRemoved, rep)
			}

			// Each file here makes one chunk with one vector, so the three
			// counts match.
			stats, err := st.Stats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Documents != tt.wantDocs || stats.Chunks != tt.wantDocs || stats.Vectors != tt.wantDocs {
				t.Errorf("stats = %+v, want %d of each", stats, tt.wantDocs)
			}
			for folder, ws := range words {
				for _, w := range ws {
					hits, err := st.SearchKeyword(ctx, w, 5)
					if err != nil {
						t.Fatal(err)
					}
					want := slices.Contains(tt.wantWords, w)
					if got := len(hits) > 0; got != want {
						t.Errorf("keyword %q from %s found = %v, want %v", w, folder, got, want)
					}
				}
			}
		})
	}
}
