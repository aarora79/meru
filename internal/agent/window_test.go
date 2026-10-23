// This file tests fitWindow, which keeps a turn's prompt inside the
// context window by shortening its oldest tool results.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// windowRun runs one turn that fetches two long pages, then answers, with
// [ollama] context_length set to window. It returns the fake engine, to
// read what each round sent.
func windowRun(t *testing.T, window int) *fakeEngine {
	t.Helper()
	cfg := testConfig(t)
	cfg.Ollama.ContextLength = window
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("fetch", `{"n":1}`)}},
		{pieces: []string{"Read the second page."}, calls: []engine.ToolCall{call("fetch", `{"n":2}`)}},
		{pieces: []string{"Done."}},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("fetch")},
		results: map[string]fakeResult{"fetch": {text: strings.Repeat("page text ", 600)}}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())
	// The fake writes the same 6,000-character page for each call; the
	// second has new arguments, so it isn't a repeat.
	if _, err := run(context.Background(), a, rpc.Request{Text: "read both pages"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return eng
}

// windowFor returns the context_length whose 80% holds extra characters
// past the first round's prompt in eng, so a test can place the limit
// between rounds.
func windowFor(eng *fakeEngine, extra int) int {
	first := eng.calls[0]
	tokens := (charsOf(first.msgs) + schemaChars(first.tools) + extra) / charsPerToken
	return tokens * 100 / windowShare
}

func TestFitWindowShortensOldResults(t *testing.T) {
	// A first run with no window measures the prompt the turn starts with.
	window := windowFor(windowRun(t, 0), 9000)

	eng := windowRun(t, window)
	if n := len(eng.calls); n != 3 {
		t.Fatalf("model called %d times, want 3", n)
	}
	last := eng.calls[2]
	var tool []engine.Message
	for _, m := range last.msgs {
		if m.Role == engine.RoleTool {
			tool = append(tool, m)
		}
	}
	if len(tool) != 2 {
		t.Fatalf("third round read %d tool results, want 2", len(tool))
	}
	if !strings.HasSuffix(tool[0].Content, shortenedNote) || len([]rune(tool[0].Content)) != keepChars+len(shortenedNote) {
		t.Errorf("first result = %d chars, want it shortened to %d", len(tool[0].Content), keepChars+len(shortenedNote))
	}
	if strings.Contains(tool[1].Content, "[Meru shortened") {
		t.Error("the newest result was shortened; the model hasn't read it yet")
	}
	if len(last.tools) == 0 {
		t.Error("the third round offered no tools, though the prompt fit once shortened")
	}
	// The second round read the first page whole: it fit then.
	for _, m := range eng.calls[1].msgs {
		if m.Role == engine.RoleTool && strings.Contains(m.Content, "[Meru shortened") {
			t.Error("the second round read a shortened result")
		}
	}
}

func TestFitWindowStopsTools(t *testing.T) {
	// The limit falls inside the first page, and nothing older can go, so
	// the second round must answer with no tools.
	window := windowFor(windowRun(t, 0), 3000)
	eng := windowRun(t, window)
	if n := len(eng.calls); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	if len(eng.calls[1].tools) != 0 {
		t.Error("the second round offered tools with the prompt past the limit")
	}
}

func TestFitWindowOff(t *testing.T) {
	// context_length = 0 leaves Ollama's own size, which Meru doesn't know,
	// so nothing is shortened.
	eng := windowRun(t, 0)
	for _, m := range eng.calls[2].msgs {
		if strings.Contains(m.Content, "[Meru shortened") {
			t.Fatal("a result was shortened with no window set")
		}
	}
}

func TestFitWindowForgetsShortenedCalls(t *testing.T) {
	a := &Agent{window: 1000, log: quietLog()}
	long := strings.Repeat("x", 3000)
	tr := &turn{seen: map[string]string{"fetch\x00{}": long}}
	msgs := []engine.Message{
		{Role: engine.RoleUser, Content: "q"},
		{Role: engine.RoleAssistant, ToolCalls: []engine.ToolCall{call("fetch", `{}`)}},
		{Role: engine.RoleTool, Content: long},
		{Role: engine.RoleAssistant, ToolCalls: []engine.ToolCall{call("fetch", `{"n":2}`)}},
		{Role: engine.RoleTool, Content: "short"},
	}
	full := a.fitWindow(context.Background(), tr, msgs, 1, 3, 0, nil)
	if full {
		t.Error("fitWindow says full; the shortened prompt fits")
	}
	if tr.shortened != 1 || len(tr.seen) != 0 {
		t.Errorf("shortened %d, seen %v; want 1 and the call forgotten", tr.shortened, tr.seen)
	}
}

// TestFitWindowTrustsOllamasCount checks that a prompt Ollama counted
// under the limit keeps its tools, however many characters it holds. A
// real turn lost its tools after one web_search, when the estimate from
// characters ran past 80% of the window while Ollama counted 18,000
// tokens.
func TestFitWindowTrustsOllamasCount(t *testing.T) {
	a := &Agent{window: 32768, log: quietLog()}
	schemas := strings.Repeat(`{"type":"object"}`, 5000) // 85,000 characters
	msgs := []engine.Message{
		{Role: engine.RoleSystem, Content: schemas},
		{Role: engine.RoleUser, Content: "latest news on cnn"},
		{Role: engine.RoleAssistant, ToolCalls: []engine.ToolCall{call("web_search", `{"query":"cnn"}`)}},
		{Role: engine.RoleTool, Content: strings.Repeat("result ", 300)},
	}
	if a.fitWindow(context.Background(), &turn{}, msgs, 2, 2, 18000, nil) {
		t.Error("tools went off with Ollama's count at 18,000 of a 32,768-token window")
	}
	if !a.fitWindow(context.Background(), &turn{}, msgs, 2, 2, 27000, nil) {
		t.Error("tools stayed on with Ollama's count past 80% of the window")
	}
}
