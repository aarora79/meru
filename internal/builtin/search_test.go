// This file tests search_files with a fake searcher: the arguments it
// takes, the numbered excerpts and citations it returns, the numbers it
// takes from the turn's counter, the cap on its text, and when it is off.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/store"
)

// fakeSearcher returns fixed results, or err, and remembers each query and
// limit it got.
type fakeSearcher struct {
	results []retrieve.Result
	err     error

	mu      sync.Mutex // guards the two lists below
	queries []string
	limits  []int
}

// SearchFiles records the call and returns up to limit of the fixed
// results.
func (f *fakeSearcher) SearchFiles(ctx context.Context, query string, limit int) ([]retrieve.Result, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.limits = append(f.limits, limit)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]retrieve.Result(nil), f.results[:min(limit, len(f.results))]...), nil
}

// chunk builds one search result.
func chunk(path, heading, text string, start, end, page int) retrieve.Result {
	return retrieve.Result{
		ChunkWithDoc: store.ChunkWithDoc{
			Chunk: store.Chunk{Heading: heading, Text: text, StartLine: start, EndLine: end, Page: page},
			Path:  path,
		},
		Score: 0.03,
	}
}

// searchTools returns built-in tools with one [index] folder and s as
// search_files' searcher.
func searchTools(t *testing.T, s FileSearcher) *Tools {
	t.Helper()
	dir := t.TempDir()
	ix, err := index.New(config.Index{Folders: []string{dir}, MaxFileMB: 1}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(filepath.Join(dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil)
	tools.UseSearch(s)
	return tools
}

func TestSearchFilesReturnsNumberedExcerpts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	s := &fakeSearcher{results: []retrieve.Result{
		chunk(filepath.Join(home, "notes", "coase.md"), "Theory of the firm", "Firms exist because markets carry transaction costs.", 3, 9, 0),
		chunk("/srv/papers/nature.pdf", "", "The price mechanism has a cost.", 0, 0, 4),
	}}
	tools := searchTools(t, s)
	// Six numbers already went to excerpts earlier in the turn.
	used := 6
	ctx := dispatch.WithCiteNumbers(context.Background(), func(n int) int {
		first := used + 1
		used += n
		return first
	})
	res, err := tools.Call(ctx, SearchFiles, json.RawMessage(`{"query":"why do firms exist"}`))
	if err != nil || res.IsError {
		t.Fatalf("Call = %+v, %v; want success", res, err)
	}
	for _, want := range []string{
		`for "why do firms exist"`,
		"like [7]",
		"read_file",
		`[7] ` + filepath.Join("~", "notes", "coase.md") + `, "Theory of the firm", lines 3–9`,
		"Firms exist because markets carry transaction costs.",
		"[8] /srv/papers/nature.pdf, page 4",
		"The price mechanism has a cost.",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result lacks %q:\n%s", want, res.Text)
		}
	}
	if len(res.Sources) != 2 {
		t.Fatalf("sources = %+v, want two", res.Sources)
	}
	if c := res.Sources[0]; c.N != 7 || c.Path != filepath.Join("~", "notes", "coase.md") || c.Heading != "Theory of the firm" || c.StartLine != 3 || c.EndLine != 9 {
		t.Errorf("first source = %+v", c)
	}
	if c := res.Sources[1]; c.N != 8 || c.Page != 4 {
		t.Errorf("second source = %+v", c)
	}
	if used != 8 {
		t.Errorf("counter at %d, want 8", used)
	}
	if s.limits[0] != defaultSearchLimit {
		t.Errorf("limit = %d, want the default %d", s.limits[0], defaultSearchLimit)
	}
}

func TestSearchFilesArguments(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string // a piece of the error text; "" for success
	}{
		{"limit", `{"query":"garden","limit":3}`, ""},
		{"most", `{"query":"garden","limit":20}`, ""},
		{"no query", `{}`, "pass query"},
		{"blank query", `{"query":"  "}`, "pass query"},
		{"limit too big", `{"query":"garden","limit":21}`, "limit 21 is out of range"},
		{"limit negative", `{"query":"garden","limit":-1}`, "limit -1 is out of range"},
		{"unknown key", `{"query":"garden","folder":"x"}`, "valid JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := searchTools(t, &fakeSearcher{results: []retrieve.Result{chunk("/n/a.md", "", "garden", 1, 1, 0)}})
			res, err := tools.Call(context.Background(), SearchFiles, json.RawMessage(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if res.IsError {
					t.Errorf("error %q, want success", res.Text)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Text, tt.want) {
				t.Errorf("result = %+v, want an error containing %q", res, tt.want)
			}
		})
	}
}

func TestSearchFilesNothingFoundAndFailure(t *testing.T) {
	res, _ := searchTools(t, &fakeSearcher{}).Call(context.Background(), SearchFiles, json.RawMessage(`{"query":"quasar"}`))
	if res.IsError || !strings.Contains(res.Text, "found nothing") || len(res.Sources) != 0 {
		t.Errorf("empty search = %+v, want a plain note and no sources", res)
	}
	res, _ = searchTools(t, &fakeSearcher{err: errors.New("embed: model not found")}).Call(context.Background(), SearchFiles, json.RawMessage(`{"query":"quasar"}`))
	if !res.IsError || !strings.Contains(res.Text, "model not found") {
		t.Errorf("failed search = %+v, want an error the model can read", res)
	}
}

// TestSearchFilesCap checks that long excerpts stop at maxSearchChars, so
// dispatch never cuts one in half, and that only the excerpts kept get a
// number and a citation.
func TestSearchFilesCap(t *testing.T) {
	long := strings.Repeat("word ", 1000) // 5,000 characters
	var results []retrieve.Result
	for range 5 {
		results = append(results, chunk("/n/long.md", "", long, 1, 90, 0))
	}
	used := 0
	ctx := dispatch.WithCiteNumbers(context.Background(), func(n int) int { used += n; return used - n + 1 })
	res, _ := searchTools(t, &fakeSearcher{results: results}).Call(ctx, SearchFiles, json.RawMessage(`{"query":"word"}`))
	if len(res.Sources) != 2 || used != 2 {
		t.Errorf("kept %d sources and reserved %d numbers, want 2 of each", len(res.Sources), used)
	}
	if n := len([]rune(res.Text)); n > maxSearchChars {
		t.Errorf("result is %d characters, over the %d cap", n, maxSearchChars)
	}
}

// TestSearchFilesOff checks that search_files is off without a searcher,
// and without [index] folders even with one.
func TestSearchFilesOff(t *testing.T) {
	dir := t.TempDir()
	ix, err := index.New(config.Index{Folders: []string{dir}, MaxFileMB: 1}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Builtin{Tools: config.BuiltinTools()}
	noSearch := New(filepath.Join(dir, "config.toml"), cfg, config.Web{}, nil, "", ix, nil, nil)
	noFolders := New(filepath.Join(dir, "config.toml"), cfg, config.Web{}, nil, "", nil, nil, nil)
	noFolders.UseSearch(&fakeSearcher{})
	for name, tools := range map[string]*Tools{"no searcher": noSearch, "no folders": noFolders} {
		for _, s := range tools.Tools() {
			if s.Name == SearchFiles {
				t.Errorf("%s: search_files offered", name)
			}
		}
		if _, err := tools.Call(context.Background(), SearchFiles, json.RawMessage(`{"query":"x"}`)); err == nil {
			t.Errorf("%s: search_files ran", name)
		}
	}
}
