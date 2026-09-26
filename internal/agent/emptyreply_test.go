// This file tests the empty-reply retry against the fake Ollama: a round
// that ends the turn with no text, only hidden thinking, gets one more
// model call with no tools and a nudge to answer, when the turn has a round
// left.

package agent

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

func TestEmptyReplyRetry(t *testing.T) {
	search := fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{
		{Name: "web_search", Arguments: map[string]any{"query": "latest open model from a large lab"}},
	}}
	thinking := fakeollama.Reply{Thinking: thinkingPieces(3)}
	tests := []struct {
		name        string
		route       string
		maxRounds   int
		replies     []fakeollama.Reply
		wantAnswer  string
		wantOutcome string // the transcript's outcome field; "" for a full answer
		wantCalls   int    // main-model calls
		wantRetry   bool
	}{
		{
			name:       "thinking only after a search, then the retry answers",
			route:      "tools",
			replies:    []fakeollama.Reply{search, thinking, {Text: "The newest one came out in June."}},
			wantAnswer: "The newest one came out in June.",
			wantCalls:  3,
			wantRetry:  true,
		},
		{
			name:       "thinking only with no tools, then the retry answers",
			route:      "direct",
			replies:    []fakeollama.Reply{thinking, {Text: "Run btop in a terminal."}},
			wantAnswer: "Run btop in a terminal.",
			wantCalls:  2,
			wantRetry:  true,
		},
		{
			name:        "the retry is empty too",
			route:       "tools",
			replies:     []fakeollama.Reply{search, thinking, thinking, {Text: "never asked for"}},
			wantAnswer:  sorry,
			wantOutcome: endGaveUp,
			wantCalls:   3,
			wantRetry:   true,
		},
		{
			name:        "thinking only on the last round",
			route:       "tools",
			maxRounds:   2,
			replies:     []fakeollama.Reply{search, thinking, {Text: "never asked for"}},
			wantAnswer:  sorry,
			wantOutcome: endGaveUp,
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
			a := ollamaAgent(t, cfg, srv, tt.route, tools)

			evs, err := run(context.Background(), a, rpc.Request{Text: "whats the newest open model"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := answerOf(evs); got != tt.wantAnswer {
				t.Errorf("answer = %q, want %q", got, tt.wantAnswer)
			}

			bodies := chatBodies(t, srv, cfg.Models.Main)
			if len(bodies) != tt.wantCalls {
				t.Fatalf("main model got %d calls, want %d", len(bodies), tt.wantCalls)
			}
			if tt.wantRetry {
				// The retry offers no tools and ends with the nudge, as a
				// user message, after the turn's messages. The empty
				// round's thinking isn't among them.
				retry := bodies[len(bodies)-1]
				if len(retry.Tools) != 0 {
					t.Errorf("the retry offered %d tools, want none", len(retry.Tools))
				}
				last := retry.Messages[len(retry.Messages)-1]
				if last.Role != string(engine.RoleUser) || last.Content != emptyNudge {
					t.Errorf("the retry's last message = %+v, want the nudge as a user message", last)
				}
				if n, m := len(retry.Messages), len(bodies[len(bodies)-2].Messages); n != m+1 {
					t.Errorf("the retry has %d messages, want the empty round's %d plus the nudge", n, m)
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
				if l.Text == emptyNudge {
					t.Errorf("the transcript records the nudge: %+v", l)
				}
			}
			if users != 1 {
				t.Errorf("transcript has %d user lines, want 1", users)
			}
			last := lines[len(lines)-1]
			if last.Type != transcript.TypeAssistant || last.Text != tt.wantAnswer || last.Outcome != tt.wantOutcome {
				t.Errorf("last transcript line = %+v, want the answer with outcome %q", last, tt.wantOutcome)
			}

			outcome := "ok"
			if tt.wantOutcome != "" {
				outcome = tt.wantOutcome
			}
			st := spanTree{spans: spans.Ended()}
			st.find(t, "meru.turn",
				attribute.Bool("meru.turn.empty_retry", tt.wantRetry),
				attribute.String("meru.turn.outcome", outcome),
				attribute.Int("meru.turn.iterations", tt.wantCalls))
		})
	}
}
