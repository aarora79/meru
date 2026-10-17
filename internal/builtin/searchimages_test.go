// This file tests search_images with a fake searcher: a query in words, an
// image named by its path, the paths it refuses, and when the tool is off.

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/store"
)

// fakeImageSearcher returns fixed results and records the query it got.
type fakeImageSearcher struct {
	results []retrieve.Result
	got     retrieve.ImageQuery
	limit   int
}

func (f *fakeImageSearcher) SearchImages(ctx context.Context, q retrieve.ImageQuery, n int) ([]retrieve.Result, error) {
	f.got, f.limit = q, n
	return f.results, nil
}

// imageTools returns built-in tools reading one [index] folder, dir, with
// s as search_images' searcher.
func imageTools(t *testing.T, dir string, s ImageSearcher) *Tools {
	t.Helper()
	ix, err := index.New(config.Index{Folders: []string{dir}, MaxFileMB: 1, Images: true}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(filepath.Join(dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil)
	if s != nil {
		tools.UseImageSearch(s)
	}
	return tools
}

// writePNG writes a small real PNG at p and returns its bytes.
func writePNG(t *testing.T, p string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSearchImagesTool(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	shot := filepath.Join(dir, "shot.png")
	data := writePNG(t, shot)
	found := retrieve.Result{ChunkWithDoc: store.ChunkWithDoc{Path: filepath.Join(dir, "beds.png"), Kind: retrieve.KindImage}, Score: 0.03}
	s := &fakeImageSearcher{results: []retrieve.Result{found}}
	tools := imageTools(t, dir, s)

	res, err := tools.Call(context.Background(), SearchImages, json.RawMessage(`{"query":"garden beds"}`))
	if err != nil || res.IsError {
		t.Fatalf("query: %+v, %v", res, err)
	}
	if s.got.Text != "garden beds" || s.got.Image != nil || s.limit != defaultImageLimit {
		t.Errorf("searched %+v with limit %d", s.got, s.limit)
	}
	if !strings.Contains(res.Text, "[1] "+filepath.Join(dir, "beds.png")+" (image)") || len(res.Sources) != 1 || res.Sources[0].Kind != "image" {
		t.Errorf("result = %q, sources %+v", res.Text, res.Sources)
	}

	args, _ := json.Marshal(map[string]string{"image": shot})
	res, err = tools.Call(context.Background(), SearchImages, args)
	if err != nil || res.IsError {
		t.Fatalf("image: %+v, %v", res, err)
	}
	if !bytes.Equal(s.got.Image, data) || !strings.Contains(res.Text, "images like") {
		t.Errorf("searched with %d bytes, result %q; want the picture at %s", len(s.got.Image), res.Text, shot)
	}
}

func TestSearchImagesRefusals(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	away, _ := filepath.EvalSymlinks(t.TempDir())
	writePNG(t, filepath.Join(away, "outside.png"))
	writePNG(t, filepath.Join(dir, "credentials.png"))
	if err := os.WriteFile(filepath.Join(dir, "fake.png"), []byte("text, named .png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(away, "outside.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	tools := imageTools(t, dir, &fakeImageSearcher{})
	tests := []struct {
		name, args, want string
	}{
		{"nothing to search for", `{}`, "pass query"},
		{"outside the folders", `{"image":"` + filepath.Join(away, "outside.png") + `"}`, "outside the folders"},
		{"a secret's name", `{"image":"` + filepath.Join(dir, "credentials.png") + `"}`, "off limits"},
		{"a link", `{"image":"` + filepath.Join(dir, "link.png") + `"}`, "off limits"},
		{"not an image by name", `{"image":"` + filepath.Join(dir, "notes.md") + `"}`, "isn't a PNG"},
		{"not an image by content", `{"image":"` + filepath.Join(dir, "fake.png") + `"}`, "isn't an image"},
		{"limit out of range", `{"query":"x","limit":99}`, "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := tools.Call(context.Background(), SearchImages, json.RawMessage(tt.args))
			if !res.IsError || !strings.Contains(res.Text, tt.want) {
				t.Errorf("result = %+v, want an error with %q", res, tt.want)
			}
		})
	}
}

func TestSearchImagesOff(t *testing.T) {
	dir := t.TempDir()
	tools := imageTools(t, dir, nil)
	for _, s := range tools.Tools() {
		if s.Name == SearchImages {
			t.Error("search_images offered with no image search")
		}
	}
	if why := tools.missing(SearchImages); !strings.Contains(why, "images") {
		t.Errorf("missing = %q", why)
	}
}
