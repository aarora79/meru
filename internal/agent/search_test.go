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

func TestRoutesWithoutSearch(t *testing.T) {
	for _, route := range []string{"direct", "tools"} {
		t.Run(route, func(t *testing.T) {
			search := &fakeSearcher{results: []retrieve.Result{result("/n/a.md", "", "x", 1, 1, 0.01)}}
			eng := &fakeEngine{pieces: []string{"hi"}}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: route, Confidence: 0.9, Outcome: "ok"}}, search, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			if len(search.queries) != 0 {
				t.Errorf("route %s searched for %q", route, search.queries)
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventSources {
					t.Errorf("route %s sent a sources event", route)
				}
			}
			if got := eng.lastCall().msgs[0].Content; got != DefaultSystemPrompt {
				t.Errorf("system prompt = %q, want the plain default", got)
			}
		})
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
