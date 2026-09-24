// This file tests the Indexer end to end against the fakes: incremental
// scans, batching of Embed calls, removal of deleted files, IndexPaths, and
// Watch reacting to files created, changed and deleted.

package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

func TestScanIncremental(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{
		"a.md":       "# A\n\nfirst",
		"b.txt":      "plain",
		"sub/c.go":   "package c\n\nfunc C() {}\n",
		"sub/d.html": "<p>page</p>",
	})
	ix, sink, eng := newTestIndexer(t, testConfig(root))
	ctx := t.Context()

	rep, err := ix.Scan(ctx)
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if rep.Seen != 4 || rep.Indexed != 4 || rep.Unchanged != 0 || rep.Chunks == 0 {
		t.Errorf("first scan = %+v; want 4 seen, 4 indexed", rep)
	}
	e, _ := sink.entry(filepath.Join(root, "a.md"))
	if e.doc.Kind != KindMarkdown || len(e.doc.Hash) != 64 || len(e.vecs) != len(e.chunks) {
		t.Errorf("a.md stored as %+v with %d chunks, %d vectors", e.doc, len(e.chunks), len(e.vecs))
	}

	// Nothing changed: no file is re-embedded.
	calls := len(eng.calls())
	rep, err = ix.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unchanged != 4 || rep.Indexed != 0 || len(eng.calls()) != calls {
		t.Errorf("second scan = %+v with %d new Embed calls; want everything unchanged", rep, len(eng.calls())-calls)
	}

	// Change a's content, touch b's mtime only, delete c.
	writeFiles(t, root, map[string]string{"a.md": "# A\n\nsecond version"})
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "b.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "sub/c.go")); err != nil {
		t.Fatal(err)
	}
	rep, err = ix.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 2 || rep.Unchanged != 1 || rep.Removed != 1 {
		t.Errorf("third scan = %+v; want 2 indexed (a changed, b touched), 1 unchanged, 1 removed", rep)
	}
	e, _ = sink.entry(filepath.Join(root, "a.md"))
	if !strings.Contains(e.chunks[0].Text, "second version") {
		t.Errorf("a.md holds %q, want the new text", e.chunks[0].Text)
	}
	if got, want := sink.paths(root), []string{"a.md", "b.txt", "sub/d.html"}; !reflect.DeepEqual(got, want) {
		t.Errorf("stored = %v, want %v", got, want)
	}
}

func TestScanRemovesNewlySkippedFiles(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"keep.md": "k", "drop.md": "d"})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{".meruignore": "drop.md\n"})
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 1 || !reflect.DeepEqual(sink.paths(root), []string{"keep.md"}) {
		t.Errorf("after ignoring drop.md: report %+v, stored %v", rep, sink.paths(root))
	}
}

func TestScanKeepsMissingFolder(t *testing.T) {
	root := tempRoot(t)
	gone := filepath.Join(root, "unplugged")
	writeFiles(t, gone, map[string]string{"a.md": "x"})
	cfg := testConfig(gone)
	ix, sink, _ := newTestIndexer(t, cfg)
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatalf("Scan with a missing folder: %v", err)
	}
	if got := sink.paths(gone); len(got) != 1 {
		t.Errorf("stored = %v; a missing folder should keep its entries", got)
	}
}

func TestScanFailedPDFCounts(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{
		"broken.pdf": "%PDF-1.4 not really",
		"good.pdf":   string(minimalPDF([]string{"Readable page"})),
	})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 1 || rep.Indexed != 1 {
		t.Errorf("report = %+v; want 1 failed, 1 indexed", rep)
	}
	e, ok := sink.entry(filepath.Join(root, "good.pdf"))
	if !ok || e.doc.Kind != KindPDF || e.chunks[0].Page != 1 {
		t.Errorf("good.pdf stored as %+v", e)
	}
}

func TestEmbedBatches(t *testing.T) {
	root := tempRoot(t)
	// 70 paragraphs of about 150 characters, with a 200-character chunk
	// limit, make 70 chunks: 32 + 32 + 6 texts per Embed call.
	var paras []string
	for i := range 70 {
		paras = append(paras, fmt.Sprintf("Paragraph %02d %s", i, strings.Repeat("filler ", 19)))
	}
	writeFiles(t, root, map[string]string{"long.txt": strings.Join(paras, "\n\n")})
	cfg := testConfig(root)
	cfg.ChunkTokens, cfg.OverlapTokens = 50, 0
	ix, sink, eng := newTestIndexer(t, cfg)
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := eng.calls(), []int{32, 32, 6}; !reflect.DeepEqual(got, want) {
		t.Errorf("Embed batch sizes = %v, want %v", got, want)
	}
	e, _ := sink.entry(filepath.Join(root, "long.txt"))
	for i, c := range e.chunks {
		if c.Ordinal != i {
			t.Fatalf("chunk %d has ordinal %d", i, c.Ordinal)
		}
	}
}

