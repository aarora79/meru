// This file tests what a turn does after a question that stopped before
// it answered (stopped.go).

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// TestStoppedCalls checks that stoppedCalls counts the model's calls in an
// unanswered last question, and nothing else.
func TestStoppedCalls(t *testing.T) {
	user := transcript.Line{Type: transcript.TypeUser, Text: "q"}
	answer := transcript.Line{Type: transcript.TypeAssistant, Text: "a"}
	call := transcript.Line{Type: transcript.TypeToolCall, Tool: "orders"}
	byMeru := transcript.Line{Type: transcript.TypeToolCall, Tool: "web_search", Caller: "meru"}
	result := transcript.Line{Type: transcript.TypeToolResult}
	tests := []struct {
		name  string
		lines []transcript.Line
		want  int
	}{
		{"new chat", nil, 0},
		{"answered", []transcript.Line{user, call, result, answer}, 0},
		{"stopped after two calls", []transcript.Line{user, answer, user, call, result, call}, 2},
		{"stopped before any call", []transcript.Line{user, answer, user}, 0},
		{"merud's own call doesn't count", []transcript.Line{user, byMeru, result, call}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stoppedCalls(tt.lines); got != tt.want {
				t.Errorf("stoppedCalls = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAfterStoppedTurn replays the shape of the real chat: a question
// whose tool calls ran and which stopped before it answered, then the same
// question again, which the model answers with no call. The prompt must
// carry the note, and the answer the notice.
func TestAfterStoppedTurn(t *testing.T) {
	cfg := testConfig(t)
	sess, err := transcript.New(filepath.Join(cfg.Dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []transcript.Line{
		{Type: transcript.TypeUser, Text: "list my holdings"},
		{Type: transcript.TypeAssistant, Text: "You hold three funds."},
		{Type: transcript.TypeUser, Text: "which ones did I buy over a year ago?"},
		{Type: transcript.TypeToolCall, CallID: "c1", Tool: "orders"},
		{Type: transcript.TypeToolResult, CallID: "c1", Outcome: "ok", Result: "bought 2025-10-01"},
		{Type: transcript.TypeToolCall, CallID: "c2", Tool: "orders"},
	} {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	eng := &fakeEngine{pieces: []string{"All three are long-term."}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("orders")}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Session: sess.ID(), Text: "which ones did I buy over a year ago?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if sys := eng.lastCall().msgs[0].Content; !strings.Contains(sys, "stopped before it was answered, after 2 tool calls") {
		t.Errorf("system prompt lacks the stopped note:\n%s", sys)
	}
	if n := noticeOf(evs); !strings.Contains(n, stoppedNotice) {
		t.Errorf("notice = %q, want the stopped notice", n)
	}

	// The next turn follows an answered one, so neither shows.
	evs, err = run(context.Background(), a, rpc.Request{Session: sess.ID(), Text: "and the others?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if sys := eng.lastCall().msgs[0].Content; strings.Contains(sys, "stopped before it was answered") {
		t.Error("the note stayed after an answered turn")
	}
	if n := noticeOf(evs); strings.Contains(n, stoppedNotice) {
		t.Errorf("notice = %q after an answered turn", n)
	}
}
