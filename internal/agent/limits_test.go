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
			wantAnswer:  fmt.Sprintf(capAnswer, 3, "weather.now ×2"),
			wantOutcome: endCapped,
		},
		{
			// The last round offers no tools; the model writes a plan with a
			// call that can't run, as in the session that led to #201.
			name:      "a plan on the last round",
			maxRounds: 3,
			replies: append(callsEveryRound[:2:2], fakeollama.Reply{Text: "Let me find the specific date with 20 stars.",
				ToolCalls: []fakeollama.ToolCall{{Name: "weather.now", Arguments: map[string]any{"city": "x"}}}}),
			wantAnswer:  "Let me find the specific date with 20 stars.\n\n" + fmt.Sprintf(capNote, 3, "weather.now ×2"),
			wantOutcome: endCapped,
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

// TestPlanNudge checks that a round which only announces its next step,
// with tools on offer and rounds left, gets one nudge to take it: the next
// call carries planNudge and the tools, the user reads both rounds' words,
// and the transcript holds no nudge. A second plan isn't nudged again.
func TestPlanNudge(t *testing.T) {
	cfg := ollamaConfig(t)
	cfg.Agent.MaxRounds = 5
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main,
		fakeollama.Reply{Text: "Let me check the weather."},
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "weather.now", Arguments: map[string]any{"city": "Oslo"}}}},
		fakeollama.Reply{Text: "I'll look again."},
		fakeollama.Reply{Text: "never asked for"})
	tools := &fakeTools{specs: []engine.ToolSpec{spec("weather.now")}, results: map[string]fakeResult{"weather.now": {text: "sunny"}}}
	a := ollamaAgent(t, cfg, srv, "tools", tools)

	evs, err := run(context.Background(), a, rpc.Request{Text: "is it sunny in Oslo?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// The second plan ends the turn: one nudge a turn.
	if got, want := answerOf(evs), "Let me check the weather.\n\nI'll look again."; got != want {
		t.Errorf("answer = %q, want %q", got, want)
	}
	bodies := chatBodies(t, srv, cfg.Models.Main)
	if len(bodies) != 3 {
		t.Fatalf("%d main-model calls, want 3", len(bodies))
	}
	nudged := bodies[1]
	if last := nudged.Messages[len(nudged.Messages)-1]; last.Content != planNudge || len(nudged.Tools) == 0 {
		t.Errorf("the second call ends %q with %d tools; want planNudge with the tools", last.Content, len(nudged.Tools))
	}
	for _, l := range readLines(t, cfg, evs[0].Session) {
		if strings.Contains(l.Text, planNudge) {
			t.Errorf("the transcript holds the nudge: %+v", l)
		}
	}
}

func TestPlansOnly(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"Let me find the specific date with 20 stars.", true},
		{"I'll read the top four articles.", true},
		{"Now I'll check the 2024 sheet.", true},
		{"let me look that up", true},
		{"Let me know if you want the 2024 sheet too.", false},
		{"I'll need the file's path to do that.", false},
		{"The total is 555.13. Let me know if you need more.", false},
		{"Let me explain:\n- first\n- second", false},
		{"Let me show the table: | a | b |", false},
		{"Let me explain. " + strings.Repeat("The answer goes on. ", 20), false},
		{"", false},
	}
	for _, tt := range tests {
		if got := plansOnly(tt.text); got != tt.want {
			t.Errorf("plansOnly(%.50q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestTried(t *testing.T) {
	calls := []triedCall{
		{"web_search", "ok"}, {"web_fetch", "error"}, {"cmd.system-load", "ok"},
		{"cmd.find-process", "ok"}, {"cmd.find-process", "ok"}, {"grep", "ok"}, {"grep", "declined"},
	}
	want := "web_search, web_fetch (failed), cmd.system-load, cmd.find-process ×2, grep ×2 (you said no)"
	if got := tried(calls); got != want {
		t.Errorf("tried = %q\nwant   %q", got, want)
	}
	if got := tried(nil); got != "no tools" {
		t.Errorf("tried(nil) = %q", got)
	}
	var many []triedCall
	for i := range maxTried + 3 {
		many = append(many, triedCall{name: fmt.Sprintf("t%d", i), outcome: "ok"})
	}
	if got := tried(many); !strings.HasSuffix(got, ", and 3 more") {
		t.Errorf("tried of %d tools = %q, want the rest counted", len(many), got)
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
