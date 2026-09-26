// This file tests the store against real SQLite files in temporary folders:
// opening and migrating, file modes, replacing and deleting documents, both
// searches, loading chunks, an embedding model change, and readers running
// during a write.

package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// testDims is the vector size the tests use. Three numbers are enough to
// place vectors by hand and know which is nearest.
const testDims = 3

// openTest opens a store in a fresh temporary folder with model "m1" and
// testDims, and closes it when the test ends.
func openTest(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "meru.db"), "m1", testDims)
}

// openAt opens the store at path and closes it when the test ends.
func openAt(t *testing.T, path, model string, dims int) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{Path: path, EmbedModel: model, Dims: dims})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// doc builds a document at path with fixed metadata.
func doc(path string) Document {
	return Document{Path: path, MTime: time.Date(2026, 9, 23, 10, 15, 2, 123456789, time.UTC), Hash: "abc", Kind: "markdown"}
}

// put stores a document whose chunks have the given texts, and gives chunk i
// the vector vecs[i]. It fails the test on error.
func put(t *testing.T, s *Store, path string, texts []string, vecs []engine.Vector) {
	t.Helper()
	chunks := make([]Chunk, len(texts))
	for i, text := range texts {
		chunks[i] = Chunk{Heading: fmt.Sprintf("h%d", i), Text: text, StartLine: i*10 + 1, EndLine: i*10 + 9}
	}
	if err := s.ReplaceDocument(context.Background(), doc(path), chunks, vecs); err != nil {
		t.Fatalf("ReplaceDocument(%s): %v", path, err)
	}
}

// unit returns a testDims vector with 1 at position i.
func unit(i int) engine.Vector {
	v := make(engine.Vector, testDims)
	v[i] = 1
	return v
}

