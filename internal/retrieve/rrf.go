// This file holds reciprocal-rank fusion (RRF), which merges ranked lists of
// chunk IDs into one ranking, and top, which sorts the merged scores.

package retrieve

import (
	"cmp"
	"slices"
)

// rrf merges ranked lists of chunk IDs into one score per chunk.
//
// Each list adds 1/(60 + rank) to every chunk it names, with rank counted
// from 1. A chunk near the top of either list scores well, and one near the
// top of both scores best. RRF looks only at positions, because BM25 scores
// and vector distances use scales that don't compare. The constant 60 is
// the usual choice; it keeps first and second place close, so a chunk both
// lists agree on beats one that a single list ranks first. This is the code
// ARCHITECTURE.md, "How hybrid search works", shows.
//
// `lists ...[]int64` makes rrf variadic: callers pass any number of lists.
func rrf(lists ...[]int64) map[int64]float64 {
	const k = 60
	scores := map[int64]float64{}
	for _, list := range lists {
		// range over a slice gives each index (the 0-based rank) and value.
		for rank, id := range list {
			scores[id] += 1.0 / float64(k+rank+1)
		}
	}
	return scores
}

// scored is one chunk ID with its fused score.
type scored struct {
	id    int64
	score float64
}

// top returns the n best-scoring chunks, highest score first. Equal scores
// go to the lower chunk ID, so the same input always gives the same order.
// Go randomizes the order it walks a map in, which is why the sort needs a
// tie-breaker at all. n <= 0 means all of them.
func top(scores map[int64]float64, n int) []scored {
	out := make([]scored, 0, len(scores))
	for id, s := range scores {
		out = append(out, scored{id: id, score: s})
	}
	// slices.SortFunc sorts with a comparison function that returns a
	// negative number when a goes first. cmp.Compare(b, a) sorts high to
	// low; cmp.Or moves on to the ID only when the scores are equal.
	slices.SortFunc(out, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.id, b.id))
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}
