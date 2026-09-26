// This file tests the retry after output Ollama couldn't read, against the
// fake Ollama: a round whose stream ends with an {"error": ...} line gets
// one more try with the same tools and a nudge, and a turn whose retry
// fails too answers with a readable message instead of Ollama's error.

package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// xmlError is the error line Ollama 0.34 sent when its qwen tool-call
// parser rejected what the model wrote.
const xmlError = "XML syntax error on line 8: element <function> closed by </parameter>"

func TestBadOutputRetry(t *testing.T) {
	// broken streams a few words, then Ollama's error line, as Ollama does
	// when the model's tool call after them doesn't parse.
	broken := fakeollama.Reply{Chunks: []string{"Let me ", "look."}, StreamError: xmlError, FailAfter: 2}
	// brokenQuiet fails before any text reaches the user.
	brokenQuiet := fakeollama.Reply{StreamError: xmlError}
	search := fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{
		{Name: "web_search", Arguments: map[string]any{"query": "latest open model from a large lab"}},
	}}
	tests := []struct {
		name      string
		maxRounds int
		replies   []fakeollama.Reply
		// wantShown is every token the user read; wantAnswer is the
		// transcript's answer, the last round's text.
		wantShown   string
		wantAnswer  string
		wantOutcome string // the transcript's outcome field; "" for a full answer
		wantCalls   int    // main-model calls
		wantRetry   bool
	}{
		{
			name:       "the retry calls the tool and answers",
			replies:    []fakeollama.Reply{broken, search, {Text: "The newest one came out in June."}},
			wantShown:  "Let me look.\n\nThe newest one came out in June.",
			wantAnswer: "The newest one came out in June.",
			wantCalls:  3,
			wantRetry:  true,
		},
		{
			name:       "no text before the error, then the retry answers",
			replies:    []fakeollama.Reply{brokenQuiet, {Text: "The newest one came out in June."}},
			wantShown:  "The newest one came out in June.",
			wantAnswer: "The newest one came out in June.",
			wantCalls:  2,
			wantRetry:  true,
		},
		{
			name:        "the retry fails too",
			replies:     []fakeollama.Reply{broken, brokenQuiet, {Text: "never asked for"}},
			wantShown:   "Let me look.\n\n" + badOutputAnswer,
			wantAnswer:  badOutputAnswer,
			wantOutcome: endBadOutput,
			wantCalls:   2,
			wantRetry:   true,
		},
		{
			name:        "the error comes on the last round",
			maxRounds:   2,
			replies:     []fakeollama.Reply{search, brokenQuiet, {Text: "never asked for"}},
			wantShown:   badOutputOnce,
			wantAnswer:  badOutputOnce,
			wantOutcome: endBadOutput,
			wantCalls:   2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans := recordSpans(t)
			cfg := ollamaConfig(t)
			if tt.maxRounds > 0 {
				cfg.Agent.MaxRounds = tt.maxRounds
			}
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, tt.replies...)
			tools := &fakeTools{
				specs:   []engine.ToolSpec{spec("web_search")},
				results: map[string]fakeResult{"web_search": {text: "1. A lab released a new open model in June."}},
			}
			eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}},
				nil, tools, nil, nil, log)

			evs, err := run(context.Background(), a, rpc.Request{Text: "whats the newest open model"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := answerOf(evs); got != tt.wantShown {
				t.Errorf("shown = %q, want %q", got, tt.wantShown)
			}
			if strings.Contains(answerOf(evs), "XML") {
				t.Errorf("the chat shows Ollama's error: %q", answerOf(evs))
			}
			// Ollama's error goes to the log at warn, with the model.
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "model="+cfg.Models.Main) ||
				!strings.Contains(logs.String(), "closed by </parameter>") {
				t.Errorf("log has no warn line with the model and Ollama's error:\n%s", logs.String())
			}

			bodies := chatBodies(t, srv, cfg.Models.Main)
			if len(bodies) != tt.wantCalls {
				t.Fatalf("main model got %d calls, want %d", len(bodies), tt.wantCalls)
			}
			if tt.wantRetry {
				// The retry offers the failed round's tools and ends with
				// the nudge, as a user message, after the failed round's
				// messages. The failed round's text isn't among them.
				failed, retry := bodies[0], bodies[1]
				if len(retry.Tools) != len(failed.Tools) || len(retry.Tools) == 0 {
					t.Errorf("the retry offered %d tools, want the failed round's %d", len(retry.Tools), len(failed.Tools))
				}
				last := retry.Messages[len(retry.Messages)-1]
				if last.Role != string(engine.RoleUser) || last.Content != outputNudge {
					t.Errorf("the retry's last message = %+v, want the nudge as a user message", last)
				}
				if n, m := len(retry.Messages), len(failed.Messages); n != m+1 {
					t.Errorf("the retry has %d messages, want the failed round's %d plus the nudge", n, m)
				}
			}
			// No call after the retry carries the nudge.
			for i, b := range bodies[min(2, len(bodies)):] {
				for _, m := range b.Messages {
					if m.Content == outputNudge {
						t.Errorf("call %d after the retry carries the nudge", i+3)
					}
				}
			}

			// The transcript holds the user's question once, never the
			// nudge, and the answer with its outcome.
			lines := readLines(t, cfg, evs[0].Session)
			users := 0
			for _, l := range lines {
				if l.Type == transcript.TypeUser {
					users++
				}
				if l.Text == outputNudge {
					t.Errorf("the transcript records the nudge: %+v", l)
				}
			}
			if users != 1 {
				t.Errorf("transcript has %d user lines, want 1", users)
			}
			last := lines[len(lines)-1]
			if last.Type != transcript.TypeAssistant || last.Text != tt.wantAnswer || last.Outcome != tt.wantOutcome {
				t.Errorf("last transcript line = %+v, want the answer %q with outcome %q", last, tt.wantAnswer, tt.wantOutcome)
			}

			outcome := "ok"
			if tt.wantOutcome != "" {
				outcome = tt.wantOutcome
			}
			st := spanTree{spans: spans.Ended()}
			st.find(t, "meru.turn",
				attribute.Bool("meru.turn.output_retry", tt.wantRetry),
				attribute.String("meru.turn.outcome", outcome),
				attribute.Int("meru.turn.iterations", tt.wantCalls))
		})
	}
}

