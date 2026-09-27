// This file tests the image rules: Upload copies a real PNG or JPEG as an
// image without read_file, and refuses a text file named like an image or
// one over the cap; Image reads back only a copy inside the uploads folder.

package builtin

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// tinyImage returns a 4x3 image, green like a garden bed, encoded as
// "png" or "jpeg".
func tinyImage(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for x := range 4 {
		for y := range 3 {
			img.Set(x, y, color.RGBA{G: 160, A: 255})
		}
	}
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUploadImage(t *testing.T) {
	pngBytes, jpegBytes := tinyImage(t, "png"), tinyImage(t, "jpeg")
	// An image needs no read_file, so these tools have no [index] folders
	// and read_file off; a file would be refused.
	tools, out, away := uploadTools(t, false, config.Builtin{})
	uploads := filepath.Join(out, uploadsFolder)

	writeFile(t, filepath.Join(away, "garden bed.png"), string(pngBytes))
	writeFile(t, filepath.Join(away, "receipt.JPG"), string(jpegBytes))
	writeFile(t, filepath.Join(away, "notes.png"), "Plant tomatoes in May.\n")
	writeFile(t, filepath.Join(away, "huge.jpg"), "")
	if err := os.Truncate(filepath.Join(away, "huge.jpg"), imageCap+1); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(away, "garden-plan.md"), "Plant tomatoes in May.\n")

	tests := []struct {
		name    string
		src     string
		want    string // the copy's name in uploads; "" when Upload must refuse
		refusal string
	}{
		{name: "png", src: filepath.Join(away, "garden bed.png"), want: "garden_bed.png"},
		{name: "jpeg with a capital ending", src: filepath.Join(away, "receipt.JPG"), want: "receipt.jpg"},
		{name: "text named like a png", src: filepath.Join(away, "notes.png"), refusal: "isn't a PNG, JPEG, GIF or WebP image"},
		{name: "over the image cap", src: filepath.Join(away, "huge.jpg"), refusal: "Meru takes images up to 20.0 MB"},
		{name: "a file still needs read_file", src: filepath.Join(away, "garden-plan.md"), refusal: "add a folder in Settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tools.Upload(tt.src)
			if tt.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tt.refusal) {
					t.Fatalf("Upload(%s) = %+v, %v; want a refusal with %q", tt.src, got, err, tt.refusal)
				}
				return
			}
			want := Uploaded{Path: filepath.Join(uploads, tt.want), Kind: rpc.AttachImage}
			if err != nil || got != want {
				t.Fatalf("Upload(%s) = %+v, %v; want %+v", tt.src, got, err, want)
			}
			src, _ := os.ReadFile(tt.src)
			if copied, err := tools.Image(got.Path); err != nil || !bytes.Equal(copied, src) {
				t.Errorf("Image(copy) = %d bytes, %v; want the %d bytes of the source", len(copied), err, len(src))
			}
		})
	}

	// Only the two images were copied.
	entries, err := os.ReadDir(uploads)
	if err != nil || len(entries) != 2 {
		t.Errorf("uploads holds %d entries, %v; want the two images", len(entries), err)
	}
}

func TestImageRefuses(t *testing.T) {
	pngBytes := tinyImage(t, "png")
	tools, out, away := uploadTools(t, false, config.Builtin{})
	uploads := filepath.Join(out, uploadsFolder)
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(away, "garden.png"), string(pngBytes))
	writeFile(t, filepath.Join(out, "outside.png"), string(pngBytes))
	writeFile(t, filepath.Join(uploads, "notes.png"), "not an image\n")
	writeFile(t, filepath.Join(uploads, "notes.md"), "Plant tomatoes in May.\n")
	if err := os.Symlink(filepath.Join(away, "garden.png"), filepath.Join(uploads, "link.png")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		refusal string
	}{
		{"outside uploads", filepath.Join(away, "garden.png"), "isn't an image you attached"},
		{"the output folder itself", filepath.Join(out, "outside.png"), "isn't an image you attached"},
		{"climbs out with ..", filepath.Join(uploads, "..", "outside.png"), "isn't an image you attached"},
		{"relative", filepath.Join("uploads", "garden.png"), "isn't an image you attached"},
		{"symbolic link", filepath.Join(uploads, "link.png"), "symbolic link"},
		{"text named like a png", filepath.Join(uploads, "notes.png"), "isn't a PNG, JPEG, GIF or WebP image"},
		{"not an image name", filepath.Join(uploads, "notes.md"), "isn't a PNG, JPEG, GIF or WebP image"},
		{"missing", filepath.Join(uploads, "gone.png"), "isn't in the uploads folder any more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := tools.Image(tt.path)
			if err == nil || !strings.Contains(err.Error(), tt.refusal) {
				t.Errorf("Image(%s) = %d bytes, %v; want a refusal with %q", tt.path, len(b), err, tt.refusal)
			}
		})
	}
}

func TestImageName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"garden bed.PNG", "garden_bed.png"},
		{"receipt.jpeg", "receipt.jpeg"},
		{"..png", "image.png"},
		{strings.Repeat("a", 150) + ".webp", strings.Repeat("a", maxNameChars-5) + ".webp"},
	}
	for _, tt := range tests {
		if got := imageName(tt.in); got != tt.want {
			t.Errorf("imageName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
