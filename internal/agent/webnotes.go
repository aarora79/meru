// This file holds the web notes a turn leaves for later turns: a short
// record of each page or result its web calls brought back, which the
// assistant line keeps and the session's history hands to the model (see
// transcript.History). A real session showed why. A turn read a product's
// page and answered well; the next three turns, whose history held only
// the questions and answers, made up the product's features instead of
// searching again.

package agent

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/transcript"
)

// The limits on a turn's web notes. Five notes of about 300 characters fit
// in 1,500, under a fifth of the history budget (maxHistoryChars), so a
// few turns of notes still leave room for the questions and answers.
const (
	maxWebNotes     = 5
	maxGistChars    = 300
	maxWebNoteChars = 1500
)

// webHit is one candidate note: what a page or a search result said, and
// whether it came from a page web_fetch read. keptNotes puts pages first,
// since a page says more than a snippet.
type webHit struct {
	note transcript.WebNote
	page bool
}

// resultLine matches the line web_search writes for each result,
// "[n] title — url". The title is greedy, so a title that holds " — "
// itself still leaves the URL at the end.
var resultLine = regexp.MustCompile(`^\[\d+\] (.*) — (https?://\S+)$`)

// fromLine matches the opening of a web_fetch answer to a prompt, "From
// <url> (fetched <date>): <answer>".
var fromLine = regexp.MustCompile(`^From (https?://\S+) \(fetched [^)]*\): `)

// webHitsOf reads the result text of one successful call to tool and
// returns what it says about the web: one hit per web_search result, or
// one for the page web_fetch read. It returns nil for any other tool, and
// for a result it can't read, such as a download. text is the result as
// dispatch returned it, secrets already redacted.
//
// It reads the text the built-in tools write (builtin.formatResults and
// webFetch), so a change to those formats needs a change here; the tests
// run the real tools' output through it.
func webHitsOf(tool, text string) []webHit {
	switch tool {
	case builtin.WebSearch:
		return searchHits(text)
	case builtin.WebFetch:
		if h, ok := pageHit(text); ok {
			return []webHit{h}
		}
	}
	return nil
}

// searchHits returns one hit per result in a web_search result: its title
// and URL, and the snippet on the line after as the gist.
func searchHits(text string) []webHit {
	lines := strings.Split(text, "\n")
	var hits []webHit
	for i, line := range lines {
		m := resultLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := transcript.WebNote{Title: strings.TrimSpace(m[1]), URL: m[2]}
		// The snippet, when the result has one, sits on the next line; a
		// "Published" line or the next result means it has none.
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next != "" && !strings.HasPrefix(next, "[") && !strings.HasPrefix(next, "Published ") {
				n.Gist = oneLine(next, maxGistChars)
			}
		}
		hits = append(hits, webHit{note: n})
	}
	return hits
}

// pageHit returns the hit for a web_fetch result: the answer to a prompt,
// or the page's URL, title and the start of its text. ok is false when
// text starts with neither.
func pageHit(text string) (webHit, bool) {
	if m := fromLine.FindStringSubmatch(text); m != nil {
		return webHit{page: true, note: transcript.WebNote{
			URL: m[1], Gist: oneLine(text[len(m[0]):], maxGistChars),
		}}, true
	}
	// A page's text comes after a head: the URL, maybe a "Title:" line,
	// the size, and a blank line.
	head, body, _ := strings.Cut(text, "\n\n")
	lines := strings.Split(head, "\n")
	if !strings.HasPrefix(lines[0], "http://") && !strings.HasPrefix(lines[0], "https://") {
		return webHit{}, false
	}
	n := transcript.WebNote{URL: strings.TrimSpace(lines[0]), Gist: oneLine(body, maxGistChars)}
	for _, l := range lines[1:] {
		if title, ok := strings.CutPrefix(l, "Title: "); ok {
			n.Title = strings.TrimSpace(title)
		}
	}
	return webHit{page: true, note: n}, true
}

// keptNotes returns the notes the assistant line keeps from hits: pages
// first, then search results, each URL once, at most maxWebNotes, and no
// more than maxWebNoteChars of titles, URLs and gists together. A note
// that would pass that cap is left out, and so is every note after it.
func keptNotes(hits []webHit) []transcript.WebNote {
	// SortStableFunc keeps the order within pages and within results.
	sorted := slices.Clone(hits)
	slices.SortStableFunc(sorted, func(x, y webHit) int {
		switch {
		case x.page && !y.page:
			return -1
		case y.page && !x.page:
			return 1
		}
		return 0
	})
	var out []transcript.WebNote
	used := 0
	for _, h := range sorted {
		if len(out) == maxWebNotes {
			break
		}
		if slices.ContainsFunc(out, func(n transcript.WebNote) bool { return n.URL == h.note.URL }) {
			continue
		}
		size := utf8.RuneCountInString(h.note.Title + h.note.URL + h.note.Gist)
		if used+size > maxWebNoteChars {
			break
		}
		out = append(out, h.note)
		used += size
	}
	return out
}
