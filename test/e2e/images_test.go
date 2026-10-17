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

// TestFindLikeThisE2E attaches a copy of one indexed picture with
// meru -image and checks that merud lists that picture first. The fake
// main model has no vision, so merud answers with the list itself, and the
// answer is the same on every run.
func TestFindLikeThisE2E(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "meru-images-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	pngFile(t, filepath.Join(dir, "plan.png"), 20)
	pngFile(t, filepath.Join(dir, "beds.png"), 200)
	// The picture to attach: the same bytes as beds.png, kept outside the
	// indexed folder.
	away := t.TempDir()
	attach := filepath.Join(away, "like-this.png")
	pngFile(t, attach, 200)

	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, fmt.Sprintf(
		"[ollama]\nbase_url = %q\n\n[models]\nfast = %q\nmain = %q\nembed = %q\n\n[web]\nsearxng_url = \"\"\n\n[index]\nfolders = [%q]\n",
		f.url, fastModel, mainModel, imageEmbedModel, dir))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)
	waitForStatus(t, h, "2 files,")

	res := runMeru(t, h, "-image", attach, "find images like this")
	if res.code != 0 {
		t.Fatalf("meru exited %d:\n%s\nmerud.log:\n%s", res.code, res.stderr, h.log())
	}
	first := strings.Index(res.stdout, "beds.png")
	second := strings.Index(res.stdout, "plan.png")
	if first < 0 || second < 0 || first > second {
		t.Errorf("answer doesn't list beds.png before plan.png:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "most like the one you attached") {
		t.Errorf("answer:\n%s", res.stdout)
	}
}
