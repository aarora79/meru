// This file tests [index] retrieval = "agentic" and the sources that
// tools return: an agentic turn runs no search before the answer and no
// recall of earlier conversations, offers search_files with the other file
// tools and tells the model to explore; and the excerpts search_files
// returns number on from the prompt's, ride on the tool_result event, and
// join the turn's "sources" event, in both modes.

package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

// fakeFileSearch is search_files' searcher: it answers every query with
// its fixed results and counts the calls.
type fakeFileSearch struct {
	results []retrieve.Result

	mu    sync.Mutex // guards calls
	calls int
}

// SearchFiles counts the call and returns a copy of the fixed results.
func (f *fakeFileSearch) SearchFiles(ctx context.Context, query string, limit int) ([]retrieve.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return append([]retrieve.Result(nil), f.results...), nil
}

// fileToolsDispatcher returns a dispatcher over the real built-in tools,
// reading one temp [index] folder, with fs as search_files' searcher.
func fileToolsDispatcher(t *testing.T, cfg *config.Config, fs builtin.FileSearcher) *dispatch.Dispatcher {
	t.Helper()
	cfg.Index.Folders = []string{t.TempDir()}
	ix, err := index.New(cfg.Index, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil)
	tools.UseSearch(fs)
	return dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{})
}

// searchCall is a model's call to search_files for query.
func searchCall(query string) engine.ToolCall {
	args, _ := json.Marshal(map[string]string{"query": query})
	return engine.ToolCall{Name: builtin.SearchFiles, Arguments: args}
}

func TestAgenticTurnSkipsSearchFirst(t *testing.T) {
	cfg := testConfig(t)
	cfg.Index.Retrieval = config.RetrievalAgentic
	fs := &fakeFileSearch{results: []retrieve.Result{result("/n/coase.md", "Firms", "Transaction costs explain firms.", 1, 4, 0.03)}}
	disp := fileToolsDispatcher(t, &cfg, fs)
	search := &fakeSearcher{
		results:  []retrieve.Result{result("/n/other.md", "", "Up-front excerpt.", 1, 1, 0.03)},
		sessions: []retrieve.SessionResult{pastSession(time.Now().AddDate(0, 0, -3), "Talked about Coase.", nil)},
	}
	recall := &fakeRecall{recalled: []retrieve.Memory{recalled("people", "Sam is the user's manager")}}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{searchCall("theory of the firm")}},
		{pieces: []string{"Firms cut transaction costs [1]."}},
	}}
	for _, route := range []string{"search", "search+tools", "tools"} {
		t.Run(route, func(t *testing.T) {
			eng.calls = nil
			search.queries, search.sessionQueries = nil, nil
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: route, Confidence: 0.9, Outcome: "ok"}}, search, disp, nil, recall, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: "What does my knowledge base say about Coase's theory of the firm?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(search.queries) != 0 || len(search.sessionQueries) != 0 {
				t.Errorf("searched first: files %v, sessions %v; want neither", search.queries, search.sessionQueries)
			}
			first := eng.calls[0]
			system := first.msgs[0].Content
			for _, gone := range []string{"From your files", "From earlier conversations", "Up-front excerpt", "You can't open or list files yourself"} {
				if strings.Contains(system, gone) {
					t.Errorf("system prompt holds %q", gone)
				}
			}
			for _, want := range []string{exploreNote, "Sam is the user's manager"} {
				if !strings.Contains(system, want) {
					t.Errorf("system prompt lacks %q", want)
				}
			}
			var offered []string
			for _, s := range first.tools {
				offered = append(offered, s.Name)
			}
			for _, want := range []string{builtin.SearchFiles, builtin.Grep, builtin.ReadFile, builtin.ListFolder} {
				if !slices.Contains(offered, want) {
					t.Errorf("offered %v, want %s among them", offered, want)
				}
			}
			// The only sources event comes from search_files, numbered from 1.
			var sources [][]rpc.Citation
			for _, ev := range evs {
				if ev.Type == rpc.EventSources {
					sources = append(sources, ev.Sources)
				}
			}
			if len(sources) != 1 || len(sources[0]) != 1 || sources[0][0].N != 1 || sources[0][0].Path != "/n/coase.md" {
				t.Errorf("sources events = %+v, want one with [1] /n/coase.md", sources)
			}
		})
	}
}

