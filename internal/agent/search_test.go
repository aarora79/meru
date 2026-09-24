// This file tests the search step of a turn: which routes search, what the
// prompt and the "sources" event hold, and what happens when the search
// finds nothing or fails.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// fakeSearcher returns fixed results and remembers each query.
type fakeSearcher struct {
	results []retrieve.Result
	err     error

	mu      sync.Mutex // guards queries
	queries []string
}

func (s *fakeSearcher) Search(ctx context.Context, query string) ([]retrieve.Result, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	// Hand back a copy: the agent shortens paths in place.
	return append([]retrieve.Result(nil), s.results...), nil
}

// result builds one search result.
func result(path, heading, text string, start, end int, score float64) retrieve.Result {
	return retrieve.Result{
		ChunkWithDoc: store.ChunkWithDoc{
			Chunk: store.Chunk{Heading: heading, Text: text, StartLine: start, EndLine: end},
			Path:  path,
			Kind:  "markdown",
		},
		Score: score,
	}
}

func TestSearchRouteAddsExcerptsAndSources(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	garden := filepath.Join(home, "notes", "garden.md")
	search := &fakeSearcher{results: []retrieve.Result{
		result(garden, "Budget", "The Q3 budget for the garden project is 4,200 dollars.", 3, 5, 0.032),
		result("/srv/shared/plan.md", "", "Plant tomatoes in May.", 1, 1, 0.016),
	}}
	for _, route := range []string{"search", "search+tools"} {
		t.Run(route, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"It is 4,200 dollars [1]."}}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: route, Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "What is the garden budget?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}

			types := make([]rpc.EventType, len(evs))
			for i, ev := range evs {
				types[i] = ev.Type
			}
			wantTypes := []rpc.EventType{rpc.EventSession, rpc.EventRoute, rpc.EventSources, rpc.EventToken, rpc.EventDone}
			if !reflect.DeepEqual(types, wantTypes) {
				t.Fatalf("event types = %v, want %v", types, wantTypes)
			}
			wantSources := []rpc.Citation{
				{N: 1, Path: filepath.Join("~", "notes", "garden.md"), Heading: "Budget", StartLine: 3, EndLine: 5, Score: 0.032},
				{N: 2, Path: "/srv/shared/plan.md", StartLine: 1, EndLine: 1, Score: 0.016},
			}
			if got := evs[2].Sources; !reflect.DeepEqual(got, wantSources) {
				t.Errorf("sources = %+v\nwant %+v", got, wantSources)
			}

			msgs := eng.lastCall().msgs
			if len(msgs) != 2 || msgs[0].Role != engine.RoleSystem {
				t.Fatalf("prompt = %+v, want a system message and the question", msgs)
			}
			system := msgs[0].Content
			for _, want := range []string{
				DefaultSystemPrompt,
				"cite each excerpt you use by its number",
				"Never invent",
				"From your files",
				`[1] ` + filepath.Join("~", "notes", "garden.md") + `, "Budget", lines 3–5`,
				"The Q3 budget for the garden project is 4,200 dollars.",
				"[2] /srv/shared/plan.md, line 1",
			} {
				if !strings.Contains(system, want) {
					t.Errorf("system prompt lacks %q:\n%s", want, system)
				}
			}
			if strings.Contains(system, home) {
				t.Errorf("system prompt holds the full home path:\n%s", system)
			}
		})
	}
}

func TestDirectRouteDoesNotSearch(t *testing.T) {
	search := &fakeSearcher{results: []retrieve.Result{result("/n/a.md", "", "x", 1, 1, 0.01)}}
	eng := &fakeEngine{pieces: []string{"hi"}}
	a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.queries) != 0 {
		t.Errorf("route direct searched for %q", search.queries)
	}
	for _, ev := range evs {
		if ev.Type == rpc.EventSources {
			t.Errorf("route direct sent a sources event")
		}
	}
	if got := eng.lastCall().msgs[0].Content; strings.Contains(got, "From your files") || strings.Contains(got, noResults) {
		t.Errorf("system prompt = %q, want no search section", got)
	}
}

// TestToolsRouteSearches pins the v0.2 rule: with no tools yet, the tools
// route searches the user's files like search+tools does.
func TestToolsRouteSearches(t *testing.T) {
	search := &fakeSearcher{results: []retrieve.Result{result("/n/a.md", "", "x", 1, 1, 0.01)}}
	eng := &fakeEngine{pieces: []string{"hi"}}
	a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "what database does meru use"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"what database does meru use"}; !reflect.DeepEqual(search.queries, want) {
		t.Errorf("queries = %q, want %q", search.queries, want)
	}
	if !slices.ContainsFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventSources }) {
		t.Errorf("route tools sent no sources event")
	}
}

func TestFilesNote(t *testing.T) {
	cfg := testConfig(t)
	cfg.Index.Folders = []string{"~/notes", "~/repos/meru"}
	eng := &fakeEngine{pieces: []string{"hi"}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, quietLog())
	if _, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "what files can you see"}); err != nil {
		t.Fatal(err)
	}
	system := eng.lastCall().msgs[0].Content
	if want := "in these folders: ~/notes, ~/repos/meru."; !strings.Contains(system, want) {
		t.Errorf("system prompt lacks %q:\n%s", want, system)
	}
	if !strings.HasPrefix(system, DefaultSystemPrompt) {
		t.Errorf("the folders note should follow the system prompt:\n%s", system)
	}
}

