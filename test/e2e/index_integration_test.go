//go:build e2e && integration

// This file checks v0.2's "done when" against the real Ollama on this
// machine, with the lite profile: merud indexes a small notes folder, and a
// question about one note gets an answer that holds the note's number and
// names the note under Sources. It logs the scan time, the search time and
// the time to first token.
//
// Run it with:
//
//	go test -tags 'e2e integration' -count=1 -v -run IntegrationNotes ./test/e2e/...
//
// It skips itself when Ollama isn't running or a lite model isn't pulled.

package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// TestIntegrationNotesAnswer indexes notesFolder (see index_test.go) with
// the real embedding model and asks when the garden project sows tomatoes.
func TestIntegrationNotesAnswer(t *testing.T) {
	h := newHome(t)
	cfg, err := config.Load(h.config) // no file yet: the lite defaults
	if err != nil {
		t.Fatal(err)
	}
	skipUnlessOllamaHas(t, cfg.Ollama.BaseURL, cfg.Models.Fast, cfg.Models.Main, cfg.Models.Embed)

	notes := notesFolder(t)
	// -v below puts the agent's "search done" line, with its time, in the log.
	h.writeConfig(t, fmt.Sprintf("[index]\nfolders = [%q]\n", notes))
	m := startProc(t, "merud", nil, "-config", h.config, "-socket", h.socket, "-v")
	waitReady(t, h, m, 5*time.Minute)
	waitForStatus(t, h, "4 files", "Scanning:   no")
	t.Logf("index status:\n%s", indexStatus(t, h))

	// The question says "my notes", as a person asking about their files
	// would, which steers the router to search.
	const question = "According to my notes, when does the garden project sow tomatoes?"
	timedAsk(t, h.socket, "Say hello in one word.") // warm-up, not measured

	start := time.Now()
	var ttft time.Duration
	var answer strings.Builder
	var sources []rpc.Citation
	var route string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for ev, err := range rpc.Do(ctx, h.socket, rpc.Request{Op: rpc.OpAsk, Text: question, Source: rpc.SourceCLI}, nil) {
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		switch ev.Type {
		case rpc.EventRoute:
			route = fmt.Sprintf("%s (%.2f, fallback %v)", ev.Route, ev.Confidence, ev.Fallback)
		case rpc.EventSources:
			sources = ev.Sources
		case rpc.EventToken:
			if ttft == 0 {
				ttft = time.Since(start)
			}
			answer.WriteString(ev.Text)
		case rpc.EventError:
			t.Fatalf("merud replied with an error: %s", ev.Error)
		}
	}
	t.Logf("route: %s", route)
	t.Logf("time to first token: %v, whole answer: %v", ttft, time.Since(start))
	t.Logf("search time: %s", searchTime(h.log()))
	t.Logf("answer: %q", answer.String())
	for _, s := range sources {
		t.Logf("source %s (score %.4f)", s, s.Score)
	}

	// A model may write "April 12" or "12 April", so look for each part.
	if !strings.Contains(answer.String(), "April") || !strings.Contains(answer.String(), "12") {
		t.Errorf("answer %q doesn't hold 12 April", answer.String())
	}
	if len(sources) == 0 || !strings.HasSuffix(sources[0].Path, "garden.md") {
		t.Errorf("sources = %+v, want garden.md first", sources)
	}

	// The same question through the meru binary prints a Sources list that
	// names the note.
	res := runMeru(t, h, question)
	t.Logf("meru printed:\n%s", res.stdout)
	if res.code != 0 || !strings.Contains(res.stdout, "\nSources:\n") || !strings.Contains(res.stdout, "garden.md") {
		t.Errorf("meru exited %d; want a Sources list naming garden.md\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
}

// searchTime pulls the time of the last search out of merud's debug log,
// such as "12ms", or says it found none.
func searchTime(log string) string {
	re := regexp.MustCompile(`msg="search done" .*? ms=(\d+)`)
	all := re.FindAllStringSubmatch(log, -1)
	if len(all) == 0 {
		return "(no search in the log)"
	}
	return all[len(all)-1][1] + "ms"
}
