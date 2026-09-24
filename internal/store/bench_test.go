// This file measures the store at realistic sizes: 10,000 and 100,000
// chunks with 768-number vectors, the size nomic-embed-text returns. It
// answers the ARCHITECTURE.md open question on how fast SQLite runs as
// WebAssembly. The 100,000-chunk run takes about a minute; -short skips it.
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

// Benchmark shape: documents of 100 chunks, 768 numbers per vector.
const (
	benchDims      = 768
	benchChunksDoc = 100
)

// benchVocabulary is the words benchmark chunks are written in. A small
// vocabulary makes every query word match many chunks, which is the slow
// case for keyword search.
const benchVocabulary = `garden quarter review recipe notes meeting plan
	project design memo draft travel trip calendar inbox reply photo email album
	schedule report summary agenda action item deadline goal risk issue fix bug release`

// randomVector returns a vector of benchDims numbers from rng.
func randomVector(rng *rand.Rand) engine.Vector {
	v := make(engine.Vector, benchDims)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

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

// benchDoc returns benchmark document number d.
func benchDoc(d int) Document {
	return Document{Path: fmt.Sprintf("/bench/%05d.md", d), MTime: time.Now(), Kind: "markdown"}
}

// benchStore builds an index of docs documents, 100 chunks each, and logs
// how long that took.
func benchStore(b *testing.B, docs int) *Store {
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
	for d := range docs {
		chunks, vecs := benchChunks(rng, benchChunksDoc)
		if err := s.ReplaceDocument(ctx, benchDoc(d), chunks, vecs); err != nil {
			b.Fatal(err)
		}
	}
	b.Logf("indexed %d chunks in %v", docs*benchChunksDoc, time.Since(start))
	return s
}

// BenchmarkStore builds each index once and times the three operations
// retrieval and the indexer run against it.
func BenchmarkStore(b *testing.B) {
	for _, chunks := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("chunks=%d", chunks), func(b *testing.B) {
			if chunks > 10_000 && testing.Short() {
				b.Skip("-short skips the 100,000-chunk index")
			}
			docs := chunks / benchChunksDoc
			s := benchStore(b, docs)
			ctx := context.Background()

			b.Run("SearchVector", func(b *testing.B) {
				q := randomVector(rand.New(rand.NewPCG(3, 4)))
				// b.Loop runs the body as many times as the benchmark
				// needs; setup before it isn't timed.
				for b.Loop() {
					hits, err := s.SearchVector(ctx, q, 50)
					if err != nil || len(hits) != 50 {
						b.Fatalf("SearchVector: %d hits, %v", len(hits), err)
					}
				}
			})
			b.Run("SearchKeyword", func(b *testing.B) {
				for b.Loop() {
					hits, err := s.SearchKeyword(ctx, "what was the plan for the quarter review", 50)
					if err != nil || len(hits) != 50 {
						b.Fatalf("SearchKeyword: %d hits, %v", len(hits), err)
					}
				}
			})
			// What the indexer does when one file changes: replace a
			// 100-chunk document in the full index.
			b.Run("ReplaceDocument", func(b *testing.B) {
				chunks, vecs := benchChunks(rand.New(rand.NewPCG(5, 6)), benchChunksDoc)
				for b.Loop() {
					if err := s.ReplaceDocument(ctx, benchDoc(docs/2), chunks, vecs); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
