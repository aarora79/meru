// This file holds search_files, the built-in tool that runs Meru's hybrid
// search (retrieve.Search: meaning and keywords, merged) over the [index]
// folders and hands the model numbered excerpts. With [index] retrieval =
// "agentic" it is how a turn finds text in the user's files; with "auto" it
// lets the model search again with other words. See ARCHITECTURE.md,
// "Retrieval".

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

// SearchFiles is the search tool's name, as the model sees it.
const SearchFiles = "search_files"

// Limits on one search_files call.
const (
	// defaultSearchLimit is how many excerpts a call returns when the model
	// doesn't say: close to the ten a search before the answer puts in the
	// prompt, a little under so a second call with other words still fits.
	defaultSearchLimit = 8
	// maxSearchLimit caps the limit the model may ask for.
	maxSearchLimit = 20
	// maxSearchChars caps the excerpts' text. dispatch cuts a result at
	// 16,000 characters; staying under that keeps whole excerpts, and a
	// cut excerpt would carry a number with half its text.
	maxSearchChars = 14000
)

// FileSearcher runs the hybrid search behind search_files: the limit best
// chunks for query, best first. merud passes an adapter around
// retrieve.Search, the same search a turn runs before the answer; tests
// pass a fake.
type FileSearcher interface {
	SearchFiles(ctx context.Context, query string, limit int) ([]retrieve.Result, error)
}

// UseSearch turns search_files on: s runs its searches. merud calls it
// once, before the first turn. Without it, search_files stays off, as the
// file tools do without [index] folders.
//
// It is a method rather than a parameter of New, as UseModel is, so the
// tests that build Tools without a store don't change.
func (t *Tools) UseSearch(s FileSearcher) {
	t.search = s
}

// searchArgs is the JSON object the model sends to search_files.
type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// searchFiles runs one search and returns the excerpts as text for the
// model, with their citations. Each excerpt gets a number from
// dispatch.CiteNumbers, so the numbers follow the excerpts already in the
// turn and the model's [n] marks stay unique. It fails, with text the
// model reads, on bad arguments and when the search fails.
func (t *Tools) searchFiles(ctx context.Context, raw json.RawMessage) (string, []rpc.Citation, error) {
	var a searchArgs
	if err := decode(SearchFiles, raw, &a); err != nil {
		return "", nil, err
	}
	a.Query = strings.TrimSpace(a.Query)
	if a.Query == "" {
		return "", nil, errors.New("search_files: pass query, the words or meaning to look for")
	}
	if a.Limit == 0 {
		a.Limit = defaultSearchLimit
	}
	if a.Limit < 1 || a.Limit > maxSearchLimit {
		return "", nil, fmt.Errorf("search_files: limit %d is out of range; pass 1 to %d", a.Limit, maxSearchLimit)
	}
	results, err := t.search.SearchFiles(ctx, a.Query, a.Limit)
	if err != nil {
		return "", nil, fmt.Errorf("search_files: the search failed: %v", err)
	}
	if len(results) == 0 {
		return fmt.Sprintf("search_files found nothing for %q. Try other words, or grep for an exact name.", a.Query), nil, nil
	}

	// Keep the best excerpts that fit under maxSearchChars. The results
	// come best first, so the cut drops the weakest. The first always
	// stays, cut to fit when it is longer on its own.
	kept, used := 0, 0
	for _, r := range results {
		n := utf8.RuneCountInString(r.Text) + 200 // 200 for the citation line
		if kept > 0 && used+n > maxSearchChars {
			break
		}
		used += n
		kept++
	}
	results = results[:kept]

	first := dispatch.CiteNumbers(ctx, len(results))
	sources := make([]rpc.Citation, len(results))
	var b strings.Builder
	fmt.Fprintf(&b, "Excerpts from the user's files for %q, best first. "+
		"Cite the ones you use by their number, like [%d]. "+
		"To read a whole file, call read_file with its path.\n", a.Query, first)
	for i, r := range results {
		r.Path = t.show(r.Path)
		// Cite writes the excerpt's number, path, heading and lines or
		// page, as the excerpts in the prompt show them.
		b.WriteString("\n" + retrieve.Cite(first+i, r) + "\n")
		b.WriteString(cut(strings.TrimSpace(r.Text), maxSearchChars-200) + "\n")
		sources[i] = rpc.Citation{
			N: first + i, Path: r.Path, Heading: r.Heading,
			StartLine: r.StartLine, EndLine: r.EndLine, Page: r.Page, Score: r.Score,
		}
	}
	return b.String(), sources, nil
}

// searchSpec returns search_files' spec. Its description tells the model
// how the tool differs from grep, and when to go on to read_file.
func (t *Tools) searchSpec() engine.ToolSpec {
	return engine.ToolSpec{
		Name: SearchFiles,
		Description: "Searches the user's indexed files and returns the best-matching excerpts, numbered, " +
			"each with its file path, heading and lines or page. It finds passages by meaning as well as by words, " +
			"so a paraphrase or a question works as the query; use grep instead for an exact name or phrase. " +
			"To read a result's whole file, call read_file with its path. " +
			"The indexed folders are: " + t.nameFolders(t.files.Folders()) + ".",
		Parameters: mustSchema(map[string]any{
			"query": prop("string", "What to look for: a few words, a phrase or a question."),
			"limit": prop("integer", fmt.Sprintf("How many excerpts to return, 1 to %d. Default %d.", maxSearchLimit, defaultSearchLimit)),
		}, "query"),
	}
}
