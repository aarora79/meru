// This file tests the check on an answer that denies having web tools, and
// the nudge that follows it.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// TestDeniesWebTools holds the real denials first, then near misses.
func TestDeniesWebTools(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"I don't have a search tool I can reach from here.", true},
		{"I don't have a tool that can fetch the live site.", true},
		{"No, I don't have an `abrowser` tool.", true},
		{"I can't browse sites or pull live updates.", true},
		{"I have no web access.", true},
		{"I don't have access to the internet.", true},
		{"I'm unable to search the web.", true},
		{"I don't have search results on that.", false},
		{"I don't have your birthday.", false},
		{"I can't search your mail.", false},
		{"I can search the web for that.", false},
		{"The web page says it can't browse offline.", false},
	}
	for _, tt := range tests {
		if got := deniesWebTools(tt.text); got != tt.want {
			t.Errorf("deniesWebTools(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

// TestWebDenialNudge: a denial with web tools on offer gets one nudge, the
// model then fetches and answers, and the transcript holds no nudge.
func TestWebDenialNudge(t *testing.T) {
	cfg := ollamaConfig(t)
	cfg.Agent.MaxRounds = 5
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main,
		fakeollama.Reply{Text: "I don't have a tool that can fetch the live site."},
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: builtin.WebFetch, Arguments: map[string]any{"url": "https://example.com/top"}}}},
		fakeollama.Reply{Text: "The top story is about trains."})
	tools := &fakeTools{
		specs:   []engine.ToolSpec{spec(builtin.WebSearch), spec(builtin.WebFetch)},
		results: map[string]fakeResult{builtin.WebFetch: {text: "Trains run late."}},
	}
	a := ollamaAgent(t, cfg, srv, "tools", tools)

	evs, err := run(context.Background(), a, rpc.Request{Text: "what are the top stories today?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	bodies := chatBodies(t, srv, cfg.Models.Main)
	if len(bodies) != 3 {
		t.Fatalf("%d main-model calls, want 3", len(bodies))
	}
	nudged := bodies[1]
	if last := nudged.Messages[len(nudged.Messages)-1]; last.Content != webDenialNudge || len(nudged.Tools) == 0 {
		t.Errorf("the second call ends %q with %d tools; want the nudge with the tools", last.Content, len(nudged.Tools))
	}
	for _, l := range readLines(t, cfg, evs[0].Session) {
		if strings.Contains(l.Text, webDenialNudge) {
			t.Errorf("the transcript holds the nudge: %+v", l)
		}
	}
}

// TestWebDenialNudgeLimits covers each case that gets no nudge.
func TestWebDenialNudgeLimits(t *testing.T) {
	web := []engine.ToolSpec{spec(builtin.WebSearch), spec(builtin.WebFetch)}
	deny := reply{text: "I can't browse sites."}
	tests := []struct {
		name  string
		turn  *turn
		rep   reply
		offer []engine.ToolSpec
		want  bool
	}{
		{"a denial with web tools", &turn{rounds: 1}, deny, web, true},
		{"no web tool on offer", &turn{rounds: 1}, deny, []engine.ToolSpec{spec("grep")}, false},
		{"web_fetch alone, as when SearXNG is down", &turn{rounds: 1}, reply{text: "I can't search the web right now."}, []engine.ToolSpec{spec(builtin.WebFetch)}, false},
		{"web_search alone", &turn{rounds: 1}, deny, []engine.ToolSpec{spec(builtin.WebSearch)}, false},
		{"already nudged", &turn{rounds: 1, webDenied: true}, deny, web, false},
		{"a web call failed", &turn{rounds: 1, webFailed: true}, deny, web, false},
		{"no round left", &turn{rounds: 5}, deny, web, false},
		{"no denial", &turn{rounds: 1}, reply{text: "Here's the news."}, web, false},
	}
	a := &Agent{maxRounds: 5}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.nudgesWebDenial(tt.turn, tt.rep, tt.offer); got != tt.want {
				t.Errorf("nudgesWebDenial = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSavesToMemory replays the real miss, then near misses.
func TestSavesToMemory(t *testing.T) {
	tests := []struct {
		question string
		want     bool
	}{
		{"save in your memory that I prefer tea", true},
		{"add this to your memory: I prefer tea", true},
		{"store that to memory", true},
		{"memorize my dentist's name", true},
		{"what is in your memory", false},
		{"how much memory does this laptop have", false},
		{"save this file", false},
	}
	for _, tt := range tests {
		if got := savesToMemory(tt.question); got != tt.want {
			t.Errorf("savesToMemory(%q) = %v, want %v", tt.question, got, tt.want)
		}
	}
}
