//go:build integration

// This file summarizes one session with the real local Ollama and logs the
// summary and how long it took. The build tag above keeps it out of a plain
// `go test`; run it with
//
//	go test -tags integration -v -run Integration ./internal/summarize/
//
// It skips when Ollama isn't running. MERU_TEST_OLLAMA,
// MERU_TEST_FAST_MODEL and MERU_TEST_EMBED_MODEL override the address and
// the models.

package summarize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// envOr returns the environment variable key, or def when it is unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestIntegrationSummarize(t *testing.T) {
	baseURL := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_FAST_MODEL", "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M")
	embedModel := envOr("MERU_TEST_EMBED_MODEL", "nomic-embed-text")
	eng, err := engine.NewOllama(baseURL, "5m", embedModel, nil, nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := eng.Info(ctx); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}
	vecs, err := eng.Embed(ctx, []string{"probe"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: embedModel, Dims: len(vecs[0])})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Warm the model, so the timing below measures a warm call.
	if _, err := eng.Generate(ctx, []engine.Message{{Role: engine.RoleUser, Content: "hi"}}, nil,
		engine.Options{Model: model, MaxTokens: 1, NoThink: true}); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "sessions")
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	week := time.Now().AddDate(0, 0, -7)
	for i, text := range []string{
		"Let's plan the vegetable garden. How many raised beds should we build this season?",
		"Two beds of 1.2 by 2.4 metres fit the sunny strip, with room for a path.",
		"OK, let's go with two. Should I start tomatoes from seed?",
		"Yes. Start them indoors six weeks before the last frost, around early April where you live.",
	} {
		typ := transcript.TypeUser
		if i%2 == 1 {
			typ = transcript.TypeAssistant
		}
		if err := sess.Append(transcript.Line{TS: week.Add(time.Duration(i) * time.Minute), Type: typ, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ReplaySessions(ctx, dir); err != nil {
		t.Fatal(err)
	}

	s := New(st, eng, model, 30*time.Minute, dir, nil)
	start := time.Now()
	if n := s.Tick(ctx); n != 1 {
		t.Fatalf("Tick wrote %d summaries, want 1", n)
	}
	took := time.Since(start)
	rows, err := st.Sessions(ctx, []string{sess.ID()})
	if err != nil || len(rows) != 1 {
		t.Fatalf("Sessions: %v", err)
	}
	t.Logf("summary in %s (summary and embedding): %q", took.Round(time.Millisecond), rows[0].Summary)
	if sum := strings.ToLower(rows[0].Summary); !strings.Contains(sum, "two") && !strings.Contains(sum, "2") {
		t.Logf("the summary lost the bed count")
	}
	if todo, _ := st.SummariesWithoutVector(ctx, 5); len(todo) != 0 {
		t.Errorf("summary not embedded")
	}
}
