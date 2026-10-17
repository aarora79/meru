// This file tests the searches limited to one kind of document, which
// image search uses.

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

func TestSearchKind(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "fake", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	add := func(path, kind, text string, v engine.Vector) {
		t.Helper()
		doc := Document{Path: path, Kind: kind, MTime: time.Now()}
		if err := st.ReplaceDocument(ctx, doc, []Chunk{{Heading: filepath.Base(path), Text: text}}, []engine.Vector{v}); err != nil {
			t.Fatal(err)
		}
	}
	// The text sits nearest the query and holds its word; the images don't.
	add("/n/garden.md", "markdown", "garden beds in April", engine.Vector{1, 0, 0})
	add("/n/img/beds.png", "image", "Image beds.png in /n/img garden", engine.Vector{0.9, 0.3, 0})
	add("/n/img/cat.png", "image", "Image cat.png in /n/img", engine.Vector{0, 1, 0})

	hits, err := st.SearchVectorKind(ctx, engine.Vector{1, 0, 0}, "image", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("vector hits = %+v, want the two images only", hits)
	}
	chunks, err := st.Chunks(ctx, []int64{hits[0].ChunkID, hits[1].ChunkID})
	if err != nil {
		t.Fatal(err)
	}
	if chunks[0].Path != "/n/img/beds.png" || chunks[1].Path != "/n/img/cat.png" {
		t.Errorf("vector order = %s, %s; want beds.png then cat.png", chunks[0].Path, chunks[1].Path)
	}

	kw, err := st.SearchKeywordKind(ctx, "garden", "image", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(kw) != 1 {
		t.Fatalf("keyword hits = %+v, want beds.png alone", kw)
	}
	if _, err := st.SearchVectorKind(ctx, engine.Vector{1, 0}, "image", 10); err == nil {
		t.Error("a query of the wrong size passed")
	}
}