// TestSearchFindsNothing covers an empty index, a search with no match and
// a search that fails: each sends no sources, tells the model it found
// nothing, and still answers.
func TestSearchFindsNothing(t *testing.T) {
	tests := []struct {
		name   string
		search *fakeSearcher
	}{
		{"no results", &fakeSearcher{}},
		{"search fails", &fakeSearcher{err: errors.New("embed: connection refused")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"I don't know."}}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}, tt.search, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "where are my notes?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventSources {
					t.Errorf("sent a sources event: %+v", ev)
				}
			}
			system := eng.lastCall().msgs[0].Content
			if !strings.Contains(system, noResults) || strings.Contains(system, "From your files") {
				t.Errorf("system prompt = %q, want the no-results note and no excerpts", system)
			}
		})
	}
}

func TestSearchCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	search := &fakeSearcher{err: context.Canceled}
	a := New(testConfig(t), &fakeEngine{pieces: []string{"x"}}, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
	err := a.Handle(ctx, rpc.Request{Text: "q"}, func(ev rpc.Event) error {
		if ev.Type == rpc.EventRoute {
			cancel() // the client hangs up before the search
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Handle = %v, want context.Canceled", err)
	}
}

func TestSearchQueryOnFollowUp(t *testing.T) {
	search := &fakeSearcher{}
	eng := &fakeEngine{pieces: []string{"ok"}}
	a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Text: "What did I plan for the garden?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), a, rpc.Request{Session: evs[0].Session, Text: "and the budget?"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"What did I plan for the garden?", "and the budget?\nWhat did I plan for the garden?"}
	if !reflect.DeepEqual(search.queries, want) {
		t.Errorf("queries = %q, want %q", search.queries, want)
	}
}

func TestSearchQuery(t *testing.T) {
	// hist builds a history from user questions, each with a stand-in answer.
	hist := func(qs ...string) []engine.Message {
		var out []engine.Message
		for _, q := range qs {
			out = append(out,
				engine.Message{Role: engine.RoleUser, Content: q},
				engine.Message{Role: engine.RoleAssistant, Content: "an answer"})
		}
		return out
	}
	tests := []struct {
		name     string
		question string
		history  []engine.Message
		want     string
	}{
		{"no history", "what database does meru use", nil, "what database does meru use"},
		{"follow-up joins the last question", "and the budget?", hist("What did I plan for the garden?"),
			"and the budget?\nWhat did I plan for the garden?"},
		{"retry skips an all-filler question", "search again i think it is specified",
			hist("what database does meru use", "try the last question again now"),
			"search again i think it is specified\nwhat database does meru use"},
		{"only filler before", "search again", hist("try again", "check my docs"), "search again"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := searchQuery(tt.question, tt.history); got != tt.want {
				t.Errorf("searchQuery = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShortPath(t *testing.T) {
	home := filepath.FromSlash("/home/u")
	tests := []struct {
		home, path, want string
	}{
		{home, filepath.FromSlash("/home/u/notes/a.md"), filepath.FromSlash("~/notes/a.md")},
		{home, filepath.FromSlash("/home/u2/a.md"), filepath.FromSlash("/home/u2/a.md")},
		{home, filepath.FromSlash("/srv/a.md"), filepath.FromSlash("/srv/a.md")},
		{home, home, home},
		{"", filepath.FromSlash("/home/u/a.md"), filepath.FromSlash("/home/u/a.md")},
	}
	for _, tt := range tests {
		if got := shortPath(tt.home, tt.path); got != tt.want {
			t.Errorf("shortPath(%q, %q) = %q, want %q", tt.home, tt.path, got, tt.want)
		}
	}
}

// TestDirectQuestionNamingAFolderSearches covers the override: a question
// the router sends direct still searches when it names an indexed folder.
func TestDirectQuestionNamingAFolderSearches(t *testing.T) {
	tests := []struct {
		question   string
		wantSearch bool
	}{
		{"what database does Meru use to store its index?", true},
		{"what's in meru's go.mod", true},
		{"what is the capital of France", false},
		{"what does merudaemon mean", false},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Index.Folders = []string{"~/repos/meru", "~/n"}
			search := &fakeSearcher{results: []retrieve.Result{result("/n/a.md", "", "x", 1, 1, 0.01)}}
			a := New(cfg, &fakeEngine{pieces: []string{"ok"}}, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: tt.question})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(search.queries) > 0; got != tt.wantSearch {
				t.Errorf("searched = %v, want %v", got, tt.wantSearch)
			}
			wantRoute := "direct"
			if tt.wantSearch {
				wantRoute = "search"
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventRoute && ev.Route != wantRoute {
					t.Errorf("route event = %q, want %q", ev.Route, wantRoute)
				}
			}
		})
	}
}

func TestFolderNames(t *testing.T) {
	got := folderNames([]string{"~/repos/meru", "~/repos/Meru", "~/n", "~/repos/personal-knowledge-base/raw/"})
	want := []string{"meru", "raw"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folderNames = %q, want %q", got, want)
	}
}
