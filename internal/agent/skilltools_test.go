// This file replays a real turn that went wrong, against the fake Ollama:
// "help me understand btop with some simple commands" routed direct, the
// skill pick chose web-research, and the model, offered only datetime,
// called datetime eight times. It tests the two fixes: a picked skill
// brings the tools it names, and a repeated call runs once.

package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// btopQuestion is the question from the real turn.
const btopQuestion = "help me understand btop with some simple commands"

// chatBody is the part of a /api/chat request these tests read: each
// message's role and text, and the names of the tools on offer.
type chatBody struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Options map[string]any `json:"options"`
}

// chatBodies decodes every /api/chat request to model the fake received,
// in order. The skill pick goes to /api/chat too, with the fast model.
func chatBodies(t *testing.T, srv *fakeollama.Server, model string) []chatBody {
	t.Helper()
	var out []chatBody
	for _, r := range srv.Requests("/api/chat") {
		if r.Model != model {
			continue
		}
		var b chatBody
		if err := json.Unmarshal(r.Body, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// toolNames returns the names of the tools a request offered.
func (b chatBody) toolNames() []string {
	var names []string
	for _, tl := range b.Tools {
		names = append(names, tl.Function.Name)
	}
	return names
}

// ollamaConfig returns a test config with the fast and main models named
// apart, so the fake Ollama keeps a queue of replies for each.
func ollamaConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Models.Fast, cfg.Models.Main = "fast-model", "main-model"
	return cfg
}

// ollamaAgent builds an agent that talks to srv through the real
// OllamaEngine, routes every question to route, and offers tools.
func ollamaAgent(t *testing.T, cfg config.Config, srv *fakeollama.Server, route string, tools ToolRunner) *Agent {
	t.Helper()
	eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, eng, &fakeRouter{dec: Decision{Route: route, Confidence: 0.761, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())
}

// routeOf returns the "route" event among evs.
func routeOf(evs []rpc.Event) rpc.Event {
	for _, ev := range evs {
		if ev.Type == rpc.EventRoute {
			return ev
		}
	}
	return rpc.Event{}
}

// answerOf joins the text of every "token" event in evs.
func answerOf(evs []rpc.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Type == rpc.EventToken {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

// TestSkillBringsTools replays the btop turn with and without the web
// tools on, and checks that a picked skill widens the route only with the
// tools config allows.
func TestSkillBringsTools(t *testing.T) {
	webOn := []engine.ToolSpec{spec("datetime"), spec("web_search"), spec("web_fetch"), spec("read_file")}
	tests := []struct {
		name       string
		route      string
		pick       string
		tools      []engine.ToolSpec
		wantRoute  string
		wantOffer  []string // tools the first main-model call offers
		wantSkills []string // skills on the route event, whose bodies load
		dropped    string   // a skill whose body must stay out; "" for none
		wantNote   bool     // toolsNote in the system prompt
		wantWeb    bool     // webFallbackNote in the system prompt
	}{
		{
			// Every route offers the web tools while web_search is on, so
			// web-research needs no wider route.
			name: "direct has the web tools already", route: "direct", pick: "web-research, writing", tools: webOn,
			wantRoute: "direct", wantOffer: []string{"datetime", "web_search", "web_fetch"},
			wantSkills: []string{"web-research", "writing"}, wantNote: true,
		},
		{
			name: "search has the web tools already", route: "search", pick: "web-research", tools: webOn,
			wantRoute: "search", wantOffer: []string{"datetime", "web_search", "web_fetch", "read_file"},
			wantSkills: []string{"web-research"}, wantNote: true, wantWeb: true,
		},
		{
			name: "web tools off drops the skill", route: "direct", pick: "web-research, writing",
			tools:     []engine.ToolSpec{spec("datetime"), spec("read_file")},
			wantRoute: "direct", wantOffer: []string{"datetime"},
			wantSkills: []string{"writing"}, dropped: "web-research", wantNote: true,
		},
		{
			name: "web_fetch alone is enough", route: "direct", pick: "web-research",
			tools:     []engine.ToolSpec{spec("datetime"), spec("web_fetch")},
			wantRoute: "tools", wantOffer: []string{"datetime", "web_fetch"},
			wantSkills: []string{"web-research"}, wantNote: true,
		},
		{
			name: "a skill with no tools leaves the route", route: "direct", pick: "writing", tools: webOn,
			wantRoute: "direct", wantOffer: []string{"datetime", "web_search", "web_fetch"},
			wantSkills: []string{"writing"}, wantNote: true,
		},
		{
			name: "the file tools on search are there already", route: "search", pick: "file-research",
			tools:     []engine.ToolSpec{spec("datetime"), spec("read_file"), spec("list_folder"), spec("grep"), spec("search_files"), spec("web_search")},
			wantRoute: "search", wantOffer: []string{"datetime", "read_file", "list_folder", "grep", "search_files", "web_search"},
			wantSkills: []string{"file-research"}, wantNote: true, wantWeb: true,
		},
		{
			// The pick that sent the real btop turn into the user's
			// folders. The widened route offers every tool, web_search
			// among them, and the prompt says to use it when the files
			// don't answer.
			name: "file-research on direct gains the web tools too", route: "direct", pick: "file-research",
			tools:     []engine.ToolSpec{spec("datetime"), spec("web_search"), spec("grep"), spec("read_file")},
			wantRoute: "tools", wantOffer: []string{"datetime", "web_search", "grep", "read_file"},
			wantSkills: []string{"file-research"}, wantNote: true, wantWeb: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Fast, fakeollama.Reply{Text: tt.pick})
			srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Text: "btop shows what your computer is doing."})
			a := ollamaAgent(t, cfg, srv, tt.route, &fakeTools{specs: tt.tools})
			a.UseSkills(fixedSkills{reg: builtinRegistry(t)})

			evs, err := run(context.Background(), a, rpc.Request{Text: btopQuestion})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			route := routeOf(evs)
			if route.Route != tt.wantRoute {
				t.Errorf("route = %q, want %q", route.Route, tt.wantRoute)
			}
			var skills []string
			for _, s := range route.Skills {
				skills = append(skills, s.Name)
			}
			if !slices.Equal(skills, tt.wantSkills) {
				t.Errorf("route event skills = %v, want %v", skills, tt.wantSkills)
			}

			bodies := chatBodies(t, srv, cfg.Models.Main)
			if len(bodies) != 1 {
				t.Fatalf("main-model calls = %d, want 1", len(bodies))
			}
			if got := bodies[0].toolNames(); !slices.Equal(got, tt.wantOffer) {
				t.Errorf("tools offered = %v, want %v", got, tt.wantOffer)
			}
			system := bodies[0].Messages[0].Content
			for _, s := range tt.wantSkills {
				if !strings.Contains(system, "Skill: "+s+"\n") {
					t.Errorf("system prompt lacks the %s instructions", s)
				}
			}
			if tt.dropped != "" && strings.Contains(system, "Skill: "+tt.dropped+"\n") {
				t.Errorf("system prompt holds the %s instructions, though none of its tools is on", tt.dropped)
			}
			if has := strings.Contains(system, toolsNote); has != tt.wantNote {
				t.Errorf("system prompt holds the tools note = %v, want %v", has, tt.wantNote)
			}
			if has := strings.Contains(system, webFallbackNote); has != tt.wantWeb {
				t.Errorf("system prompt holds the web fallback note = %v, want %v", has, tt.wantWeb)
			}
			// A web skill doesn't make a direct question about the user's
			// files. A search route was about them already.
			if tt.route == "direct" && tt.pick != "file-research" && strings.Contains(system, fileToolsNote) {
				t.Error("a web turn got the note on the file tools")
			}
		})
	}
}

// TestRepeatedCallRunsOnce replays the rest of the btop turn: a model
// that calls datetime with no arguments round after round. The first call
// runs; the next hands back the same result with repeatNote; the one after
// carries lastRepeatNote; and the round after that offers no tools, so the
// model answers.
func TestRepeatedCallRunsOnce(t *testing.T) {
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	datetime := fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "datetime"}}}
	srv.Enqueue(cfg.Models.Main, datetime, datetime, datetime,
		fakeollama.Reply{Text: "btop is a terminal monitor. Run btop, then press q to quit."})
	tools := &fakeTools{
		specs:   []engine.ToolSpec{spec("datetime")},
		results: map[string]fakeResult{"datetime": {text: "Thursday, 24 September 2026, 10:00"}},
	}
	a := ollamaAgent(t, cfg, srv, "direct", tools)

	evs, err := run(context.Background(), a, rpc.Request{Text: btopQuestion})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(tools.recorded()); n != 1 {
		t.Errorf("dispatch ran %d calls, want 1", n)
	}
	calls := 0
	for _, ev := range evs {
		if ev.Type == rpc.EventToolCall {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("tool_call events = %d, want 1: a repeat is not a call", calls)
	}

	bodies := chatBodies(t, srv, cfg.Models.Main)
	if len(bodies) != 4 {
		t.Fatalf("main-model calls = %d, want 4", len(bodies))
	}
	// last returns the last message of request i.
	last := func(i int) string {
		m := bodies[i].Messages
		return m[len(m)-1].Content
	}
	if got := last(1); got != "Thursday, 24 September 2026, 10:00" {
		t.Errorf("round 2 reads %q, want the real result", got)
	}
	if got := last(2); got != "Thursday, 24 September 2026, 10:00\n\n"+repeatNote {
		t.Errorf("round 3 reads %q, want the result and repeatNote", got)
	}
	if got := last(3); got != "Thursday, 24 September 2026, 10:00\n\n"+lastRepeatNote {
		t.Errorf("round 4 reads %q, want the result and lastRepeatNote", got)
	}
	for i, b := range bodies {
		if offered := len(b.Tools) > 0; offered != (i < 3) {
			t.Errorf("round %d offered tools = %v, want %v", i+1, offered, i < 3)
		}
	}
	if got := answerOf(evs); got != "btop is a terminal monitor. Run btop, then press q to quit." {
		t.Errorf("answer = %q", got)
	}
}

