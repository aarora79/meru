// This file tests the limits that keep a turn from running forever, against
// the fake Ollama: the cap on tokens per model call, which counts hidden
// thinking, the turn's deadline, and the rounds cap. A turn that hits any
// of them still answers, with an apology or with the text so far and a
// note, and the transcript records how it ended.

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// thinkingPieces returns n pieces of hidden reasoning.
func thinkingPieces(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "hmm "
	}
	return out
}

// TestTurnLimits runs turns that end without a full answer and checks what
// the user reads, the outcome in the transcript, and that the turn itself
// doesn't fail.
func TestTurnLimits(t *testing.T) {
	// callsEveryRound makes the model call weather.now with a new city each
	// time, so no call is a repeat, and never write text.
	var callsEveryRound []fakeollama.Reply
	for i := range 3 {
		callsEveryRound = append(callsEveryRound, fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{
			{Name: "weather.now", Arguments: map[string]any{"city": fmt.Sprintf("city %d", i)}},
		}})
	}
	tests := []struct {
		name        string
		maxTokens   int
		timeout     string
		maxRounds   int
		replies     []fakeollama.Reply
		wantAnswer  string
		wantOutcome string
	}{
		{
			name:      "thinking hits the token cap",
			maxTokens: 5,
			// Without the cap, this model would think for 5,000 chunks.
			replies:     []fakeollama.Reply{{Thinking: thinkingPieces(5000), Text: "never sent"}},
			wantAnswer:  sorry,
			wantOutcome: endCutOff,
		},
		{
			name:    "thinking runs past the deadline",
			timeout: "200ms",
			// 5,000 chunks at 5 ms each would take 25 seconds.
			replies:     []fakeollama.Reply{{Thinking: thinkingPieces(5000), ChunkDelay: 5 * time.Millisecond, Text: "never sent"}},
			wantAnswer:  sorry,
			wantOutcome: endTimeout,
		},
		{
			name:        "text cut at the token cap",
			maxTokens:   3,
			replies:     []fakeollama.Reply{{Text: "btop shows your processes and more words"}},
			wantAnswer:  "btop shows your \n\n" + cutOffNote,
			wantOutcome: endCutOff,
		},
		{
			name:        "text cut at the deadline",
			timeout:     "300ms",
			replies:     []fakeollama.Reply{{Chunks: []string{"btop ", "shows ", "your ", "processes"}, ChunkDelay: time.Second}},
			wantAnswer:  "btop \n\n" + timeoutNote,
			wantOutcome: endTimeout,
		},
		{
			name:        "only tool calls until the rounds run out",
			maxRounds:   3,
			replies:     callsEveryRound,
			wantAnswer:  sorry,
			wantOutcome: endGaveUp,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			if tt.maxTokens > 0 {
				cfg.Agent.MaxOutputTokens = tt.maxTokens
			}
			if tt.timeout != "" {
				cfg.Agent.TurnTimeout = tt.timeout
			}
			if tt.maxRounds > 0 {
				cfg.Agent.MaxRounds = tt.maxRounds
			}
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, tt.replies...)
			tools := &fakeTools{
				specs:   []engine.ToolSpec{spec("weather.now")},
				results: map[string]fakeResult{"weather.now": {text: "sunny"}},
			}
			a := ollamaAgent(t, cfg, srv, "tools", tools)

			start := time.Now()
			evs, err := run(context.Background(), a, rpc.Request{Text: "how do I use btop?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("turn took %v; the limits didn't stop it", d)
			}
			if got := answerOf(evs); got != tt.wantAnswer {
				t.Errorf("answer = %q\nwant     %q", got, tt.wantAnswer)
			}
			if last := evs[len(evs)-1]; last.Type != rpc.EventDone {
				t.Errorf("last event = %s, want done", last.Type)
			}
			lines := readLines(t, cfg, evs[0].Session)
			last := lines[len(lines)-1]
			if last.Type != transcript.TypeAssistant || last.Text != tt.wantAnswer || last.Outcome != tt.wantOutcome {
				t.Errorf("last transcript line = %+v, want the answer with outcome %q", last, tt.wantOutcome)
			}
			// Every main-model call carries the cap as num_predict.
			for i, b := range chatBodies(t, srv, cfg.Models.Main) {
				if got, _ := b.Options["num_predict"].(float64); int(got) != cfg.Agent.MaxOutputTokens {
					t.Errorf("call %d num_predict = %v, want %d", i+1, b.Options["num_predict"], cfg.Agent.MaxOutputTokens)
				}
			}
		})
	}
}

// TestFullAnswerHasNoOutcome checks that a turn that answers in full keeps
// the outcome field empty in its transcript line.
func TestFullAnswerHasNoOutcome(t *testing.T) {
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Thinking: thinkingPieces(3), Text: "Run btop in a terminal."})
	a := ollamaAgent(t, cfg, srv, "direct", nil)
	evs, err := run(context.Background(), a, rpc.Request{Text: "how do I use btop?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := answerOf(evs); got != "Run btop in a terminal." {
		t.Errorf("answer = %q", got)
	}
	lines := readLines(t, cfg, evs[0].Session)
	if last := lines[len(lines)-1]; last.Outcome != "" {
		t.Errorf("outcome = %q, want none", last.Outcome)
	}
}

// TestCancelStillFails checks that a turn the client cancels fails as
// before, with no apology: only the turn's own deadline turns into one.
func TestCancelStillFails(t *testing.T) {
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Thinking: thinkingPieces(5000), ChunkDelay: 5 * time.Millisecond})
	a := ollamaAgent(t, cfg, srv, "direct", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	evs, err := run(ctx, a, rpc.Request{Text: "how do I use btop?"})
	if err == nil {
		t.Fatal("Handle succeeded after the client went away")
	}
	if strings.Contains(answerOf(evs), sorry) {
		t.Error("a cancelled turn sent the apology")
	}
}
