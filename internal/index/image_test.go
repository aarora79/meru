// This file tests images in the index: which files count as images, how
// one is stored, the files that are refused, and that the file tools still
// treat an image as media.

package index

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/retrieve"
)

// pngBytes returns a small, real PNG image, its color set by shade so two
// calls can make two different images.
func pngBytes(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{shade, 100, 200, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// imageConfig is testConfig with images on.
func imageConfig(root string) (cfg config.Index) {
	cfg = testConfig(root)
	cfg.Images = true
	return cfg
}

func TestKindImage(t *testing.T) {
	if KindImage != retrieve.KindImage {
		t.Fatalf("index.KindImage = %q, retrieve.KindImage = %q; they must match", KindImage, retrieve.KindImage)
	}
	on := &Indexer{images: true}
	off := &Indexer{}
	tests := []struct {
		name          string
		onKind, onWhy string
		offWhy        string
	}{
		{"shot.png", KindImage, "", ReasonMedia},
		{"PHOTO.JPG", KindImage, "", ReasonMedia},
		{"a.jpeg", KindImage, "", ReasonMedia},
		{"anim.gif", KindImage, "", ReasonMedia},
		{"pic.webp", KindImage, "", ReasonMedia},
		{"phone.heic", "", ReasonMedia, ReasonMedia},
		{"icon.svg", "", ReasonMedia, ReasonMedia},
		{"notes.md", KindMarkdown, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if k, why := on.kindOf(tt.name); k != tt.onKind || why != tt.onWhy {
				t.Errorf("images on: kindOf = %q, %q; want %q, %q", k, why, tt.onKind, tt.onWhy)
			}
			if _, why := off.kindOf(tt.name); why != tt.offWhy {
				t.Errorf("images off: reason = %q, want %q", why, tt.offWhy)
			}
		})
	}
}

func TestIsImageData(t *testing.T) {
	if !IsImageData(pngBytes(t, 10)) {
		t.Error("a real PNG isn't an image")
	}
	if IsImageData([]byte("just text, named .png")) {
		t.Error("text passed as an image")
	}
}

func TestIndexImage(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"notes.md": "# Garden\n\nsow in April"})
	if err := os.MkdirAll(filepath.Join(root, "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, shade := range map[string]uint8{"img/a.png": 10, "img/b.png": 200} {
		if err := os.WriteFile(filepath.Join(root, name), pngBytes(t, shade), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, sink, eng := newTestIndexer(t, imageConfig(root))
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 3 || eng.images != 2 {
		t.Errorf("scan = %+v with %d images embedded; want 3 indexed, 2 images", rep, eng.images)
	}
	e, ok := sink.entry(filepath.Join(root, "img", "a.png"))
	if !ok || e.doc.Kind != KindImage || len(e.chunks) != 1 || len(e.vecs) != 1 {
		t.Fatalf("a.png stored as %+v", e)
	}
	if c := e.chunks[0]; c.Heading != "a.png" || c.Text != "Image a.png in "+filepath.Join(root, "img") {
		t.Errorf("a.png chunk = %+v; want its name and folder", c)
	}

	// Unchanged images aren't embedded again.
	before := eng.images
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if eng.images != before {
		t.Errorf("a second scan embedded %d images again", eng.images-before)
	}
}

func TestIndexImagesOff(t *testing.T) {
	root := tempRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.png"), pngBytes(t, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, _, eng := newTestIndexer(t, testConfig(root))
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexed != 0 || rep.Skipped[ReasonMedia] != 1 || eng.images != 0 {
		t.Errorf("images off: scan = %+v, %d images embedded", rep, eng.images)
	}
}

func TestIndexImageRefused(t *testing.T) {
	root := tempRoot(t)
	files := map[string][]byte{
		"fake.png":        []byte("just text, named .png"),
		"credentials.png": pngBytes(t, 10), // matches the secret pattern credentials*
		"broken.png":      append(pngBytes(t, 20), []byte("refuse-me")...),
		"ok.png":          pngBytes(t, 30),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One over the 20 MiB cap: Truncate makes it that long without
	// writing 20 MiB.
	huge := filepath.Join(root, "huge.png")
	if err := os.WriteFile(huge, pngBytes(t, 40), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(huge, MaxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	ix, sink, eng := newTestIndexer(t, imageConfig(root))
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatalf("Scan stopped: %v; a refused image must fail only its own file", err)
	}
	if rep.Indexed != 1 || eng.images != 1 {
		t.Errorf("scan = %+v, %d images embedded; want only ok.png", rep, eng.images)
	}
	if rep.Failed != 2 || rep.Skipped[ReasonSecret] != 1 || rep.Skipped[ReasonTooLarge] != 1 {
		t.Errorf("scan = %+v; want fake.png and broken.png failed, credentials.png secret, huge.png too large", rep)
	}
	if _, ok := sink.entry(filepath.Join(root, "ok.png")); !ok {
		t.Error("ok.png wasn't stored")
	}
}

// TestFileToolsSkipImages checks that read_file and the other file tools
// still see an image as media when the index reads images.
func TestFileToolsSkipImages(t *testing.T) {
	root := tempRoot(t)
	p := filepath.Join(root, "a.png")
	if err := os.WriteFile(p, pngBytes(t, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, _, _ := newTestIndexer(t, imageConfig(root))
	c, err := ix.Check(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Reason != ReasonMedia {
		t.Errorf("Check(a.png).Reason = %q, want %q", c.Reason, ReasonMedia)
	}
}
