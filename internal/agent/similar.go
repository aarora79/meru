// This file holds what a turn does with an attached image besides showing
// it to the model: it looks in the user's folders for images like it,
// before the model runs, the way searchFiles looks in their files. A turn
// whose model can look at pictures gets the list in its prompt; a turn
// whose model can't answers with the list, where it used to refuse. See
// ARCHITECTURE.md, "Agent loop".

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

// similarCount is how many images like an attached one a turn finds. The
// answer lists them, so a handful is enough.
const similarCount = 8

// ImageSearcher finds indexed images like a picture. merud passes an
// adapter around retrieve.SearchImages; tests pass a fake.
type ImageSearcher interface {
	SearchImages(ctx context.Context, q retrieve.ImageQuery, n int) ([]retrieve.Result, error)
}

// UseImageSearch lets a turn with an attached image look for images like
// it. merud calls it once, before the first Handle, when the index reads
// images. Without it, an image turn works as before.
func (a *Agent) UseImageSearch(s ImageSearcher) {
	a.imageSearch = s
}

// findSimilar looks for indexed images like image, the first picture the
// question carries, and returns them as a prompt section with citations.
// A failed search is logged and gives an empty fileHits: the turn goes on
// as it would have without image search.
func (a *Agent) findSimilar(ctx context.Context, image []byte) fileHits {
	ctx, span := obs.Tracer().Start(ctx, "meru.search")
	defer span.End()
	results, err := a.imageSearch.SearchImages(ctx, retrieve.ImageQuery{Image: image}, similarCount)
	if err != nil {
		a.log.WarnContext(ctx, "image search failed; answering without similar images", "err", err)
		obs.EndSpanErr(ctx, span, err)
		return fileHits{}
	}
	var docs []string
	for i := range results {
		docs = append(docs, results[i].Path)
		results[i].Path = rpc.ShortPath(a.home, results[i].Path)
	}
	sources := make([]rpc.Citation, len(results))
	for i, r := range results {
		sources[i] = rpc.Citation{N: i + 1, Path: r.Path, Score: r.Score, Kind: r.CitationKind()}
	}
	a.log.DebugContext(ctx, "similar images", "found", len(results))
	return fileHits{section: similarSection(results), sources: sources, docs: docs, results: results}
}

// similarSection is the prompt section that lists the images like the
// attached one, or "" when there are none. It says Meru ran the search
// already. Under retrieve.Format's "Excerpts from the user's files", a
// model asked to "find similar images" missed the list, said it had no
// tool for that, and offered a web search.
func similarSection(results []retrieve.Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Meru already searched the user's folders for pictures that look like the attached one. " +
		"These are the results, best first, so you need no tool to find similar images. " +
		"When the user asks for images like this one, list these and cite each by number, like [1]. " +
		"These lines hold only paths; to compare one with the attached image, open it with view_image.\n\n")
	for i, r := range results {
		b.WriteString(retrieve.Cite(i+1, r) + "\n")
	}
	return b.String()
}

// similarAnswer is the answer to an image turn whose model can't look at
// pictures but whose image search found some: the list, numbered as the
// sources are, a line on why no model answered, and visionSuggestion. With
// nothing found it returns "", and the turn answers with noVisionAnswer.
func similarAnswer(model string, found fileHits, sets []VisionSet) string {
	if len(found.sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Here are the images in your folders most like the one you attached:\n\n")
	for _, s := range found.sources {
		fmt.Fprintf(&b, "- [%d] %s\n", s.N, s.Path)
	}
	fmt.Fprintf(&b, "\n%s can't look at pictures, so Meru found these by the image itself and asked no model about it. %s",
		model, visionSuggestion(sets))
	return b.String()
}

// emitSimilar sends the similar images as the turn's sources, numbered from
// 1, so the answer's [n] marks point at them and tool calls number on from
// there.
func (a *Agent) emitSimilar(t *turn) error {
	t.sources, t.cites = t.similar.sources, len(t.similar.sources)
	return t.emit(rpc.Event{Type: rpc.EventSources, Sources: t.similar.sources})
}