func TestToolSourcesNumberAfterThePrompts(t *testing.T) {
	cfg := testConfig(t)
	fs := &fakeFileSearch{results: []retrieve.Result{
		result("/n/naur.md", "Theory building", "Programming builds a theory in the programmer's head.", 2, 8, 0.03),
		result("/n/naur2.md", "", "The theory dies with the team.", 1, 3, 0.02),
	}}
	disp := fileToolsDispatcher(t, &cfg, fs)
	search := &fakeSearcher{results: []retrieve.Result{
		result("/n/a.md", "", "First up-front excerpt.", 1, 1, 0.03),
		result("/n/b.md", "", "Second up-front excerpt.", 1, 1, 0.02),
	}}
	eng := &fakeEngine{rounds: []fakeRound{
		// Two searches in one round run at the same time; their numbers
		// must not overlap.
		{calls: []engine.ToolCall{searchCall("naur theory"), searchCall("programming as theory building")}},
		{pieces: []string{"Naur says programming builds a theory [3]."}},
	}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}, search, disp, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Text: "What did Naur argue in Programming as Theory Building?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var results []*rpc.ToolEvent
	var sourceEvents [][]rpc.Citation
	for _, ev := range evs {
		switch ev.Type {
		case rpc.EventToolResult:
			results = append(results, ev.Tool)
		case rpc.EventSources:
			sourceEvents = append(sourceEvents, ev.Sources)
		}
	}
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want 2", len(results))
	}
	var toolNs []int
	for _, r := range results {
		for _, c := range r.Sources {
			toolNs = append(toolNs, c.N)
		}
	}
	slices.Sort(toolNs)
	if !reflect.DeepEqual(toolNs, []int{3, 4, 5, 6}) {
		t.Errorf("tool_result source numbers = %v, want 3 to 6 after the prompt's two", toolNs)
	}
	// The first sources event holds the prompt's two; the last one every
	// source, in number order.
	if len(sourceEvents) != 2 || len(sourceEvents[0]) != 2 {
		t.Fatalf("sources events = %+v, want the prompt's, then all six", sourceEvents)
	}
	last := sourceEvents[1]
	var ns []int
	for _, c := range last {
		ns = append(ns, c.N)
	}
	if !reflect.DeepEqual(ns, []int{1, 2, 3, 4, 5, 6}) {
		t.Errorf("last sources event numbers = %v, want 1 to 6", ns)
	}
	// The model read the same numbers in the tool results.
	msgs := eng.lastCall().msgs
	var toolText string
	for _, m := range msgs {
		if m.Role == engine.RoleTool {
			toolText += m.Content
		}
	}
	for _, want := range []string{"[3] /n/naur.md", "[5] /n/naur.md"} {
		if !strings.Contains(toolText, want) {
			t.Errorf("tool results lack %q:\n%s", want, toolText)
		}
	}
	// The client's list of cited sources finds the tool's excerpt.
	var answer string
	for _, ev := range evs {
		if ev.Type == rpc.EventToken {
			answer += ev.Text
		}
	}
	if cited := rpc.Cited(answer, last); len(cited) != 1 || cited[0].N != 3 {
		t.Errorf("cited = %+v, want [3] alone", cited)
	}
}

// names builds tool schemas that carry only a name, enough for the notes.
func names(ns ...string) []engine.ToolSpec {
	var specs []engine.ToolSpec
	for _, n := range ns {
		specs = append(specs, engine.ToolSpec{Name: n})
	}
	return specs
}

func TestNoteFor(t *testing.T) {
	tests := []struct {
		name  string
		specs []engine.ToolSpec
		want  string
	}{
		{"none", nil, ""},
		{"file tools alone", names("read_file", "search_files"), ""},
		{"file tools and a command", names("grep", "cmd.git-log"), commandsNote},
		{"with datetime", names("datetime", "grep"), toolsNote},
		{"an MCP tool", names("obsidian.obsidian_read_note", "cmd.git-log"), toolsNote},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := noteFor(tt.specs); got != tt.want {
				t.Errorf("noteFor = %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestFileToolsNoteFor covers which turns get the note on the file tools:
// only a file turn that offers them, with the note for its retrieval mode.
func TestFileToolsNoteFor(t *testing.T) {
	tests := []struct {
		name     string
		agentic  bool
		specs    []engine.ToolSpec
		fileTurn bool
		want     string
	}{
		{"file turn", false, names("datetime", "read_file", "search_files"), true, fileToolsNote},
		{"file turn, agentic", true, names("datetime", "grep"), true, exploreNote},
		{"not a file turn", false, names("web_search", "read_file", "grep"), false, ""},
		{"not a file turn, agentic", true, names("web_search", "read_file", "grep"), false, ""},
		{"file turn with no file tools", false, names("datetime", "cmd.git-log"), true, ""},
		{"no tools", true, nil, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{agentic: tt.agentic}
			if got := a.fileToolsNoteFor(tt.specs, tt.fileTurn); got != tt.want {
				t.Errorf("fileToolsNoteFor = %q\nwant %q", got, tt.want)
			}
		})
	}
}
