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

// TestSearchImages checks image search against a real store: an image
// query ranks images by their look alone and leaves text out; a text query
// gets the query prefix's input and also matches an image by its name.
func TestSearchImages(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "fake", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	add := func(p, kind, text string, v engine.Vector) {
		t.Helper()
		doc := store.Document{Path: p, Kind: kind, MTime: time.Now()}
		if err := st.ReplaceDocument(ctx, doc, []store.Chunk{{Heading: filepath.Base(p), Text: text}}, []engine.Vector{v}); err != nil {
			t.Fatal(err)
		}
	}
	add("/n/notes.md", "markdown", "beds by the fence", engine.Vector{1, 0, 0})
	add("/n/img/beds.png", KindImage, "Image beds.png in /n/img", engine.Vector{0.9, 0.2, 0})
	add("/n/img/cat.png", KindImage, "Image cat.png in /n/img", engine.Vector{0, 0, 1})

	// The fake engine looks up a vector by text; an image input has no
	// text, so "" stands for the attached picture.
	eng := &fakeEngine{vectors: map[string]engine.Vector{
		"":     {1, 0, 0},
		"cats": {0, 0, 1},
	}}
	got, err := SearchImages(ctx, st, eng, ImageQuery{Image: []byte("png bytes")}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "/n/img/beds.png" || !got[0].IsImage() {
		t.Fatalf("image query = %+v; want beds.png first and no text", got)
	}
	got, err = SearchImages(ctx, st, eng, ImageQuery{Text: "cats"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "/n/img/cat.png" {
		t.Errorf("text query = %+v; want cat.png", got)
	}
	if got, _ := SearchImages(ctx, st, eng, ImageQuery{}, 5); got != nil {
		t.Errorf("empty query = %+v, want nothing", got)
	}
}
