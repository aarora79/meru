//go:build integration

// This file scores image search on the owner's own pictures, with the real
// local Ollama. The build tag above keeps it out of a plain `go test`. It
// reads bench/image-eval.jsonl, which git ignores: one line per image, its
// path and three questions that describe it. Copy the live index first, so
// the test never opens the one merud writes, then run
//
//	sqlite3 ~/.meru/meru.db ".backup /tmp/eval.db"
//	MERU_IMAGE_EVAL_DB=/tmp/eval.db go test -tags integration -v -run ImageEval ./internal/retrieve/
//
// It skips without MERU_IMAGE_EVAL_DB, without the eval file, or when
// Ollama isn't running. MERU_TEST_OLLAMA and MERU_TEST_EMBED_MODEL override
// the address and the model, which must be the one that built the index.

package retrieve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image"
	_ "image/gif" // the blank imports let image.Decode read GIF and PNG files
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
)

// evalImage is one line of bench/image-eval.jsonl. The struct tags name
// the JSON keys each field reads.
type evalImage struct {
	Path    string   `json:"path"`
	Queries []string `json:"queries"`
}

// evalEnvOr returns the environment variable key, or def when it is unset.
func evalEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// readEval reads the eval file at p, with "~" in each path turned into the
// home folder. It skips the test when the file is missing.
func readEval(t *testing.T, p string) []evalImage {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Skipf("no eval file: %v", err)
	}
	// defer runs f.Close() when readEval returns.
	defer f.Close()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var out []evalImage
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e evalImage
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if rest, ok := strings.CutPrefix(e.Path, "~/"); ok {
			e.Path = filepath.Join(home, rest)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// rankOf returns where path sits in results, from 1, or 0 when it's absent.
func rankOf(results []Result, path string) int {
	for i, r := range results {
		if r.Path == path {
			return i + 1
		}
	}
	return 0
}

// TestImageEval measures two things from the image search testing plan,
// and reports each against its bar without failing, as the router's eval
// does: the numbers are for a person to weigh.
//
//   - text to image: the bar is each question finding its image first at
//     least half the time, and in the top five at least four times in five;
//   - image to image: each image, saved again as a JPEG at 80% quality,
//     should find its original first. An animated GIF keeps only its first
//     frame, so it can miss.
func TestImageEval(t *testing.T) {
	dbPath := os.Getenv("MERU_IMAGE_EVAL_DB")
	if dbPath == "" {
		t.Skip("set MERU_IMAGE_EVAL_DB to a copy of meru.db")
	}
	images := readEval(t, filepath.Join("..", "..", "bench", "image-eval.jsonl"))
	baseURL := evalEnvOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := evalEnvOr("MERU_TEST_EMBED_MODEL", "embeddinggemma-2:440m")

	eng, err := engine.NewOllama(baseURL, "5m", model, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The prefixes and context come from the same table merud reads, so
	// the questions embed as a real turn's do.
	em := config.FindEmbedModel(model)
	eng.EmbedPrompts = engine.Prompts{Query: em.Query, Document: em.Document}
	eng.EmbedContext = em.Context

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := eng.Info(ctx); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}
	probe, err := eng.Embed(ctx, engine.Texts("probe"))
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	st, err := store.Open(ctx, store.Options{Path: dbPath, EmbedModel: model, Dims: len(probe[0])})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var asked, first, top5 int
	for _, im := range images {
		for _, q := range im.Queries {
			results, err := SearchImages(ctx, st, eng, ImageQuery{Text: q}, 5)
			if err != nil {
				t.Fatal(err)
			}
			asked++
			switch r := rankOf(results, im.Path); {
			case r == 1:
				first++
				top5++
			case r > 1:
				top5++
				t.Logf("rank %d: %q", r, q)
			default:
				t.Logf("missed: %q", q)
			}
		}
	}
	t.Logf("text to image: first %d of %d, top five %d of %d", first, asked, top5, asked)
	if asked == 0 {
		t.Fatal("the eval file holds no questions")
	}
	if 2*first < asked || 5*top5 < 4*asked {
		t.Log("text to image misses the bar: first in at least half, top five in at least four in five")
	} else {
		t.Log("text to image meets the bar")
	}

	var tried, found int
	for _, im := range images {
		data, err := os.ReadFile(im.Path)
		if err != nil {
			t.Fatal(err)
		}
		// image.Decode reads any format registered by the imports above;
		// WebP isn't one, so a WebP image sits this check out.
		pic, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Logf("can't decode %s: %v", filepath.Base(im.Path), err)
			continue
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, pic, &jpeg.Options{Quality: 80}); err != nil {
			t.Fatal(err)
		}
		results, err := SearchImages(ctx, st, eng, ImageQuery{Image: b.Bytes()}, 5)
		if err != nil {
			t.Fatal(err)
		}
		tried++
		if r := rankOf(results, im.Path); r == 1 {
			found++
		} else {
			t.Logf("image to image: %s ranked %d", filepath.Base(im.Path), r)
		}
	}
	t.Logf("image to image: first %d of %d", found, tried)
}
