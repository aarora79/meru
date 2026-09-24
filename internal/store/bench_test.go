// This file measures the store at a realistic size: 10,000 chunks with
// 768-number vectors, the size nomic-embed-text returns. It answers the
// ARCHITECTURE.md open question on how fast SQLite runs as WebAssembly.
//
//	go test -run '^$' -bench . -benchtime 20x ./internal/store/

package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// Benchmark sizes: 100 documents of 100 chunks each.
const (
	benchDims      = 768
	benchDocs      = 100
	benchChunksDoc = 100
)

// benchVocabulary is the words benchmark chunks are written in. A small
// vocabulary makes every query word match many chunks, which is the slow
// case for keyword search.
const benchVocabulary = `budget quarter review earnings notes meeting plan
	project design memo draft travel invoice receipt tax return portfolio stock bond
	market report summary agenda action item deadline goal risk issue fix bug release`

// benchChunks returns n chunks of 80 random words and n random vectors.
func benchChunks(rng *rand.Rand, n int) ([]Chunk, []engine.Vector) {
	vocabulary := strings.Fields(benchVocabulary)
	chunks := make([]Chunk, n)
	vecs := make([]engine.Vector, n)
	for i := range chunks {
		words := make([]string, 80)
		for j := range words {
			words[j] = vocabulary[rng.IntN(len(vocabulary))]
		}
		chunks[i] = Chunk{Text: strings.Join(words, " ")}
		vecs[i] = randomVector(rng)
	}
	return chunks, vecs
}

// randomVector returns a vector of benchDims numbers from rng.
func randomVector(rng *rand.Rand) engine.Vector {
	v := make(engine.Vector, benchDims)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

// benchDoc returns benchmark document number d.
func benchDoc(d int) Document {
	return Document{Path: fmt.Sprintf("/bench/%03d.md", d), MTime: time.Now(), Kind: "markdown"}
}

// benchStore builds the 10,000-chunk index once per benchmark and reports
// how long indexing took.
func benchStore(b *testing.B) *Store {
	b.Helper()
	ctx := context.Background()
	s, err := Open(ctx, Options{Path: filepath.Join(b.TempDir(), "meru.db"), EmbedModel: "bench", Dims: benchDims})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })

	// A fixed seed gives every run the same data.
	rng := rand.New(rand.NewPCG(1, 2))
	start := time.Now()
	for d := range benchDocs {
		chunks, vecs := benchChunks(rng, benchChunksDoc)
		if err := s.ReplaceDocument(ctx, benchDoc(d), chunks, vecs); err != nil {
			b.Fatal(err)
		}
	}
	b.Logf("indexed %d chunks in %v", benchDocs*benchChunksDoc, time.Since(start))
	return s
}

func BenchmarkSearchVector(b *testing.B) {
	s := benchStore(b)
	q := randomVector(rand.New(rand.NewPCG(3, 4)))
	ctx := context.Background()
	// b.Loop runs the body as many times as the benchmark needs, and starts
	// the clock at its first call, so the setup above isn't timed.
	for b.Loop() {
		hits, err := s.SearchVector(ctx, q, 50)
		if err != nil || len(hits) != 50 {
			b.Fatalf("SearchVector: %d hits, %v", len(hits), err)
		}
	}
}

func BenchmarkSearchKeyword(b *testing.B) {
	s := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		hits, err := s.SearchKeyword(ctx, "what was the budget for the quarter review", 50)
		if err != nil || len(hits) != 50 {
			b.Fatalf("SearchKeyword: %d hits, %v", len(hits), err)
		}
	}
}

// BenchmarkReplaceDocument times what the indexer does when one file
// changes: replace a 100-chunk document in the full index.
func BenchmarkReplaceDocument(b *testing.B) {
	s := benchStore(b)
	chunks, vecs := benchChunks(rand.New(rand.NewPCG(5, 6)), benchChunksDoc)
	ctx := context.Background()
	for b.Loop() {
		if err := s.ReplaceDocument(ctx, benchDoc(benchDocs/2), chunks, vecs); err != nil {
			b.Fatal(err)
		}
	}
}