// TestCallKey checks which calls count as the same call.
func TestCallKey(t *testing.T) {
	tests := []struct {
		a, b engine.ToolCall
		same bool
	}{
		{call("datetime", `{}`), call("datetime", ``), true},
		{call("datetime", `{}`), call("datetime", `null`), false},
		{call("web_search", `{"query":"btop","n":2}`), call("web_search", `{ "n": 2, "query": "btop" }`), true},
		{call("web_search", `{"query":"btop"}`), call("web_search", `{"query":"htop"}`), false},
		{call("web_search", `{"query":"btop"}`), call("web_fetch", `{"query":"btop"}`), false},
		{call("x", `not json`), call("x", `not json`), true},
	}
	for _, tt := range tests {
		if got := callKey(tt.a) == callKey(tt.b); got != tt.same {
			t.Errorf("callKey(%s %s) == callKey(%s %s) is %v, want %v",
				tt.a.Name, tt.a.Arguments, tt.b.Name, tt.b.Arguments, got, tt.same)
		}
	}
}

// TestRepeatInOneRound checks that the same call twice in one round runs
// once, and the copy gets the result with repeatNote.
func TestRepeatInOneRound(t *testing.T) {
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("notes.search", `{"q":"garden"}`), call("notes.search", `{"q": "garden"}`)}},
		{pieces: []string{"Done."}},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("notes.search")}, results: map[string]fakeResult{"notes.search": {text: "sow in April"}}}
	if _, err := run(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "when do I sow?"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(tools.recorded()); n != 1 {
		t.Errorf("dispatch ran %d calls, want 1", n)
	}
	msgs := eng.lastCall().msgs
	got := []string{msgs[len(msgs)-2].Content, msgs[len(msgs)-1].Content}
	want := []string{"sow in April", "sow in April\n\n" + repeatNote}
	if !slices.Equal(got, want) {
		t.Errorf("tool messages = %q, want %q", got, want)
	}
}