// checkIndexes fails the test if chunk_fts or chunk_vec holds a row for a
// chunk that no longer exists, or misses one that does. FTS5's
// integrity-check with rank 1 compares the keyword index with the chunks
// table it indexes.
func checkIndexes(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO chunk_fts (chunk_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Errorf("chunk_fts integrity check: %v", err)
	}
	var orphans int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM chunk_vec WHERE chunk_id NOT IN (SELECT id FROM chunks)`).Scan(&orphans); err != nil {
		t.Fatalf("count orphan vectors: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d vectors have no chunk", orphans)
	}
}

// stats returns s.Stats or fails the test.
func stats(t *testing.T, s *Store) Stats {
	t.Helper()
	st, err := s.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	return st
}

func TestOpenRejectsBadOptions(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		opts Options
	}{
		{"no path", Options{EmbedModel: "m", Dims: 3}},
		{"no model", Options{Path: filepath.Join(dir, "a.db"), Dims: 3}},
		{"no dims", Options{Path: filepath.Join(dir, "b.db"), EmbedModel: "m"}},
		{"missing folder", Options{Path: filepath.Join(dir, "nope", "c.db"), EmbedModel: "m", Dims: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Open(context.Background(), tt.opts)
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded, want an error")
			}
		})
	}
}

// TestOpenCreatesAndReopens checks a new file gets the current schema, and
// that reopening with the same model keeps documents, chunks and vectors.
// The folder name has a space and a "#", which a file: URI must escape.
func TestOpenCreatesAndReopens(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "my notes #1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "meru.db")

	s, err := Open(ctx, Options{Path: path, EmbedModel: "m1", Dims: testDims})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	version, err := s.metaInt(ctx, "schema_version")
	if err != nil || version != len(migrations()) {
		t.Errorf("schema_version = %d, %v; want %d", version, err, len(migrations()))
	}
	put(t, s, "/n/a.md", []string{"alpha", "beta"}, []engine.Vector{unit(0), unit(1)})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file: %v", err)
	}

	s = openAt(t, path, "m1", testDims)
	if got, want := stats(t, s), (Stats{Documents: 1, Chunks: 2, Vectors: 2}); got != want {
		t.Errorf("after reopen: %+v, want %+v", got, want)
	}
	d, ok, err := s.Document(ctx, "/n/a.md")
	if err != nil || !ok {
		t.Fatalf("Document: %v, %v", ok, err)
	}
	if want := doc("/n/a.md"); !d.MTime.Equal(want.MTime) || d.Hash != want.Hash || d.Kind != want.Kind {
		t.Errorf("Document = %+v, want %+v", d, want)
	}
	if again, _ := s.NeedsReembed(ctx); again {
		t.Error("NeedsReembed after reopening with the same model")
	}
}

// TestOpenRefusesNewerSchema checks that a file from a newer Meru isn't
// touched.
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meru.db")
	s, err := Open(context.Background(), Options{Path: path, EmbedModel: "m1", Dims: testDims})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err := Open(context.Background(), Options{Path: path, EmbedModel: "m1", Dims: testDims}); err == nil {
		s.Close()
		t.Fatal("Open succeeded on schema version 99")
	}
}

// TestFileModes checks the database and its -wal and -shm files are
// readable by their owner only, on a new file and on one whose mode was
// loosened.
func TestFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix file modes")
	}
	path := filepath.Join(t.TempDir(), "meru.db")
	s := openAt(t, path, "m1", testDims)
	put(t, s, "/n/a.md", []string{"alpha"}, []engine.Vector{unit(0)})
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %o, want 600", filepath.Base(p), mode)
		}
	}

	// An older file with a looser mode gets fixed on the next open.
	other := filepath.Join(t.TempDir(), "old.db")
	if err := os.WriteFile(other, nil, 0o644); err != nil { // #nosec G306 -- the test needs a loose mode to fix
		t.Fatal(err)
	}
	openAt(t, other, "m1", testDims)
	fi, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("reopened file has mode %o, want 600", mode)
	}
}

// TestReplaceDocument checks that replacing a document removes every old
// chunk, keyword row and vector, keeps the document's ID, and that a bad
// call changes nothing.
func TestReplaceDocument(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	put(t, s, "/n/a.md", []string{"old apple", "old banana", "old cherry"},
		[]engine.Vector{unit(0), unit(1), unit(2)})
	put(t, s, "/n/b.md", []string{"other"}, []engine.Vector{unit(0)})
	before, _, _ := s.Document(ctx, "/n/a.md")

	put(t, s, "/n/a.md", []string{"new durian"}, []engine.Vector{unit(1)})

	if got, want := stats(t, s), (Stats{Documents: 2, Chunks: 2, Vectors: 2}); got != want {
		t.Errorf("stats = %+v, want %+v", got, want)
	}
	checkIndexes(t, s)
	after, _, _ := s.Document(ctx, "/n/a.md")
	if after.ID != before.ID {
		t.Errorf("document ID changed from %d to %d", before.ID, after.ID)
	}
	if hits, _ := s.SearchKeyword(ctx, "apple banana cherry", 10); len(hits) != 0 {
		t.Errorf("old words still match: %+v", hits)
	}
	if hits, _ := s.SearchKeyword(ctx, "durian", 10); len(hits) != 1 {
		t.Errorf("new word matched %d chunks, want 1", len(hits))
	}

	bad := []struct {
		name   string
		chunks []Chunk
		vecs   []engine.Vector
		path   string
	}{
		{"fewer vectors", []Chunk{{Text: "x"}, {Text: "y"}}, []engine.Vector{unit(0)}, "/n/a.md"},
		{"wrong size", []Chunk{{Text: "x"}}, []engine.Vector{{1, 2}}, "/n/a.md"},
		{"empty path", nil, nil, ""},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.ReplaceDocument(ctx, doc(tt.path), tt.chunks, tt.vecs); err == nil {
				t.Error("ReplaceDocument succeeded, want an error")
			}
			if hits, _ := s.SearchKeyword(ctx, "durian", 10); len(hits) != 1 {
				t.Error("a failed replace changed the index")
			}
		})
	}
}

// TestReplaceDocumentNoChunks checks a document with no text is stored
// with no chunks.
func TestReplaceDocumentNoChunks(t *testing.T) {
	s := openTest(t)
	put(t, s, "/n/empty.md", nil, nil)
	if got, want := stats(t, s), (Stats{Documents: 1}); got != want {
		t.Errorf("stats = %+v, want %+v", got, want)
	}
}

func TestDeleteDocument(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	put(t, s, "/n/a.md", []string{"apple", "banana"}, []engine.Vector{unit(0), unit(1)})
	put(t, s, "/n/b.md", []string{"cherry"}, []engine.Vector{unit(2)})

	tests := []struct {
		name string
		path string
		want Stats
	}{
		{"indexed", "/n/a.md", Stats{Documents: 1, Chunks: 1, Vectors: 1}},
		{"already gone", "/n/a.md", Stats{Documents: 1, Chunks: 1, Vectors: 1}},
		{"never indexed", "/n/zzz.md", Stats{Documents: 1, Chunks: 1, Vectors: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.DeleteDocument(ctx, tt.path); err != nil {
				t.Fatalf("DeleteDocument: %v", err)
			}
			if got := stats(t, s); got != tt.want {
				t.Errorf("stats = %+v, want %+v", got, tt.want)
			}
			checkIndexes(t, s)
		})
	}
	if _, ok, _ := s.Document(ctx, "/n/a.md"); ok {
		t.Error("deleted document still found")
	}
	if hits, _ := s.SearchVector(ctx, unit(0), 10); len(hits) != 1 || hits[0].Rank != 0 {
		t.Errorf("vector search after delete = %+v, want only b.md's chunk", hits)
	}
}

func TestPaths(t *testing.T) {
	s := openTest(t)
	sep := string(filepath.Separator)
	all := []string{
		sep + filepath.Join("home", "notes", "a.md"),
		sep + filepath.Join("home", "notes", "sub", "b.md"),
		sep + filepath.Join("home", "notes2", "c.md"),
		sep + filepath.Join("home", "100%_x", "d.md"),
	}
	for _, p := range all {
		put(t, s, p, nil, nil)
	}
	tests := []struct {
		prefix string
		want   []string
	}{
		{"", []string{all[3], all[0], all[1], all[2]}},
		{sep + filepath.Join("home", "notes"), []string{all[0], all[1]}},
		{sep + filepath.Join("home", "notes") + sep, []string{all[0], all[1]}},
		{sep + filepath.Join("home", "100%_x"), []string{all[3]}},
		{sep + filepath.Join("home", "100"), nil},
		{sep + "elsewhere", nil},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			got, err := s.Paths(context.Background(), tt.prefix)
			if err != nil {
				t.Fatalf("Paths: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Paths(%q) = %q, want %q", tt.prefix, got, tt.want)
			}
			// CountPaths counts by the same rule.
			n, err := s.CountPaths(context.Background(), tt.prefix)
			if err != nil || n != len(tt.want) {
				t.Errorf("CountPaths(%q) = %d, %v; want %d", tt.prefix, n, err, len(tt.want))
			}
		})
	}
}

// TestSearchVector checks nearest-first order on hand-placed vectors.
func TestSearchVector(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	// Chunk IDs follow insertion order: 1 east, 2 north-east, 3 north, 4 up.
	put(t, s, "/n/a.md", []string{"east", "north-east", "north", "up"},
		[]engine.Vector{{1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0, 0, 1}})

	tests := []struct {
		name  string
		query engine.Vector
		k     int
		want  []int64
	}{
		{"east", engine.Vector{1, 0.1, 0}, 3, []int64{1, 2, 3}},
		{"north", engine.Vector{0.1, 1, 0}, 2, []int64{3, 2}},
		{"up", engine.Vector{0, 0, 5}, 1, []int64{4}},
		{"k larger than the index", engine.Vector{1, 0, 0}, 10, []int64{1, 2, 3, 4}},
		{"k zero", engine.Vector{1, 0, 0}, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := s.SearchVector(ctx, tt.query, tt.k)
			if err != nil {
				t.Fatalf("SearchVector: %v", err)
			}
			var got []int64
			for i, h := range hits {
				got = append(got, h.ChunkID)
				if h.Rank != i {
					t.Errorf("hit %d has rank %d", i, h.Rank)
				}
				if i > 0 && h.Score < hits[i-1].Score {
					t.Errorf("distances not ascending: %+v", hits)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("chunks = %v, want %v", got, tt.want)
			}
		})
	}
	// Score is the cosine distance, whatever the vectors' lengths:
	// 1 - cos(45°) for north-east, 1 for up, 2 for west.
	put(t, s, "/n/b.md", []string{"west"}, []engine.Vector{{-3, 0, 0}})
	hits, err := s.SearchVector(ctx, engine.Vector{5, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	wantDist := map[int64]float64{1: 0, 2: 1 - 1/math.Sqrt2, 3: 1, 4: 1, 5: 2}
	for _, h := range hits {
		if math.Abs(h.Score-wantDist[h.ChunkID]) > 1e-6 {
			t.Errorf("chunk %d: distance %v, want %v", h.ChunkID, h.Score, wantDist[h.ChunkID])
		}
	}
	if len(hits) != len(wantDist) {
		t.Errorf("got %d hits, want %d", len(hits), len(wantDist))
	}

	if _, err := s.SearchVector(ctx, engine.Vector{1, 0}, 3); err == nil {
		t.Error("a query with the wrong size succeeded")
	}
}

// TestSearchVectorEmpty checks an empty index returns no hits.
func TestSearchVectorEmpty(t *testing.T) {
	hits, err := openTest(t).SearchVector(context.Background(), unit(0), 5)
	if err != nil || len(hits) != 0 {
		t.Errorf("SearchVector on an empty index = %v, %v", hits, err)
	}
}

// TestSearchKeyword checks BM25 order and that hostile input can't break
// out of the quoting.
func TestSearchKeyword(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	vecs := []engine.Vector{unit(0), unit(0), unit(0), unit(0)}
	put(t, s, "/n/a.md", []string{
		"The Q3 plan grew. Plan review on Friday.", // 1: both words, plan twice
		"Groceries and the weekly plan",            // 2: plan once
		"KESTREL launch moved; NEAR the top",       // 3
		"Café au lait, naïve résumé 東京",            // 4
	}, vecs)

	tests := []struct {
		query string
		want  []int64
	}{
		{"q3 plan", []int64{1, 2}},
		{"KESTREL", []int64{3}},
		{"kestrel", []int64{3}},
		{"what did I write about the q3 plan?", []int64{1, 2, 3}}, // "the" matches chunk 3 too, ranked last
		{"東京", []int64{4}},
		{"résumé", []int64{4}},
		{"", nil},
		{"   ", nil},
		{"zebra", nil},
		// Hostile input: each must run without an FTS5 syntax error.
		{`"`, nil},
		{`"plan`, []int64{1, 2}},
		{`plan"`, []int64{1, 2}},
		{`plan AND`, []int64{2, 1}}, // "and" is a plain word: chunk 2 holds it
		{`OR`, nil},
		{`NOT plan`, []int64{1, 2}},
		{`NEAR(plan q3, 2)`, []int64{3, 1, 2}}, // "near" is a plain word too
		{`NEAR`, []int64{3}},
		{`pla*`, nil},
		{`*`, nil},
		{`text:plan`, []int64{1, 2}},
		{`heading:h0`, []int64{1}},
		{`-plan`, []int64{1, 2}},
		{`^plan`, []int64{1, 2}},
		{`(plan`, []int64{1, 2}},
		{`)) OR ((`, nil},
		{`{heading text}: plan`, []int64{1, 2}},
		{"plan\x00q3", []int64{1, 2}},
		{"'; DROP TABLE chunks; --", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			hits, err := s.SearchKeyword(ctx, tt.query, 10)
			if err != nil {
				t.Fatalf("SearchKeyword(%q): %v", tt.query, err)
			}
			var got []int64
			for _, h := range hits {
				got = append(got, h.ChunkID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("SearchKeyword(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
	if st := stats(t, s); st.Chunks != 4 {
		t.Errorf("chunks = %d after hostile queries, want 4", st.Chunks)
	}
}

func TestFTSQuery(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"hello", `"hello"`},
		{"Q3 plan", `"Q3" OR "plan"`},
		{`a "quoted" AND b`, `"a" OR "quoted" OR "AND" OR "b"`},
		{"plan PLAN Plan", `"plan"`},
		{"NEAR(x*, 2)", `"NEAR" OR "x" OR "2"`},
		{"e-mail", `"e" OR "mail"`},
	}
	for _, tt := range tests {
		if got := ftsQuery(tt.in); got != tt.want {
			t.Errorf("ftsQuery(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
	long := "w"
	for i := range 100 {
		long += fmt.Sprintf(" w%d", i)
	}
	if n := strings.Count(ftsQuery(long), " OR ") + 1; n != maxQueryTerms {
		t.Errorf("a 101-word query kept %d terms, want %d", n, maxQueryTerms)
	}
}

// TestChunks checks chunks come back in the caller's order, with their
// document, and that unknown IDs drop out.
func TestChunks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	put(t, s, "/n/a.md", []string{"one", "two"}, []engine.Vector{unit(0), unit(1)})
	put(t, s, "/n/b.md", []string{"three"}, []engine.Vector{unit(2)})

	tests := []struct {
		name string
		ids  []int64
		want []string
	}{
		{"given order", []int64{3, 1, 2}, []string{"three", "one", "two"}},
		{"unknown IDs dropped", []int64{99, 2, 42}, []string{"two"}},
		{"none", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Chunks(ctx, tt.ids)
			if err != nil {
				t.Fatalf("Chunks: %v", err)
			}
			var texts []string
			for _, c := range got {
				texts = append(texts, c.Text)
			}
			if !slices.Equal(texts, tt.want) {
				t.Errorf("texts = %q, want %q", texts, tt.want)
			}
		})
	}

	got, _ := s.Chunks(ctx, []int64{2})
	want := ChunkWithDoc{
		Chunk: Chunk{ID: 2, DocID: 1, Ordinal: 1, Heading: "h1", Text: "two", StartLine: 11, EndLine: 19},
		Path:  "/n/a.md", Kind: "markdown",
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("Chunks([2]) = %+v, want %+v", got, want)
	}
}

// TestEmbedModelChange checks a new model or vector size drops the vectors,
// keeps the text searchable, and that re-storing every document clears
// NeedsReembed.
func TestEmbedModelChange(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		model string
		dims  int
	}{
		{"new model", "m2", testDims},
		{"new size", "m1", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meru.db")
			s, err := Open(ctx, Options{Path: path, EmbedModel: "m1", Dims: testDims})
			if err != nil {
				t.Fatal(err)
			}
			put(t, s, "/n/a.md", []string{"apple", "banana"}, []engine.Vector{unit(0), unit(1)})
			s.Close()

			s = openAt(t, path, tt.model, tt.dims)
			if got, want := stats(t, s), (Stats{Documents: 1, Chunks: 2}); got != want {
				t.Errorf("stats = %+v, want %+v", got, want)
			}
			if need, err := s.NeedsReembed(ctx); err != nil || !need {
				t.Errorf("NeedsReembed = %v, %v; want true", need, err)
			}
			if hits, _ := s.SearchKeyword(ctx, "banana", 5); len(hits) != 1 {
				t.Error("keyword search lost the text")
			}
			q := make(engine.Vector, tt.dims)
			q[0] = 1
			if hits, err := s.SearchVector(ctx, q, 5); err != nil || len(hits) != 0 {
				t.Errorf("SearchVector = %v, %v; want no hits", hits, err)
			}

			chunks := []Chunk{{Text: "apple"}, {Text: "banana"}}
			if err := s.ReplaceDocument(ctx, doc("/n/a.md"), chunks, []engine.Vector{q, q}); err != nil {
				t.Fatal(err)
			}
			if need, _ := s.NeedsReembed(ctx); need {
				t.Error("NeedsReembed still true after re-embedding")
			}
		})
	}
}

// TestConcurrentReadsDuringWrites runs readers while a writer replaces a
// document over and over. Each reader must always see a whole write: as
// many vectors as chunks, and either the old or the new text. Run with
// -race to also catch data races in the Go code.
func TestConcurrentReadsDuringWrites(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	put(t, s, "/n/a.md", []string{"apple", "banana"}, []engine.Vector{unit(0), unit(1)})

	const writes = 40
	// done closes when the writer finishes, telling readers to stop. A
	// channel of struct{} carries no data; closing it is the signal.
	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	// go starts the function in a new goroutine, which runs alongside this one.
	go func() {
		defer wg.Done()
		defer close(done)
		for i := range writes {
			texts := []string{"apple", "banana"}
			if i%2 == 0 {
				texts = []string{"apple", "banana", "cherry"}
			}
			vecs := make([]engine.Vector, len(texts))
			for j := range vecs {
				vecs[j] = unit(j)
			}
			chunks := make([]Chunk, len(texts))
			for j, text := range texts {
				chunks[j] = Chunk{Text: text}
			}
			if err := s.ReplaceDocument(ctx, doc("/n/a.md"), chunks, vecs); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()

	for r := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// select waits on several channel operations; the default
				// case runs when none is ready, so the loop keeps reading
				// until done closes.
				select {
				case <-done:
					return
				default:
				}
				st, err := s.Stats(ctx)
				if err != nil {
					t.Errorf("reader %d: %v", r, err)
					return
				}
				if st.Chunks != st.Vectors || (st.Chunks != 2 && st.Chunks != 3) {
					t.Errorf("reader %d saw a half-done write: %+v", r, st)
					return
				}
				hits, err := s.SearchKeyword(ctx, "apple", 5)
				if err != nil || len(hits) != 1 {
					t.Errorf("reader %d: keyword hits %v, %v", r, hits, err)
					return
				}
				if _, err := s.SearchVector(ctx, unit(0), 5); err != nil {
					t.Errorf("reader %d: %v", r, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	checkIndexes(t, s)
}
