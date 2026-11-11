// Tests for the rule that spots a question about Meru itself (self.go).

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// TestAboutMeru holds questions on both sides of the line. The first group
// asks about Meru and should run about_meru first; the second asks Meru to
// do something, or asks about the user's own things, and should take the
// usual path.
func TestAboutMeru(t *testing.T) {
	yes := []string{
		"do you send any trackign data toi the cloud'",
		"do you send any tracking data to the cloud?",
		"or any sort of telemetry, do you collect any?",
		"Does Meru send telemetry?",
		"is meru private",
		"what model are you",
		"which model are you running?",
		"who are you",
		"what's your name",
		"what version are you",
		"do you keep logs of my questions",
		"are you online?",
		"does any of my data leave this computer when I use meru",
		"how do you work",
		"do you phone home",
		"do you store my data in the cloud",
	}
	no := []string{
		"can you search my files for telemetry",
		"check my repo for tracking code",
		"does my repo send telemetry",
		"what does the mcp gateway registry send to its telemetry endpoint",
		"could you send this to Sam",
		"can you run the tests",
		"summarize the data in sales.csv",
		"what's the weather in Boston",
		"do you know the capital of France",
		"find my notes on cloud costs",
		"draft an email about our telemetry plan",
		"what is telemetry",
	}
	for _, q := range yes {
		if !aboutMeru(q) {
			t.Errorf("aboutMeru(%q) = false, want true: it asks about Meru", q)
		}
	}
	for _, q := range no {
		if aboutMeru(q) {
			t.Errorf("aboutMeru(%q) = true, want false: it isn't about Meru", q)
		}
	}
}

// TestSelfQuestionTurn replays the turn that drove self.go: "do you send any
// tracking data to the cloud", routed to search+tools, with the user's files
// full of another project's telemetry. The turn runs about_meru through
// dispatch before the model's first round, as merud's own call, puts its
// facts in the prompt, and searches no files. A question about the user's
// own repo still searches them.
func TestSelfQuestionTurn(t *testing.T) {
	files := []retrieve.Result{result("/notes/registry/telemetry.md", "Telemetry", "The registry sends anonymous telemetry.", 1, 3, 0.03)}
	for _, tt := range []struct {
		question   string
		wantSelf   bool
		wantSearch bool
	}{
		{"do you send any tracking data to the cloud?", true, false},
		{"does my registry repo send telemetry?", false, true},
	} {
		t.Run(tt.question, func(t *testing.T) {
			cfg := ollamaConfig(t)
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Text: "Meru sends one anonymous count a day, when you turn it on."})
			rec := &rowRecorder{}
			tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()},
				config.Web{}, nil, "", nil, nil, nil)
			tools.UseAbout(func(context.Context) builtin.About {
				return builtin.About{Version: "v0.7.6", Profile: "lite", Main: "qwen3.6:35b", UsageReport: "off. Meru sends nothing about itself."}
			})
			search := &fakeSearcher{results: files}
			a := webAgent(t, cfg, srv, "search+tools", search, dispatch.New([]dispatch.Backend{tools}, rec, dispatch.Options{}))

			evs, err := run(context.Background(), a, rpc.Request{Text: tt.question})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			rows := rec.all()
			ranSelf := len(rows) == 1 && rows[0].Tool == builtin.AboutMeru && rows[0].Caller == dispatch.CallerMeru
			if ranSelf != tt.wantSelf {
				t.Errorf("tool_calls rows = %+v, want about_meru by meru: %v", rows, tt.wantSelf)
			}
			search.mu.Lock()
			searched := len(search.queries) > 0
			search.mu.Unlock()
			if searched != tt.wantSearch {
				t.Errorf("searched the files = %v, want %v", searched, tt.wantSearch)
			}
			system := chatBodies(t, srv, cfg.Models.Main)[0].Messages[0].Content
			if got := strings.Contains(system, selfIntroRule); got != tt.wantSelf {
				t.Errorf("system prompt holds the about_meru facts = %v, want %v", got, tt.wantSelf)
			}
			if tt.wantSelf {
				if strings.Contains(system, "The registry sends anonymous telemetry.") {
					t.Error("the other project's telemetry doc reached the prompt")
				}
				if lines := readLines(t, cfg, evs[0].Session); len(meruCalls(lines)) != 1 {
					t.Errorf("merud's calls in the transcript = %d, want 1", len(meruCalls(lines)))
				}
			}
		})
	}
}
