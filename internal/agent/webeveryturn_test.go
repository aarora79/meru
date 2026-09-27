// This file tests the web tools on every route: which turns offer
// web_search and web_fetch and the line that tells the model to use them,
// which skills the prompt lists, the hint a call named after a skill gets,
// and a replay of the real session behind the change. The turns run
// against the fake engine or the fake Ollama, with the real built-in web
// tools searching a fake SearXNG.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// songQuestion is the real session's first question, with an invented
// song and singer and the user's typo kept.
const songQuestion = "what does the song maname maname sung by r. devi acvtually mean"

// TestWebToolsOnEveryRoute checks which turns offer the web tools, and
// that the prompt's web line and the web-research skill come and go with
// them.
func TestWebToolsOnEveryRoute(t *testing.T) {
	yes := func(context.Context, string) (bool, error) { return true, nil }
	no := func(context.Context, string) (bool, error) { return false, nil }
	tests := []struct {
		name    string
		route   string
		scope   string
		webOn   bool // SearXNG is set, so web_search is on
		canCall func(context.Context, string) (bool, error)
		want    []string // the tools the first model call offers
	}{
		{"direct offers the web tools", "direct", rpc.ScopeAuto, true, yes,
			[]string{builtin.DateTime, builtin.WebSearch, builtin.WebFetch}},
		{"search offers them too", "search", rpc.ScopeAuto, true, yes,
			[]string{builtin.DateTime, builtin.WebSearch, builtin.WebFetch}},
		{"web off leaves them out", "direct", rpc.ScopeAuto, false, yes,
			[]string{builtin.DateTime}},
		{"just talk offers nothing", "direct", rpc.ScopeTalk, true, yes, nil},
		{"my files leaves them out", "direct", rpc.ScopeFiles, true, yes,
			[]string{builtin.DateTime}},
		{"the web scope offers them", "direct", rpc.ScopeWeb, true, yes,
			[]string{builtin.DateTime, builtin.WebSearch, builtin.WebFetch}},
		{"a model with no tools gets none", "direct", rpc.ScopeAuto, true, no, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			searx := ""
			if tt.webOn {
				searx = startSearx(t).url
			}
			eng := &fakeEngine{pieces: []string{"It is a love song."}}
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.966, Outcome: "ok"}},
				nil, webDispatcher(cfg, searx, &rowRecorder{}), nil, nil, quietLog())
			a.UseSkills(fixedSkills{reg: builtinRegistry(t)})
			a.UseToolCheck(tt.canCall)

			if _, err := run(context.Background(), a, rpc.Request{Text: songQuestion, Scope: tt.scope}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			last := eng.lastCall()
			var got []string
			for _, s := range last.tools {
				got = append(got, s.Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("tools offered = %v, want %v", got, tt.want)
			}
			offered := slices.Contains(got, builtin.WebSearch)
			system := last.msgs[0].Content
			if has := strings.Contains(system, webNote); has != offered {
				t.Errorf("system prompt holds the web line = %v, want %v", has, offered)
			}
			if has := strings.Contains(system, "- web-research: "); has != offered {
				t.Errorf("skills list names web-research = %v, want %v", has, offered)
			}
			// writing names no tools, so it is listed on every turn.
			if !strings.Contains(system, "- writing: ") {
				t.Error("skills list lacks writing")
			}
		})
	}
}

