// This file tests the web-first step in whole turns: a question that asks
// for the web or gives a URL, a question that names a thing the user's
// files don't cover, the scopes, a SearXNG that is off or down, the web
// notes a later turn reads, a call named after a skill, and a replay of
// the real session that drove all four. The turns run against the fake
// Ollama or the fake engine, with the real built-in web tools searching a
// fake SearXNG, or with the fake ToolRunner where a test needs a page from
// a public address.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// acmeJSON is SearXNG's answer for a search about the made-up Acme Flow.
const acmeJSON = `{"results":[
{"title":"Acme Flow","url":"https://acme.example/flow","content":"Acme Flow moves notes between apps."},
{"title":"Acme Flow pricing","url":"https://acme.example/flow/pricing","content":"Plans for small and large teams."}]}`

// fakeSearx is a fake SearXNG on loopback. It answers every /search with
// acmeJSON and records each query.
type fakeSearx struct {
	url string

	mu      sync.Mutex // guards queries
	queries []string
}

// startSearx starts a fake SearXNG for the test.
func startSearx(t *testing.T) *fakeSearx {
	t.Helper()
	f := &fakeSearx{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.Query().Get("q"))
		f.mu.Unlock()
		fmt.Fprint(w, acmeJSON)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// got returns the queries so far.
func (f *fakeSearx) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// rowRecorder keeps the tool_calls rows dispatch writes, in place of the
// store.
type rowRecorder struct {
	mu   sync.Mutex // guards rows
	rows []store.ToolCall
}

func (r *rowRecorder) InsertToolCall(_ context.Context, row store.ToolCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, row)
	return nil
}

// all returns the rows so far.
func (r *rowRecorder) all() []store.ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.rows)
}

// webDispatcher returns the real dispatcher over the real built-in tools,
// with web_search searching searxURL; "" leaves web_search off, as a config
// with no SearXNG address does. The tools read no folders.
func webDispatcher(cfg config.Config, searxURL string, rec *rowRecorder) *dispatch.Dispatcher {
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()},
		config.Web{SearXNGURL: searxURL, MaxResults: 8}, nil, "", nil, nil, nil)
	return dispatch.New([]dispatch.Backend{tools}, rec, dispatch.Options{})
}

// webAgent builds an agent that talks to the fake Ollama srv, routes every
// question to route, searches files with search (nil for none) and runs
// tools through tools.
func webAgent(t *testing.T, cfg config.Config, srv *fakeollama.Server, route string, search Searcher, tools ToolRunner) *Agent {
	t.Helper()
	eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, eng, &fakeRouter{dec: Decision{Route: route, Confidence: 0.55, Outcome: "ok"}}, search, tools, nil, nil, quietLog())
}

// meruCalls returns the lines in lines that record a call merud made itself.
func meruCalls(lines []transcript.Line) []transcript.Line {
	var out []transcript.Line
	for _, l := range lines {
		if l.Type == transcript.TypeToolCall && l.Caller == dispatch.CallerMeru {
			out = append(out, l)
		}
	}
	return out
}

// assistantLine returns the last assistant line in lines.
func assistantLine(t *testing.T, lines []transcript.Line) transcript.Line {
	t.Helper()
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Type == transcript.TypeAssistant {
			return lines[i]
		}
	}
	t.Fatal("no assistant line")
	return transcript.Line{}
}

