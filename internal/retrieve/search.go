// This file holds Search: embed the question, run both searches, merge them
// with rrf, and load the winners. Each stage is timed into
// meru.retrieval.duration and summed up on one meru.retrieve span.

package retrieve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
)

// The list sizes ARCHITECTURE.md starts with: 50 hits from each search, 10
// chunks after the merge. They are starting values to tune against real
// questions.
const (
	defaultListSize = 50
	defaultTopN     = 10
)

// Options sets the list sizes for one search. The zero value means the
// defaults: 50 from each search and 10 results.
type Options struct {
	VectorK  int // hits to take from vector search
	KeywordK int // hits to take from keyword search
	TopN     int // results to return after the merge
}

// withDefaults returns o with each zero or negative size set to its default.
// o is a copy (a value receiver), so the caller's Options don't change.
func (o Options) withDefaults() Options {
	if o.VectorK <= 0 {
		o.VectorK = defaultListSize
	}
	if o.KeywordK <= 0 {
		o.KeywordK = defaultListSize
	}
	if o.TopN <= 0 {
		o.TopN = defaultTopN
	}
	return o
}

// Result is one retrieved chunk: its text, where it came from, and its fused
// score. store.ChunkWithDoc is embedded, so its fields (Path, Heading, Text,
// StartLine and the rest) read as r.Path, r.Heading and so on.
type Result struct {
	store.ChunkWithDoc
	// Score is the chunk's RRF score. Higher is better; the most a chunk can
	// score from two lists is 2/61, about 0.033.
	Score float64
}

// Search finds the chunks that best answer query, best first. It embeds the
// query with eng, runs vector and keyword search on st, merges the two
// lists with rrf, and loads the top opts.TopN chunks. A blank query returns
// no results and no error. It fails when the embedding or either search
// fails.
//
// The meru.retrieve span carries counts and timings only, never the query or
// chunk text.
func Search(ctx context.Context, st *store.Store, eng engine.Engine, query string, opts Options) (results []Result, err error) {
	opts = opts.withDefaults()
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	ctx, span := obs.Tracer().Start(ctx, "meru.retrieve")
	// This deferred function reads err, the named result, after the return
	// statement has set it, so every exit marks and ends the span.
	defer func() {
		obs.EndSpanErr(ctx, span, err)
		span.End()
	}()

	start := time.Now()
	vecs, err := eng.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("retrieve: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, errors.New("retrieve: embed query: no vector returned")
	}
	embedDur := time.Since(start)

	start = time.Now()
	vecHits, err := st.SearchVector(ctx, vecs[0], opts.VectorK)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	vecDur := time.Since(start)
	obs.RecordRetrieval(ctx, "vector", vecDur)

	start = time.Now()
	ftsHits, err := st.SearchKeyword(ctx, query, opts.KeywordK)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	ftsDur := time.Since(start)
	obs.RecordRetrieval(ctx, "fts", ftsDur)

	start = time.Now()
	scores := rrf(chunkIDs(vecHits), chunkIDs(ftsHits))
	best := top(scores, opts.TopN)
	fusionDur := time.Since(start)
	obs.RecordRetrieval(ctx, "fusion", fusionDur)

	start = time.Now()
	ids := make([]int64, len(best))
	for i, b := range best {
		ids[i] = b.id
	}
	chunks, err := st.Chunks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	loadDur := time.Since(start)

	// Chunks keeps the order of ids, so results come out best first.
	// A chunk deleted between the search and the load drops out.
	results = make([]Result, len(chunks))
	for i, c := range chunks {
		results[i] = Result{ChunkWithDoc: c, Score: scores[c.ID]}
	}

	span.SetAttributes(
		attribute.Int("meru.retrieve.vector_hits", len(vecHits)),
		attribute.Int("meru.retrieve.fts_hits", len(ftsHits)),
		attribute.Int("meru.retrieve.fused", len(scores)),
		attribute.Int("meru.retrieve.results", len(results)),
		attribute.Float64("meru.retrieve.embed_ms", ms(embedDur)),
		attribute.Float64("meru.retrieve.vector_ms", ms(vecDur)),
		attribute.Float64("meru.retrieve.fts_ms", ms(ftsDur)),
		attribute.Float64("meru.retrieve.fusion_ms", ms(fusionDur)),
		attribute.Float64("meru.retrieve.load_ms", ms(loadDur)),
	)
	return results, nil
}

// chunkIDs returns the chunk IDs of hits, in rank order.
func chunkIDs(hits []store.Hit) []int64 {
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.ChunkID
	}
	return ids
}

// ms returns d in milliseconds with a fraction. The fast stages take well
// under a millisecond, which Duration.Milliseconds would round to 0.
func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