func TestEmbedTextHasHeading(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"n.md": "# Budget\n\n## Q3\n\nSpend less."})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _ := sink.entry(filepath.Join(root, "n.md"))
	// The fake engine's vector is the embedded text's length.
	want := len("Budget > Q3\n\n## Q3\n\nSpend less.")
	if len(e.vecs) != 1 || int(e.vecs[0][0]) != want {
		t.Errorf("vectors = %v; want one of %d, the heading path plus the text", e.vecs, want)
	}
}

// failingEngine fails every Embed call.
type failingEngine struct{ fakeEngine }

func (e *failingEngine) Embed(context.Context, []string) ([]engine.Vector, error) {
	return nil, errors.New("ollama is down")
}

func TestScanStopsWhenEngineFails(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"a.md": "x", "b.md": "y"})
	ix, err := New(testConfig(root), newFakeSink(), &failingEngine{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Scan(t.Context()); err == nil || !strings.Contains(err.Error(), "ollama is down") {
		t.Errorf("Scan = %v; want the engine's error", err)
	}
}

func TestIndexPaths(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"a.md": "x", "sub/b.md": "y", "sub/c.md": "z"})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	ctx := t.Context()

	rep, err := ix.IndexPaths(ctx, []string{filepath.Join(root, "a.md"), filepath.Join(root, "sub")})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 3 {
		t.Errorf("report = %+v, want 3 indexed", rep)
	}

	// A deleted folder takes its files out of the store.
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	rep, err = ix.IndexPaths(ctx, []string{filepath.Join(root, "sub")})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 2 || !reflect.DeepEqual(sink.paths(root), []string{"a.md"}) {
		t.Errorf("after deleting sub: report %+v, stored %v", rep, sink.paths(root))
	}

	// A path outside every folder is refused.
	_, err = ix.IndexPaths(ctx, []string{tempRoot(t)})
	if !errors.Is(err, ErrOutsideFolders) {
		t.Errorf("IndexPaths outside = %v, want ErrOutsideFolders", err)
	}
}

func TestWatch(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"existing.md": "old"})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	ix.debounce = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	// go starts Watch in a new goroutine; the channel carries its result
	// back so the test can wait for it to stop.
	go func() { done <- ix.Watch(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch returned %v", err)
		}
	}()

	text := func(rel string) string {
		e, ok := sink.entry(filepath.Join(root, rel))
		if !ok || len(e.chunks) == 0 {
			return ""
		}
		return e.chunks[0].Text
	}
	write := func(rel, body string) func() {
		return func() { writeFiles(t, root, map[string]string{rel: body}) }
	}

	// Create. The first write may land before Watch is listening, so the
	// poke writes again until the file shows up.
	write("new.md", "fresh")()
	eventually(t, "new.md indexed", func() bool { return text("new.md") == "fresh" }, write("new.md", "fresh"))

	// Modify.
	write("existing.md", "changed")()
	eventually(t, "existing.md updated", func() bool { return text("existing.md") == "changed" }, write("existing.md", "changed"))

	// A new folder gets watched, and files in it get indexed.
	write("newdir/inner.md", "inside")()
	eventually(t, "newdir/inner.md indexed", func() bool { return text("newdir/inner.md") == "inside" }, write("newdir/inner.md", "inside"))

	// Delete.
	if err := os.Remove(filepath.Join(root, "new.md")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "new.md removed", func() bool {
		_, ok := sink.entry(filepath.Join(root, "new.md"))
		return !ok
	}, nil)

	// A skipped file never goes in.
	write(".env", "SECRET=1")()
	write("after.md", "marker")()
	eventually(t, "after.md indexed", func() bool { return text("after.md") == "marker" }, write("after.md", "marker"))
	if _, ok := sink.entry(filepath.Join(root, ".env")); ok {
		t.Error(".env got indexed")
	}
}

func TestWatchOff(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Watch = false
	ix, _, _ := newTestIndexer(t, cfg)
	if err := ix.Watch(t.Context()); err != nil {
		t.Errorf("Watch with watch = false: %v", err)
	}
}