// TestAskedWebFirst checks rule 1 with the real web tools: a question that
// asks for the web runs web_search through dispatch before the model's
// first round, as merud's own call, and the results sit in the prompt.
func TestAskedWebFirst(t *testing.T) {
	tests := []struct {
		name      string
		question  string
		route     string
		wantQuery string
		wantRoute string
	}{
		{"search the web on direct", "can you search the web for Acme Flow pricing?", "direct", "Acme Flow pricing", "tools"},
		{"look it up", "look it up online: what is Acme Flow", "direct", "Acme Flow", "tools"},
		{"web search on search", "do a web search about Acme Flow plans", "search", "Acme Flow plans", "search+tools"},
		{"tools stays tools", "google it: Acme Flow team plans", "tools", "Acme Flow team plans", "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Text: "Acme Flow moves notes between apps (https://acme.example/flow)."})
			searx := startSearx(t)
			rec := &rowRecorder{}
			a := webAgent(t, cfg, srv, tt.route, nil, webDispatcher(cfg, searx.url, rec))

			evs, err := run(context.Background(), a, rpc.Request{Text: tt.question})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := searx.got(); !slices.Equal(got, []string{tt.wantQuery}) {
				t.Errorf("SearXNG queries = %q, want %q", got, []string{tt.wantQuery})
			}
			if r := routeOf(evs).Route; r != tt.wantRoute {
				t.Errorf("route = %q, want %q", r, tt.wantRoute)
			}
			// The call's events come before the answer's first token.
			firstCall, firstToken := -1, -1
			for i, ev := range evs {
				if ev.Type == rpc.EventToolCall && firstCall < 0 {
					firstCall = i
				}
				if ev.Type == rpc.EventToken && firstToken < 0 {
					firstToken = i
				}
			}
			if firstCall < 0 || firstCall > firstToken {
				t.Errorf("tool_call event at %d, first token at %d; want the call first", firstCall, firstToken)
			}

			// The audit log and the transcript show a call merud made.
			rows := rec.all()
			if len(rows) != 1 || rows[0].Tool != builtin.WebSearch || rows[0].Caller != dispatch.CallerMeru || rows[0].Outcome != dispatch.OutcomeOK {
				t.Errorf("tool_calls rows = %+v, want one web_search by meru, ok", rows)
			}
			lines := readLines(t, cfg, evs[0].Session)
			if got := meruCalls(lines); len(got) != 1 || got[0].Tool != builtin.WebSearch {
				t.Errorf("merud's calls in the transcript = %+v, want one web_search", got)
			}

			// The prompt holds the results under "From the web", after the
			// parts that stay the same, and the model can search again.
			bodies := chatBodies(t, srv, cfg.Models.Main)
			if len(bodies) != 1 {
				t.Fatalf("main-model calls = %d, want 1", len(bodies))
			}
			system := bodies[0].Messages[0].Content
			for _, want := range []string{webCiteRule, "From the web", "[1] Acme Flow — https://acme.example/flow"} {
				if !strings.Contains(system, want) {
					t.Errorf("system prompt lacks %q", want)
				}
			}
			if strings.Index(system, "From the web") < strings.Index(system, a.currentFilesNote()) {
				t.Error("the web section sits before the files note, among the parts that stay the same")
			}
			if !slices.Contains(bodies[0].toolNames(), builtin.WebSearch) {
				t.Errorf("tools offered = %v, want web_search among them", bodies[0].toolNames())
			}

			// The answer line keeps a note of what the web said.
			web := assistantLine(t, lines).Web
			if len(web) == 0 || web[0].URL != "https://acme.example/flow" {
				t.Errorf("assistant line web notes = %+v, want the Acme Flow result first", web)
			}
		})
	}
}

