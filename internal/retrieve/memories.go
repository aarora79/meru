// This file holds SearchMemories, memory recall: three ranked lists of
// memories (by meaning, by keyword, by recency) merged with the same rrf
// the file search uses. See ARCHITECTURE.md, "Memory", "How Meru uses
// them", step 2.

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

// memoryListSize is how many memories each of the three lists holds before
// the merge. Memories are few next to chunks, so 50 from each list covers
// every memory outside the profile until you have saved hundreds.
const memoryListSize = 50

// Memory is one recalled memory with its fused score. store.Memory is
// embedded, so r.Kind, r.Text and the rest read as fields of r.
type Memory struct {
	store.Memory
	// Score is the memory's RRF score over the three lists. Higher is
	// better; the most it can reach is 3/61, about 0.049.
	Score float64
}

// SearchMemories recalls the n memories that best fit query, best first,
// leaving out the kinds in exclude (the agent passes the profile kinds,
// which are in every prompt already).
//
// It embeds query with eng once, then builds three lists from st: the
// nearest memories by meaning, the best by keyword (BM25), and the newest.
// rrf merges them, so a memory high in any list scores well and one high in
// two or three scores best. The recency list means a fact saved yesterday
// can surface even when the question shares no word with it, which is how
// "remember that" works without a reminder.
//
// A blank query returns nothing and calls no model. It fails when the
// embedding or a query fails. The meru.recall span carries counts and
// timings, never text.
func SearchMemories(ctx context.Context, st *store.Store, eng engine.Engine, query string, exclude []string, n int) (results []Memory, err error) {
	if strings.TrimSpace(query) == "" || n <= 0 {
		return nil, nil
	}
	ctx, span := obs.Tracer().Start(ctx, "meru.recall")
	// The deferred function reads err, the named result, after a return
	// statement has set it, so every exit marks and ends the span.
	defer func() {
		obs.EndSpanErr(ctx, span, err)
		span.End()
	}()

	start := time.Now()
	vecs, err := eng.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("recall: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, errors.New("recall: embed query: no vector returned")
	}
	embedDur := time.Since(start)

	start = time.Now()
	byMeaning, err := st.SearchMemoryVector(ctx, vecs[0], memoryListSize, exclude)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}
	byWords, err := st.SearchMemoryKeyword(ctx, query, memoryListSize, exclude)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}
	newest, err := st.RecentMemories(ctx, memoryListSize, exclude)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}

	// Each list carries whole rows, so the merge needs no second query: keep
	// every row by ID and pick the winners from the map.
	rows := map[int64]store.Memory{}
	lists := make([][]int64, 0, 3)
	for _, list := range [][]store.Memory{byMeaning, byWords, newest} {
		ids := make([]int64, len(list))
		for i, m := range list {
			ids[i] = m.ID
			rows[m.ID] = m
		}
		lists = append(lists, ids)
	}
	scores := rrf(lists...)
	best := top(scores, n)
	results = make([]Memory, len(best))
	for i, b := range best {
		results[i] = Memory{Memory: rows[b.id], Score: b.score}
	}
	searchDur := time.Since(start)
	// One stage for the three queries and the merge: they take well under
	// a millisecond each over a few hundred memories.
	obs.RecordRetrieval(ctx, "memories", searchDur)

	span.SetAttributes(
		attribute.Int("meru.recall.vector_hits", len(byMeaning)),
		attribute.Int("meru.recall.fts_hits", len(byWords)),
		attribute.Int("meru.recall.recent_hits", len(newest)),
		attribute.Int("meru.recall.results", len(results)),
		attribute.Float64("meru.recall.embed_ms", ms(embedDur)),
		attribute.Float64("meru.recall.search_ms", ms(searchDur)),
	)
	return results, nil
}
