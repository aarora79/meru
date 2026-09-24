// This file holds the in-memory fakes the indexer tests use in place of the
// SQLite store and Ollama, plus small helpers for building folders.

package index

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
)

// fakeSink is a Sink held in a map. The mutex matters in the watch test,
// where Watch writes from its goroutine while the test reads.
type fakeSink struct {
	mu   sync.Mutex
	docs map[string]fakeEntry
}

// fakeEntry is what fakeSink holds for one path.
type fakeEntry struct {
	doc    store.Document
	chunks []store.Chunk
	vecs   []engine.Vector
}

func newFakeSink() *fakeSink { return &fakeSink{docs: map[string]fakeEntry{}} }

func (s *fakeSink) Document(ctx context.Context, path string) (store.Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.docs[path]
	return e.doc, ok, nil
}

func (s *fakeSink) ReplaceDocument(ctx context.Context, doc store.Document, chunks []store.Chunk, vecs []engine.Vector) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[doc.Path] = fakeEntry{doc: doc, chunks: chunks, vecs: vecs}
	return nil
}

func (s *fakeSink) DeleteDocument(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, path)
	return nil
}

func (s *fakeSink) Paths(ctx context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.docs {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

// paths returns every stored path relative to root, sorted, with forward
// slashes, so tests can compare against literals.
func (s *fakeSink) paths(root string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for p := range s.docs {
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
	}
	slices.Sort(out)
	return out
}

// entry returns what the sink holds for path.
func (s *fakeSink) entry(path string) (fakeEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.docs[path]
	return e, ok
}

// fakeEngine is an engine.Engine whose Embed returns one tiny vector per
// text and records the size of every batch. The other methods aren't used.
type fakeEngine struct {
	mu      sync.Mutex
	batches []int
}

func (e *fakeEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.batches = append(e.batches, len(texts))
	out := make([]engine.Vector, len(texts))
	for i, t := range texts {
		out[i] = engine.Vector{float32(len(t))}
	}
	return out, nil
}

func (e *fakeEngine) calls() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.batches)
}

func (e *fakeEngine) Generate(context.Context, []engine.Message, []engine.ToolSpec, engine.Options) (engine.Completion, error) {
	panic("not used")
}

func (e *fakeEngine) Stream(context.Context, []engine.Message, []engine.ToolSpec, engine.Options) (iter.Seq2[engine.Delta, error], error) {
	panic("not used")
}

func (e *fakeEngine) Info(context.Context) (engine.ModelInfo, error) { panic("not used") }

// testConfig returns an [index] config for root with the defaults.
func testConfig(root string) config.Index {
	return config.Index{
		Folders:       []string{root},
		MaxFileMB:     5,
		ChunkTokens:   500,
		OverlapTokens: 50,
		Watch:         true,
	}
}

// newTestIndexer builds an Indexer over cfg with fresh fakes.
func newTestIndexer(t *testing.T, cfg config.Index) (*Indexer, *fakeSink, *fakeEngine) {
	t.Helper()
	sink, eng := newFakeSink(), &fakeEngine{}
	ix, err := New(cfg, sink, eng, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ix, sink, eng
}

// tempRoot returns a fresh temporary folder with symlinks resolved. On
// macOS t.TempDir sits under /var, which links to /private/var, and the
// indexer stores real paths.
func tempRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeFiles creates each file in files (relative path to content) under
// root, making folders as needed.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// eventually polls cond every 20 ms until it holds or 10 seconds pass.
// Every half second it calls poke, which may be nil; the watch tests use it
// to write a file again in case the watcher wasn't listening yet. Poking
// less often than every poll leaves the path quiet long enough for the
// watcher's debounce to let it through.
func eventually(t *testing.T, what string, cond func() bool, poke func()) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for i := 0; !cond(); i++ {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if poke != nil && i > 0 && i%25 == 0 {
			poke()
		}
		<-tick.C
	}
}