// TestWebFirstSkipped checks that a question that asks for the web runs no
// up-front search when SearXNG is off, and that a SearXNG that is down
// costs one failed call and no "From the web" section.
func TestWebFirstSkipped(t *testing.T) {
	tests := []struct {
		name      string
		searxng   func(t *testing.T) string // the SearXNG address; "" for none
		wantRows  []string                  // outcome of each tool_calls row
		wantRoute string
	}{
		{"no SearXNG", func(*testing.T) string { return "" }, nil, "direct"},
		{"SearXNG down", func(t *testing.T) string {
			srv := httptest.NewServer(http.NotFoundHandler())
			srv.Close() // nothing answers at this address now
			return srv.URL
		}, []string{dispatch.OutcomeError}, "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Text: "I couldn't search the web."})
			rec := &rowRecorder{}
			a := webAgent(t, cfg, srv, "direct", nil, webDispatcher(cfg, tt.searxng(t), rec))

			evs, err := run(context.Background(), a, rpc.Request{Text: "search the web for Acme Flow"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var outcomes []string
			for _, r := range rec.all() {
				outcomes = append(outcomes, r.Outcome)
			}
			if !slices.Equal(outcomes, tt.wantRows) {
				t.Errorf("tool_calls outcomes = %v, want %v", outcomes, tt.wantRows)
			}
			if r := routeOf(evs).Route; r != tt.wantRoute {
				t.Errorf("route = %q, want %q", r, tt.wantRoute)
			}
			system := chatBodies(t, srv, cfg.Models.Main)[0].Messages[0].Content
			if strings.Contains(system, "From the web") {
				t.Error("the prompt holds a web section, though no search succeeded")
			}
			if got := answerOf(evs); got != "I couldn't search the web." {
				t.Errorf("answer = %q", got)
			}
		})
	}
}

// TestURLWebFirst checks that a question with URLs reads each page, two at
// most, through dispatch before the model's first round. The fake
// ToolRunner stands in for dispatch: the real web_fetch refuses this
// machine's addresses, so an httptest page can't reach it.
func TestURLWebFirst(t *testing.T) {
	tests := []struct {
		name     string
		question string
		specs    []engine.ToolSpec
		want     []string // URLs fetched, in order
	}{
		{"one URL", "what does https://acme.example/flow say about teams?", names("web_search", "web_fetch"),
			[]string{"https://acme.example/flow"}},
		{"three URLs, two read", "compare https://a.example/x, https://b.example/y and https://c.example/z",
			names("web_fetch"), []string{"https://a.example/x", "https://b.example/y"}},
		{"web_fetch off", "what does https://acme.example/flow say?", names("web_search"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"It says teams share boards."}}
			tools := &fakeTools{specs: tt.specs, results: map[string]fakeResult{"web_fetch": {text: pageText}}}
			a := toolsAgent(t, "direct", eng, tools)
			if _, err := run(context.Background(), a, rpc.Request{Text: tt.question}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var got []string
			for _, c := range tools.recorded() {
				var args struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal(c.Args, &args); err != nil {
					t.Fatal(err)
				}
				if c.Name != "web_fetch" || c.Caller != dispatch.CallerMeru {
					t.Errorf("call = %s by %q, want web_fetch by meru", c.Name, c.Caller)
				}
				got = append(got, args.URL)
			}
			// The calls run side by side, so they arrive in any order.
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("fetched %v, want %v", got, tt.want)
			}
			system := eng.lastCall().msgs[0].Content
			if has := strings.Contains(system, "[1] https://acme.example/flow") || strings.Contains(system, "[2] https://acme.example/flow"); has != (len(tt.want) > 0) {
				t.Errorf("prompt holds the page = %v, want %v", has, len(tt.want) > 0)
			}
		})
	}
}

// TestNamedWebFirst checks rule 2: a question that names a thing the
// user's files don't cover searches the web first, and one whose files do
// cover it, that points at a connected tool, that stays direct, or that is
// about the user's own life doesn't.
func TestNamedWebFirst(t *testing.T) {
	strong, weak := 2.0/61, 1.0/61
	withName := []retrieve.Result{result("/n/tools.md", "Tools", "We moved our notes to Acme Flow.", 1, 2, strong)}
	web := names("web_search", "web_fetch", "read_file", "datetime")
	tests := []struct {
		name      string
		question  string
		route     string
		results   []retrieve.Result
		specs     []engine.ToolSpec
		wantQuery string // "" for no web search
		wantRoute string
	}{
		{"files found nothing", "what is Acme Flow?", "search", nil, web, `"Acme Flow"`, "search+tools"},
		{"a weak excerpt", "how does Acme Flow compare with Meru?", "search",
			[]retrieve.Result{result("/n/tools.md", "", "Acme Flow", 1, 2, weak)}, web, `"Acme Flow" compare meru`, "search+tools"},
		{"a strong excerpt without the name", "what is Acme Flow?", "search+tools",
			[]retrieve.Result{result("/n/garden.md", "", "What is the plan?", 1, 2, strong)}, web, `"Acme Flow"`, "search+tools"},
		{"a tools route that searched", "what is Acme Flow?", "tools", nil, web, `"Acme Flow"`, "tools"},
		{"the files cover it", "what is Acme Flow?", "search", withName, web, "", "search"},
		{"direct", "what is Acme Flow?", "direct", nil, web, "", "direct"},
		{"about the user", "when does my Acme Flow plan renew?", "search", nil, web, "", "search"},
		{"names nothing", "how do I bake bread?", "search", nil, web, "", "search"},
		{"web_search off", "what is Acme Flow?", "search", nil, names("web_fetch", "read_file"), "", "search"},
		{"a mail question", "what did the Acme Flow email say?", "tools", nil,
			append(names("google.search_gmail_messages"), web...), "", "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"Acme Flow moves notes between apps."}}
			tools := &fakeTools{specs: tt.specs, results: map[string]fakeResult{"web_search": {text: searchText}}}
			search := &fakeSearcher{results: tt.results}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.8, Outcome: "ok"}},
				search, tools, nil, nil, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: tt.question})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var queries []string
			for _, c := range tools.recorded() {
				var args struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal(c.Args, &args)
				if c.Caller == dispatch.CallerMeru {
					queries = append(queries, args.Query)
				}
			}
			var want []string
			if tt.wantQuery != "" {
				want = []string{tt.wantQuery}
			}
			if !slices.Equal(queries, want) {
				t.Errorf("merud's searches = %q, want %q", queries, want)
			}
			if r := routeOf(evs).Route; r != tt.wantRoute {
				t.Errorf("route = %q, want %q", r, tt.wantRoute)
			}
			system := eng.lastCall().msgs[0].Content
			if has := strings.Contains(system, "From the web"); has != (tt.wantQuery != "") {
				t.Errorf("prompt holds the web section = %v, want %v", has, tt.wantQuery != "")
			}
			// An empty file search on a web-first turn doesn't tell the
			// model to answer from what it knows.
			if tt.wantQuery != "" && tt.results == nil && strings.Contains(system, "Answer from what you know") {
				t.Error("a web-first turn's prompt says to answer from what the model knows")
			}
		})
	}
}