// TestHTTPErrorNoRetry checks that a failure before the stream starts, an
// HTTP 500, still fails the turn with Ollama's error and gets no retry.
func TestHTTPErrorNoRetry(t *testing.T) {
	spans := recordSpans(t)
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Status: 500, Error: "model crashed"}, fakeollama.Reply{Text: "never asked for"})
	a := ollamaAgent(t, cfg, srv, "direct", nil)

	_, err := run(context.Background(), a, rpc.Request{Text: "whats the newest open model"})
	var apiErr *engine.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 || !strings.Contains(err.Error(), "model crashed") {
		t.Fatalf("Handle error = %v, want Ollama's 500 with its message", err)
	}
	if n := len(chatBodies(t, srv, cfg.Models.Main)); n != 1 {
		t.Errorf("main model got %d calls, want 1", n)
	}
	st := spanTree{spans: spans.Ended()}
	st.find(t, "meru.turn",
		attribute.Bool("meru.turn.output_retry", false),
		attribute.String("meru.turn.outcome", "error"))
}

// TestBadOutputGetsNoNotice checks that a turn ending bad_output gets no
// unbacked-claim notice, even when the failed round's text claimed an
// action before Ollama's error. No answer came, so nothing claimed
// anything: the user reads badOutputAnswer and no "notice" event.
func TestBadOutputGetsNoNotice(t *testing.T) {
	spans := recordSpans(t)
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	claims := fakeollama.Reply{Chunks: []string{"Done. ", "I moved the file to the archive folder."},
		StreamError: xmlError, FailAfter: 2}
	// Both rounds claim the move, so the answer text endTurn builds starts
	// with a claim that claimsAction would flag.
	srv.Enqueue(cfg.Models.Main, claims, claims)
	tools := &fakeTools{specs: []engine.ToolSpec{spec("web_search")}}
	a := ollamaAgent(t, cfg, srv, "tools", tools)

	evs, err := run(context.Background(), a, rpc.Request{Text: "move the report to the archive folder"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == rpc.EventNotice {
			t.Errorf("a bad_output turn sent a notice: %+v", ev)
		}
	}
	lines := readLines(t, cfg, evs[0].Session)
	last := lines[len(lines)-1]
	want := "Done. I moved the file to the archive folder.\n\n" + badOutputAnswer
	if last.Outcome != endBadOutput || last.Notice != "" || last.Text != want {
		t.Errorf("last transcript line = %+v, want %q with outcome %q and no notice", last, want, endBadOutput)
	}
	if !claimsAction(last.Text) {
		t.Fatalf("claimsAction(%q) = false; the test needs a text that claims an action", last.Text)
	}
	st := spanTree{spans: spans.Ended()}
	st.find(t, "meru.turn",
		attribute.Bool("meru.turn.unbacked_claim", false),
		attribute.String("meru.turn.outcome", endBadOutput))
}
