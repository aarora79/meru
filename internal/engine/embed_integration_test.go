//go:build integration

// This file checks image and long-text embedding against the real local
// Ollama with a model that embeds images. Run it with
//
//	go test -tags integration -v -run IntegrationEmbed ./internal/engine/
//
// It skips when Ollama isn't running or lacks the model.
// MERU_TEST_IMAGE_EMBED_MODEL overrides the model, embeddinggemma-2:440m.

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// testImage draws a 256-pixel square picture: blue and yellow stripes, or
// a red disc on white, so two calls make two plainly different images with
// no files needed. (Stripes against a checkerboard in the same two colors
// scored 0.907: alike to the model, as they are to a person.)
func testImage(stripes bool) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := range 256 {
		for x := range 256 {
			var c color.RGBA
			switch {
			case stripes && (x/32)%2 == 0:
				c = color.RGBA{20, 60, 160, 255}
			case stripes:
				c = color.RGBA{240, 200, 40, 255}
			case (x-128)*(x-128)+(y-128)*(y-128) < 80*80:
				c = color.RGBA{200, 30, 30, 255}
			default:
				c = color.RGBA{255, 255, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// encode writes img as PNG, or JPEG when asJPEG is set.
func encode(t *testing.T, img image.Image, asJPEG bool) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	if asJPEG {
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// cosine is the cosine of the angle between two vectors: 1 for the same
// direction, near 0 for unrelated ones.
func cosine(a, b Vector) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / math.Sqrt(na*nb)
}

func TestIntegrationEmbedImages(t *testing.T) {
	baseURL := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_IMAGE_EMBED_MODEL", "embeddinggemma-2:440m")
	e, err := NewOllama(baseURL, "5m", model, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	em := config.FindEmbedModel(model)
	e.EmbedPrompts = Prompts{Query: em.Query, Document: em.Document}
	e.EmbedContext = em.Context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := e.Embed(ctx, Texts("probe")); err != nil {
		t.Skipf("%s not available at %s: %v", model, baseURL, err)
	}

	stripesPNG := encode(t, testImage(true), false)
	stripesJPEG := encode(t, testImage(true), true)
	discPNG := encode(t, testImage(false), false)

	t.Run("different images differ, one image in two formats matches", func(t *testing.T) {
		vs, err := e.Embed(ctx, []Input{{Image: stripesPNG}, {Image: discPNG}, {Image: stripesJPEG}})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(vs) != 3 {
			t.Fatalf("got %d vectors for 3 images", len(vs))
		}
		differ, same := cosine(vs[0], vs[1]), cosine(vs[0], vs[2])
		t.Logf("stripes vs disc %.3f, stripes as PNG vs JPEG %.3f", differ, same)
		if differ > 0.9 {
			t.Errorf("stripes vs disc = %.3f, want under 0.9: the model may not be seeing the images", differ)
		}
		if same < 0.95 || same-differ < 0.05 {
			t.Errorf("stripes as PNG vs JPEG = %.3f, want over 0.95 and well above %.3f", same, differ)
		}
	})

	t.Run("a batch matches one call per image", func(t *testing.T) {
		batch, err := e.Embed(ctx, []Input{{Image: stripesPNG}, {Image: discPNG}})
		if err != nil {
			t.Fatal(err)
		}
		one, err := e.Embed(ctx, []Input{{Image: discPNG}})
		if err != nil {
			t.Fatal(err)
		}
		if c := cosine(batch[1], one[0]); c < 0.999 {
			t.Errorf("batch[1] vs its own call = %.4f, want the same vector", c)
		}
	})

	t.Run("num_ctx lets a long text past 2048 tokens", func(t *testing.T) {
		// Ask Ollama directly, as Embed would, to read prompt_eval_count,
		// which Embed doesn't return.
		long := strings.Repeat("Tomatoes go in the sunny bed in April, beans by the fence. ", 600)
		body, _ := json.Marshal(embedRequest{Model: model, Input: []embedItem{{Text: long}}, Options: &embedOptions{NumCtx: em.Context}})
		resp, err := http.Post(baseURL+"/api/embed", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r struct {
			PromptEvalCount int `json:"prompt_eval_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.PromptEvalCount <= 2048 {
			t.Errorf("prompt_eval_count = %d, want over 2048: num_ctx didn't take", r.PromptEvalCount)
		}
	})
}