// TestScopeWebFirst checks the desktop app's scopes: web always goes to the
// web first, and files, talk and mail never do, whatever the question.
func TestScopeWebFirst(t *testing.T) {
	tests := []struct {
		scope     string
		wantQuery string // "" for no web search
	}{
		{rpc.ScopeWeb, "Acme Flow"},
		{rpc.ScopeFiles, ""},
		{rpc.ScopeTalk, ""},
		{rpc.ScopeMail, ""},
	}
	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"ok"}}
			tools := &fakeTools{
				specs:   append(names("google.search_gmail_messages"), names("web_search", "web_fetch", "read_file", "datetime")...),
				results: map[string]fakeResult{"web_search": {text: searchText}},
			}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}},
				&fakeSearcher{}, tools, nil, nil, quietLog())
			if _, err := run(context.Background(), a, rpc.Request{Text: "what is Acme Flow", Scope: tt.scope}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var queries []string
			for _, c := range tools.recorded() {
				var args struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal(c.Args, &args)
				queries = append(queries, args.Query)
			}
			var want []string
			if tt.wantQuery != "" {
				want = []string{tt.wantQuery}
			}
			if !slices.Equal(queries, want) {
				t.Errorf("searches = %q, want %q", queries, want)
			}
		})
	}
}

// TestWebNotesInHistory checks rule 3: a turn that read the web keeps a
// note of it on its answer line, a later turn's history carries the note,
// and the history still fits its budget.
func TestWebNotesInHistory(t *testing.T) {
	answer := strings.Repeat("Acme Flow moves notes between apps. ", 25) // about 900 characters
	eng := &fakeEngine{pieces: []string{answer}}
	tools := &fakeTools{specs: names("web_search", "web_fetch"), results: map[string]fakeResult{"web_search": {text: searchText}}}
	cfg := testConfig(t)
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "search the web for Acme Flow"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	session := evs[0].Session
	web := assistantLine(t, readLines(t, cfg, session)).Web
	if len(web) != 3 || web[0].URL != "https://acme.example/flow" || web[0].Gist != "Acme Flow moves notes between apps." {
		t.Fatalf("web notes = %+v, want the three results", web)
	}

	// The next question asks nothing of the web, and its history holds the
	// note after the first answer.
	if _, err := run(context.Background(), a, rpc.Request{Text: "and what does it cost?", Session: session}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	msgs := eng.lastCall().msgs
	note := "[from the web: Acme Flow https://acme.example/flow — Acme Flow moves notes between apps.]"
	if !strings.Contains(msgs[2].Content, note) {
		t.Errorf("history's first answer = %q, want the note %q", msgs[2].Content, note)
	}

	// Ten more web turns push the history past its budget; the oldest
	// turns drop and the newest note stays.
	for range 10 {
		if _, err := run(context.Background(), a, rpc.Request{Text: "search the web for Acme Flow", Session: session}); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}
	msgs = eng.lastCall().msgs
	chars := 0
	for _, m := range msgs[1 : len(msgs)-1] {
		chars += utf8.RuneCountInString(m.Content)
	}
	if chars > maxHistoryChars {
		t.Errorf("history holds %d characters, over the %d budget", chars, maxHistoryChars)
	}
	if last := msgs[len(msgs)-2]; !strings.Contains(last.Content, note) {
		t.Errorf("the newest answer in history lacks its web note: %q", last.Content)
	}
}

// TestSkillNameCall checks rule 4 with the real dispatcher: a model that
// calls "web-research", a skill, as a tool gets a result that names the
// tools to call, the call counts as malformed, and the model's next round
// calls web_search. The skills list tells the model the same up front.
func TestSkillNameCall(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("web-research", `{"query":"Acme Flow"}`)}},
		{calls: []engine.ToolCall{call("web_search", `{"query":"\"Acme Flow\""}`)}},
		{pieces: []string{"Acme Flow moves notes between apps (https://acme.example/flow)."}},
	}}
	searx := startSearx(t)
	rec := &rowRecorder{}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}},
		nil, webDispatcher(cfg, searx.url, rec), nil, nil, quietLog())
	a.UseSkills(fixedSkills{reg: builtinRegistry(t)})

	evs, err := run(context.Background(), a, rpc.Request{Text: "what's new with Acme Flow"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(eng.calls) != 3 {
		t.Fatalf("model calls = %d, want 3", len(eng.calls))
	}
	system := eng.calls[0].msgs[0].Content
	if !strings.Contains(system, skillsListHeader) ||
		!strings.Contains(system, "- web-research: ") || !strings.Contains(system, "To use it, call web_search or web_fetch.") {
		t.Errorf("the skills list doesn't say skills aren't tools:\n%s", system)
	}
	second := eng.calls[1].msgs
	hint := "web-research is a skill, not a tool. Call web_search or web_fetch."
	if got := second[len(second)-1]; got.Role != engine.RoleTool || got.Content != hint {
		t.Errorf("result of the web-research call = %+v, want %q", got, hint)
	}
	rows := rec.all()
	if len(rows) != 2 || rows[0].Tool != "web-research" || rows[0].Outcome != dispatch.OutcomeDenied ||
		rows[1].Tool != builtin.WebSearch || rows[1].Outcome != dispatch.OutcomeOK {
		t.Errorf("tool_calls rows = %+v, want web-research denied, then web_search ok", rows)
	}
	if got := searx.got(); !slices.Equal(got, []string{`"Acme Flow"`}) {
		t.Errorf("SearXNG queries = %q", got)
	}
	if bad := assistantLine(t, readLines(t, cfg, evs[0].Session)).BadCalls; bad != 1 {
		t.Errorf("bad_calls = %d, want 1", bad)
	}
}

// seqRouter hands out one decision per turn, in order, as the router did
// in the session TestQuickSessionReplay replays.
type seqRouter struct {
	mu   sync.Mutex // guards next
	decs []Decision
	next int
}

func (r *seqRouter) Decide(context.Context, string, []engine.Message) (Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.decs[min(r.next, len(r.decs)-1)]
	r.next++
	return d, nil
}

// TestQuickSessionReplay replays the shape of the real session behind this
// change, with made-up names: a product newer than the model ("Acme
// Quick"), a model that searched for an older one it knew, a page the user
// pasted, follow-ups whose history lost the page, and "do a web search
// about quick" routed direct, where the model called the web-research
// skill as a tool.
func TestQuickSessionReplay(t *testing.T) {
	const quickPage = "https://quick.acme.example/\nTitle: Acme Quick\n2.0 KB, HTML. Characters 0 to 48 of 48.\n\n" +
		"Acme Quick is an assistant that answers from team documents."
	eng := &fakeEngine{rounds: []fakeRound{
		// Turn 1: the model swaps in a product it knows.
		{calls: []engine.ToolCall{call("web_search", `{"query":"Acme QuickSight features"}`)}},
		{pieces: []string{"Acme QuickSight is a dashboard tool."}},
		// Turn 2: the user pastes the page.
		{pieces: []string{"Acme Quick answers from team documents."}},
		// Turn 3: a follow-up with no web call.
		{pieces: []string{"People like that it reads team documents."}},
		// Turn 4: "do a web search about quick", and a skill called as a tool.
		{calls: []engine.ToolCall{call("web-research", `{"query":"quick"}`)}},
		{calls: []engine.ToolCall{call("web_search", `{"query":"\"Acme Quick\""}`)}},
		{pieces: []string{"Acme Quick answers from team documents (https://quick.acme.example/)."}},
	}}
	tools := &fakeTools{specs: names("web_search", "web_fetch", "datetime"), results: map[string]fakeResult{
		"web_search": {text: searchText},
		"web_fetch":  {text: quickPage},
	}}
	router := &seqRouter{decs: []Decision{
		{Route: "tools", Confidence: 0.77, Outcome: "ok"},
		{Route: "tools", Confidence: 0.83, Outcome: "ok"},
		{Route: "search+tools", Confidence: 0.54, Outcome: "ok"},
		{Route: "direct", Confidence: 0.55, Outcome: "ok"},
	}}
	cfg := testConfig(t)
	a := New(cfg, eng, router, nil, tools, nil, nil, quietLog())
	a.UseSkills(fixedSkills{reg: builtinRegistry(t)})

	ask := func(session, question string) string {
		t.Helper()
		evs, err := run(context.Background(), a, rpc.Request{Text: question, Session: session})
		if err != nil {
			t.Fatalf("Handle(%q): %v", question, err)
		}
		return evs[0].Session
	}
	meruTools := func() []string {
		var out []string
		for _, c := range tools.recorded() {
			if c.Caller == dispatch.CallerMeru {
				out = append(out, c.Name)
			}
		}
		return out
	}

	// Turn 1: "search about" isn't a phrase that asks for the web, and the
	// name is lower case, so merud makes no call; the model does.
	session := ask("", "can you search about acme quick and compare it to meru")
	if got := meruTools(); len(got) != 0 {
		t.Errorf("turn 1: merud's calls = %v, want none", got)
	}

	// Turn 2: the pasted URL is read before the model's first round.
	ask(session, "https://quick.acme.example/ what is this?")
	if got := meruTools(); !slices.Equal(got, []string{"web_fetch"}) {
		t.Errorf("turn 2: merud's calls = %v, want web_fetch", got)
	}

	// Turn 3: the follow-up's history holds what the page said.
	ask(session, "what do people make of it?")
	history := eng.calls[3].msgs
	note := "[from the web: Acme Quick https://quick.acme.example/ — Acme Quick is an assistant that answers from team documents.]"
	if !slices.ContainsFunc(history, func(m engine.Message) bool { return strings.Contains(m.Content, note) }) {
		t.Errorf("turn 3: history lacks the page's note %q", note)
	}

	// Turn 4: routed direct, and still searched before the answer.
	ask(session, "do a web search about quick and educate yourself")
	if got := meruTools(); !slices.Equal(got, []string{"web_fetch", "web_search"}) {
		t.Errorf("turn 4: merud's calls = %v, want web_fetch, then web_search", got)
	}
	first := eng.calls[4].msgs
	if !strings.Contains(first[0].Content, "From the web") {
		t.Error("turn 4: the first round's prompt lacks the web results")
	}
	second := eng.calls[5].msgs
	if got := second[len(second)-1].Content; got != "web-research is a skill, not a tool. Call web_search or web_fetch." {
		t.Errorf("turn 4: the skill call's result = %q", got)
	}
	lines := readLines(t, cfg, session)
	if last := assistantLine(t, lines); last.BadCalls != 1 || last.Route != "tools" {
		t.Errorf("turn 4: bad_calls = %d, route = %q; want 1 and tools", last.BadCalls, last.Route)
	}
}

// TestWebFirstLogField checks the bounded web_first field on the turn's
// info line: asked, named or none.
func TestWebFirstLogField(t *testing.T) {
	tests := []struct {
		question, route, want string
	}{
		{"search the web for Acme Flow", "direct", "web_first=asked"},
		{"what is Acme Flow?", "search", "web_first=named"},
		{"how do I bake bread?", "search", "web_first=none"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			log, buf := bufferLog(slog.LevelInfo)
			tools := &fakeTools{specs: names("web_search", "web_fetch"), results: map[string]fakeResult{"web_search": {text: searchText}}}
			a := New(testConfig(t), &fakeEngine{pieces: []string{"ok"}},
				&fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.8, Outcome: "ok"}}, &fakeSearcher{}, tools, nil, nil, log)
			if _, err := run(context.Background(), a, rpc.Request{Text: tt.question}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("info log lacks %q:\n%s", tt.want, buf.String())
			}
		})
	}
}
