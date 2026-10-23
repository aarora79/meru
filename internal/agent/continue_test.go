// This file tests Continue: the hand-off continuation builds from a
// session's last turn, and a whole turn that picks up a cut-off answer.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestContinuation(t *testing.T) {
	call := func(id, tool, args string) transcript.Line {
		return transcript.Line{Type: transcript.TypeToolCall, CallID: id, Kind: "builtin", Server: "meru", Tool: tool, Args: json.RawMessage(args)}
	}
	result := func(id, outcome, text string) transcript.Line {
		return transcript.Line{Type: transcript.TypeToolResult, CallID: id, Outcome: outcome, Result: text}
	}
	question := transcript.Line{Type: transcript.TypeUser, Text: "compare the two release notes"}
	answer := func(outcome string) transcript.Line {
		return transcript.Line{Type: transcript.TypeAssistant, Text: "The first one", Outcome: outcome}
	}
	long := strings.Repeat("release ", 200)

	tests := []struct {
		name    string
		lines   []transcript.Line
		wantErr bool
		has     []string
		hasNot  []string
	}{
		{name: "cut off with calls",
			lines: []transcript.Line{question, call("c1", "web_fetch", `{"url":"https://go.dev/doc/go1.26"}`), result("c1", "ok", long),
				call("c2", "web_fetch", `{"url":"https://go.dev/doc/go1.25"}`), result("c2", "error", "timed out"), answer(endCutOff)},
			has: []string{`"compare the two release notes"`, `web_fetch {"url":"https://go.dev/doc/go1.26"}`, "release release", "…",
				`web_fetch {"url":"https://go.dev/doc/go1.25"} → error`, "Pick up where the answer stopped"},
			hasNot: []string{long, "timed out"}},
		{name: "timeout counts", lines: []transcript.Line{question, answer(endTimeout)}, has: []string{"It made no tool calls."}},
		{name: "a whole answer", lines: []transcript.Line{question, answer("")}, wantErr: true},
		{name: "gave up", lines: []transcript.Line{question, answer(endGaveUp)}, wantErr: true},
		{name: "no answer yet", lines: []transcript.Line{question}, wantErr: true},
		{name: "no turn", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, msg, err := continuation(tt.lines)
			if tt.wantErr {
				if !errors.Is(err, errNothingToContinue) {
					t.Fatalf("err = %v, want errNothingToContinue", err)
				}
				return
			}
			if err != nil || q != question.Text {
				t.Fatalf("question %q, err %v", q, err)
			}
			for _, s := range tt.has {
				if !strings.Contains(msg, s) {
					t.Errorf("message lacks %q:\n%s", s, msg)
				}
			}
			for _, s := range tt.hasNot {
				if strings.Contains(msg, s) {
					t.Errorf("message holds %.40q", s)
				}
			}
		})
	}
}

func TestContinuationKeepsNewestCalls(t *testing.T) {
	lines := []transcript.Line{{Type: transcript.TypeUser, Text: "q"}}
	for i := range 30 {
		id := fmt.Sprintf("c%d", i)
		lines = append(lines,
			transcript.Line{Type: transcript.TypeToolCall, CallID: id, Kind: "builtin", Server: "meru", Tool: "web_fetch", Args: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))},
			transcript.Line{Type: transcript.TypeToolResult, CallID: id, Outcome: "ok", Result: strings.Repeat("x", 1000)})
	}
	lines = append(lines, transcript.Line{Type: transcript.TypeAssistant, Outcome: endCutOff})
	_, msg, err := continuation(lines)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, `{"n":29}`) || strings.Contains(msg, `{"n":0}`) || !strings.Contains(msg, "earlier calls left out") {
		t.Errorf("want the newest calls kept and the oldest left out:\n%.300s", msg)
	}
	if n := len([]rune(msg)); n > handoffChars+1000 {
		t.Errorf("hand-off is %d characters, past the cap", n)
	}
}

func TestContinueTurn(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("fetch", `{"url":"https://go.dev/doc"}`)}},
		{pieces: []string{"Go 1.26 adds"}, done: "length"},
		{pieces: []string{" a new garbage collector."}},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("fetch")},
		results: map[string]fakeResult{"fetch": {text: "Go 1.26 release notes: the Green Tea collector is on by default."}}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "what's new in Go 1.26?"})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	done := evs[len(evs)-1]
	if done.Type != rpc.EventDone || done.Outcome != endCutOff {
		t.Fatalf("first turn's done = %+v, want outcome cut_off", done)
	}
	session := evs[0].Session

	evs, err = run(context.Background(), a, rpc.Request{Session: session, Continue: true})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if done := evs[len(evs)-1]; done.Outcome != "" {
		t.Errorf("continue's done outcome = %q, want a whole answer", done.Outcome)
	}
	if n := len(tools.recorded()); n != 1 {
		t.Errorf("dispatch ran %d calls, want 1: the continue turn reads the first one's result", n)
	}
	msgs := eng.lastCall().msgs
	last := msgs[len(msgs)-1].Content
	for _, want := range []string{"what's new in Go 1.26?", `fetch {"url":"https://go.dev/doc"}`, "Green Tea collector"} {
		if !strings.Contains(last, want) {
			t.Errorf("continue prompt lacks %q:\n%s", want, last)
		}
	}
	lines := readLines(t, cfg, session)
	var users []string
	for _, l := range lines {
		if l.Type == transcript.TypeUser {
			users = append(users, l.Text)
		}
	}
	if len(users) != 2 || users[1] != continueWords {
		t.Errorf("user lines = %q, want the question then %q", users, continueWords)
	}

	// The answer is whole now, so a second Continue has nothing to do.
	if _, err := run(context.Background(), a, rpc.Request{Session: session, Continue: true}); !errors.Is(err, errNothingToContinue) {
		t.Errorf("second continue: err %v, want errNothingToContinue", err)
	}
	if _, err := run(context.Background(), a, rpc.Request{Continue: true}); err == nil {
		t.Error("continue with no session ran")
	}
}
