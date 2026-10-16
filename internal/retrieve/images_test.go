// This file tests how file search treats images: at most two in a search's
// results, cited as images, and described to the model as pictures it
// can't see.

package retrieve

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
)

func TestSearchKeepsTwoImages(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "fake", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Four images right on the query's direction, and one text a little
	// off it. Without the cap the images would take the first four places.
	for i := range 4 {
		p := fmt.Sprintf("/n/img/shot%d.png", i)
		doc := store.Document{Path: p, Kind: KindImage, MTime: time.Now()}
		chunk := store.Chunk{Heading: filepath.Base(p), Text: "Image " + filepath.Base(p) + " in /n/img"}
		if err := st.ReplaceDocument(ctx, doc, []store.Chunk{chunk}, []engine.Vector{{1, float32(i) * 0.01, 0}}); err != nil {
			t.Fatal(err)
		}
	}
	text := store.Document{Path: "/n/plan.md", Kind: "markdown", MTime: time.Now()}
	if err := st.ReplaceDocument(ctx, text, []store.Chunk{{Heading: "Plan", Text: "The garden plan."}}, []engine.Vector{{0.8, 0.5, 0}}); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{vectors: map[string]engine.Vector{"garden screenshot": {1, 0, 0}}}

	results, err := Search(ctx, st, eng, "garden screenshot", Options{})
	if err != nil {
		t.Fatal(err)
	}
	images, sawText := 0, false
	for _, r := range results {
		if r.IsImage() {
			images++
			if r.CitationKind() != "image" {
				t.Errorf("%s: CitationKind = %q", r.Path, r.CitationKind())
			}
		} else {
			sawText = true
		}
	}
	if images != maxImageResults || !sawText {
		t.Errorf("got %d images and text %v in %d results; want %d images and the text", images, sawText, len(results), maxImageResults)
	}
}

func TestCiteAndFormatImage(t *testing.T) {
	r := Result{ChunkWithDoc: store.ChunkWithDoc{
		Path:  "~/Documents/img/shot.png",
		Kind:  KindImage,
		Chunk: store.Chunk{Heading: "shot.png", Text: "Image shot.png in ~/Documents/img"},
	}}
	if got := Cite(1, r); got != "[1] ~/Documents/img/shot.png (image)" {
		t.Errorf("Cite = %q", got)
	}
	f := Format([]Result{r})
	if !strings.Contains(f, "You can't see it") || strings.Contains(f, "Image shot.png in") {
		t.Errorf("Format tells the model the wrong thing about an image:\n%s", f)
	}
	if (Result{}).CitationKind() != "" {
		t.Error("a text result has a citation kind")
	}
}
