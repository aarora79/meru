// This file tests view_image: a picture it returns for the model, the paths
// it refuses, the turn's limit, and when it is off.

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

func TestViewImage(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	away, _ := filepath.EvalSymlinks(t.TempDir())
	data := writePNG(t, filepath.Join(dir, "shot.png"))
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
	tools := imageTools(t, dir, nil)

	// path returns the JSON arguments for a call on p.
	path := func(p string) string {
		b, _ := json.Marshal(map[string]string{"path": p})
		return string(b)
	}
	tests := []struct {
		name    string
		args    string
		want    string // a piece of the result's text
		isError bool
	}{
		{"an image in the folders", path(filepath.Join(dir, "shot.png")), "is attached to this result", false},
		{"no path", `{}`, "view_image: pass path", true},
		{"outside the folders", path(filepath.Join(away, "outside.png")), "outside the folders", true},
		{"a secret's name", path(filepath.Join(dir, "credentials.png")), "off limits", true},
		{"a link", path(filepath.Join(dir, "link.png")), "off limits", true},
		{"not an image by name", path(filepath.Join(dir, "notes.md")), "isn't a PNG", true},
		{"not an image by content", path(filepath.Join(dir, "fake.png")), "isn't an image", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tools.Call(context.Background(), ViewImage, json.RawMessage(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tt.isError || !strings.Contains(res.Text, tt.want) {
				t.Fatalf("result = %+v; want IsError %v and %q", res, tt.isError, tt.want)
			}
			if tt.isError {
				if !strings.HasPrefix(res.Text, "view_image: ") || res.Images != nil {
					t.Errorf("refusal = %q with %d images; want a view_image: error and no picture", res.Text, len(res.Images))
				}
				return
			}
			// The picture rides in Images, never in the text the logs keep.
			if len(res.Images) != 1 || !bytes.Equal(res.Images[0], data) {
				t.Errorf("Images = %d pictures; want the file's bytes", len(res.Images))
			}
			if len(res.Sources) != 1 || res.Sources[0].Kind != "image" || res.Sources[0].Path != filepath.Join(dir, "shot.png") {
				t.Errorf("Sources = %+v; want the picture as an image source", res.Sources)
			}
		})
	}
}

// TestViewImageCapped checks that a call the agent marks past the turn's
// limit reads no file and says why.
func TestViewImageCapped(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	writePNG(t, filepath.Join(dir, "shot.png"))
	tools := imageTools(t, dir, nil)
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "shot.png")})
	res, err := tools.Call(WithViewCapped(context.Background(), 8), ViewImage, args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Text, "looked at 8 pictures") || res.Images != nil {
		t.Errorf("capped call = %+v; want a refusal that names the limit and no picture", res)
	}
}

// TestViewImageOff checks that view_image needs folders to read and a
// place in [builtin] tools.
func TestViewImageOff(t *testing.T) {
	offered := func(tools *Tools) bool {
		for _, s := range tools.Tools() {
			if s.Name == ViewImage {
				return true
			}
		}
		return false
	}
	if !offered(imageTools(t, t.TempDir(), nil)) {
		t.Error("view_image not offered with a folder to read")
	}
	bare := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	if offered(bare) {
		t.Error("view_image offered with no folders")
	}
	if why := bare.missing(ViewImage); !strings.Contains(why, "folders") {
		t.Errorf("missing = %q", why)
	}
}
