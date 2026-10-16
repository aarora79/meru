//go:build e2e

// This file checks that merud indexes images with an embedding model that
// takes them, sending each as {"image": base64} to Ollama, and leaves them
// out with [index] images = false.

package e2e

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// imageEmbedModel is an embedding model Meru knows takes images; the fake
// Ollama answers for any name.
const imageEmbedModel = "embeddinggemma-2:440m"

// pngFile writes a small PNG of one colour at p.
func pngFile(t *testing.T, p string, shade uint8) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{shade, 90, 160, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIndexImagesE2E(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("images=%v", on), func(t *testing.T) {
			dir, err := os.MkdirTemp("", "meru-images-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			dir, _ = filepath.EvalSymlinks(dir)
			if err := os.WriteFile(filepath.Join(dir, "garden.md"), []byte("# Garden\n\nSow tomatoes in April.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			pngFile(t, filepath.Join(dir, "plan.png"), 20)
			pngFile(t, filepath.Join(dir, "beds.png"), 200)

			f := startFake(t)
			h := newHome(t)
			h.writeConfig(t, fmt.Sprintf(
				"[ollama]\nbase_url = %q\n\n[models]\nfast = %q\nmain = %q\nembed = %q\n\n[web]\nsearxng_url = \"\"\n\n[index]\nfolders = [%q]\nimages = %v\n",
				f.url, fastModel, mainModel, imageEmbedModel, dir, on))
			m := startMerud(t, h, nil)
			waitReady(t, h, m, readyTimeout)

			want := "1 file,"
			if on {
				want = "3 files,"
			}
			waitForStatus(t, h, want)

			// The images went to Ollama as image objects, never as text.
			images := 0
			for _, r := range f.requests(t) {
				if r.Path == "/api/embed" && strings.Contains(string(r.Body), `"image":`) {
					images++
				}
			}
			if on && images != 2 || !on && images != 0 {
				t.Errorf("images=%v: %d embed requests carried an image", on, images)
			}
		})
	}
}
