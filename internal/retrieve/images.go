// This file holds image search: the indexed images nearest to an image the
// user gives Meru, or to a description in words. It searches the same
// tables as file search, limited to documents of kind "image". See
// ARCHITECTURE.md, "Retrieval".

package retrieve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
)

// nameWeight is how much a match on an image's file or folder name counts
// beside a match on the picture itself, in the fusion of SearchImages. File
// search gives both lists equal weight; for images that let names such as
// "taxi" or "airport" pull in other pictures from the same trip ahead of the
// one that shows the words asked for.
const nameWeight = 0.25

// ImageQuery is what to look for: an image's bytes, or a description in
// words. With both, the image wins: "like this one" is the more exact ask.
type ImageQuery struct {
	Image []byte
	Text  string
}

// SearchImages returns up to n indexed images nearest to q, best first.
//
// An image query embeds the picture, with no prefix, and ranks images by
// vector alone: its "words" are pixels, which keyword search can't match.
// A text query embeds the words with the model's query prefix and fuses
// two lists, as file search does: images by meaning, and images whose
// names or folders hold the words. It returns nothing for an empty query,
// and fails when the embedding or a search fails.
func SearchImages(ctx context.Context, st *store.Store, eng engine.Engine, q ImageQuery, n int) ([]Result, error) {
	text := strings.TrimSpace(q.Text)
	if (q.Image == nil && text == "") || n <= 0 {
		return nil, nil
	}
	start := time.Now()
	in := engine.Input{Image: q.Image}
	if q.Image == nil {
		in = engine.Input{Text: text, Query: true}
	}
	vecs, err := eng.Embed(ctx, []engine.Input{in})
	if err != nil {
		return nil, fmt.Errorf("image search: embed: %w", err)
	}
	if len(vecs) != 1 {
		return nil, errors.New("image search: embed: no vector returned")
	}
	vecHits, err := st.SearchVectorKind(ctx, vecs[0], KindImage, defaultListSize)
	if err != nil {
		return nil, fmt.Errorf("image search: %w", err)
	}
	var kwHits []store.Hit
	if q.Image == nil {
		if kwHits, err = st.SearchKeywordKind(ctx, text, KindImage, defaultListSize); err != nil {
			return nil, fmt.Errorf("image search: %w", err)
		}
	}
	scores := fuseImages(chunkIDs(vecHits), chunkIDs(kwHits))
	best := top(scores, n)
	ids := make([]int64, len(best))
	for i, b := range best {
		ids[i] = b.id
	}
	chunks, err := st.Chunks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("image search: %w", err)
	}
	results := make([]Result, len(chunks))
	for i, c := range chunks {
		results[i] = Result{ChunkWithDoc: c, Score: scores[c.ID]}
	}
	obs.RecordRetrieval(ctx, "images", time.Since(start))
	return results, nil
}

// fuseImages merges two ranked lists of image chunk IDs, byLook from the
// vectors and byName from keyword search, into one score per chunk. Each
// list scores as rrf does, and byName's scores count nameWeight as much, so
// the picture's own list leads and a name match lifts an image without
// carrying a poor visual match to the top.
func fuseImages(byLook, byName []int64) map[int64]float64 {
	scores := rrf(byLook)
	// range over a map gives each key and value, in no fixed order; the
	// sum doesn't depend on the order.
	for id, s := range rrf(byName) {
		scores[id] += nameWeight * s
	}
	return scores
}