// TestSkillHint checks the hint a call named after a skill gets back,
// with the skill's tools on offer and without.
func TestSkillHint(t *testing.T) {
	reg := builtinRegistry(t)
	web := []string{builtin.DateTime, builtin.WebSearch, builtin.WebFetch}
	tests := []struct {
		name  string
		call  string
		offer []engine.ToolSpec
		all   []string
		want  string
	}{
		{"offered", "web-research", names(builtin.DateTime, builtin.WebSearch, builtin.WebFetch), web,
			"web-research is a skill, not a tool. Call web_search or web_fetch."},
		{"offered in part", "web-research", names(builtin.WebFetch), web,
			"web-research is a skill, not a tool. Call web_fetch."},
		{"allowed but not offered", "web-research", nil, web,
			"web-research is a skill, not a tool. It works through web_search or web_fetch, " +
				"and this answer doesn't offer them, so answer without them."},
		{"turned off in config", "web-research", nil, []string{builtin.DateTime}, ""},
		{"a skill with no tools", "writing", names(builtin.DateTime), web, ""},
		{"not a skill", "web_lookup", nil, web, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skillHint(reg, tt.call, tt.offer, tt.all); got != tt.want {
				t.Errorf("skillHint = %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestSongSessionReplay replays the real session behind this change, with
// an invented song. Turn 1 routed direct, where the model had no web tool,
// called the web-research skill as a tool, and told the user it couldn't
// search the web. Now the direct route offers web_search, the prompt says
// to use it, and the model's call runs through dispatch. Turn 2's
// follow-up, "you have accerss to web search", searched for the words it
// glued onto the first question; now merud searches for the first
// question alone.
func TestSongSessionReplay(t *testing.T) {
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	// The fast model picks no skill on either turn.
	srv.Enqueue(cfg.Models.Fast, fakeollama.Reply{Text: "none"}, fakeollama.Reply{Text: "none"})
	srv.Enqueue(cfg.Models.Main,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{
			Name: builtin.WebSearch, Arguments: map[string]any{"query": "Maname Maname R. Devi song meaning"},
		}}},
		fakeollama.Reply{Text: "The song asks the moon to carry a message (https://acme.example/flow)."},
		fakeollama.Reply{Text: "Here is what the song means."},
	)
	searx := startSearx(t)
	rec := &rowRecorder{}
	router := &seqRouter{decs: []Decision{
		{Route: "direct", Confidence: 0.966, Outcome: "ok"},
		{Route: "tools", Confidence: 0.5, Outcome: "ok"},
	}}
	eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(cfg, eng, router, nil, webDispatcher(cfg, searx.url, rec), nil, nil, quietLog())
	a.UseSkills(fixedSkills{reg: builtinRegistry(t)})

	// Turn 1.
	evs, err := run(context.Background(), a, rpc.Request{Text: songQuestion})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	session := evs[0].Session
	if r := routeOf(evs); r.Route != "direct" {
		t.Errorf("turn 1: route = %q, want direct", r.Route)
	}
	bodies := chatBodies(t, srv, cfg.Models.Main)
	if len(bodies) != 2 {
		t.Fatalf("turn 1: main-model calls = %d, want 2", len(bodies))
	}
	if got := bodies[0].toolNames(); !slices.Contains(got, builtin.WebSearch) || !slices.Contains(got, builtin.WebFetch) {
		t.Errorf("turn 1: tools offered = %v, want web_search and web_fetch among them", got)
	}
	system := bodies[0].Messages[0].Content
	if !strings.Contains(system, webNote) {
		t.Error("turn 1: the system prompt lacks the web line")
	}
	if !strings.Contains(system, "- web-research: ") || !strings.Contains(system, "To use it, call web_search or web_fetch.") {
		t.Error("turn 1: the skills list doesn't name web-research with its tools")
	}
	// The model searched, not merud: the direct route has no web-first step.
	if got := meruCalls(readLines(t, cfg, session)); len(got) != 0 {
		t.Errorf("turn 1: merud's own calls = %d, want none", len(got))
	}
	rows := rec.all()
	if len(rows) != 1 || rows[0].Tool != builtin.WebSearch || rows[0].Outcome != dispatch.OutcomeOK {
		t.Errorf("turn 1: tool_calls rows = %+v, want one web_search that ran", rows)
	}

	// Turn 2.
	if _, err := run(context.Background(), a, rpc.Request{Text: "you have accerss to web search", Session: session}); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	queries := searx.got()
	want := "song maname maname sung by r. devi acvtually mean"
	if len(queries) != 2 || queries[1] != want {
		t.Errorf("SearXNG queries = %q, want turn 2's to be %q", queries, want)
	}
}
