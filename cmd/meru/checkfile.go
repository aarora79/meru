// This file holds the check file that `meru check` runs: the format of one
// question, the parser that reads the file and refuses a bad line, and the
// grader that turns one turn's events into PASS or FAIL with reasons. It
// talks to nobody; check.go sends the questions to merud.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// checkQuestion is one line of the check file:
//
//	{"id": "direct-capital", "category": "direct",
//	 "question": "What is the capital of Australia?",
//	 "want": {"route": ["direct"], "answer_any": ["Canberra"]}}
//
// The words in backquotes are struct tags: they tell encoding/json which
// JSON key fills each field.
type checkQuestion struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Question string `json:"question"`
	// Session names a group. Questions with the same group run in one
	// merud session, in file order; a question with no group gets a
	// session of its own.
	Session string    `json:"session,omitempty"`
	Want    checkWant `json:"want"`
}

// checkWant says what a good turn looks like. Every field is optional; a
// question passes when every field it sets passes.
type checkWant struct {
	// Route lists the routes that count as right.
	Route []string `json:"route,omitempty"`
	// Tools lists tools the turn must call; toolMatches says what counts as
	// a match.
	Tools []string `json:"tools,omitempty"`
	// NoTools means the turn must call no tool at all.
	NoTools bool `json:"no_tools,omitempty"`
	// AnswerAny needs one of these in the answer, AnswerAll needs every
	// one. Both ignore case.
	AnswerAny []string `json:"answer_any,omitempty"`
	AnswerAll []string `json:"answer_all,omitempty"`
	// AnswerNone fails the answer when it holds any of these, ignoring
	// case. It catches an answer that claims an action no tool took, such
	// as "I've sent the email" after the send was declined.
	AnswerNone []string `json:"answer_none,omitempty"`
	// SourcesAny needs a path in the turn's sources events that holds one
	// of these; SourcesNone needs no path to hold any of them. The events
	// cover the excerpts in the prompt and those a tool such as
	// search_files returned. Both ignore case.
	SourcesAny  []string `json:"sources_any,omitempty"`
	SourcesNone []string `json:"sources_none,omitempty"`
	// MaxSeconds is how long the turn may take. Zero means no limit.
	MaxSeconds float64 `json:"max_seconds,omitempty"`
}

// parseChecks reads a check file: one JSON object per line, with blank lines
// and lines that start with "#" skipped. name labels the messages. It stops
// at the first bad line and says which line it was: JSON that doesn't parse,
// a field the format doesn't have, a missing id, category or question, a
// negative max_seconds, or an id used twice.
func parseChecks(r io.Reader, name string) ([]checkQuestion, error) {
	var questions []checkQuestion
	firstLine := map[string]int{} // id → the line that first used it
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // allow lines up to 1 MiB
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		q, err := parseCheckLine(text)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", name, n, err)
		}
		if first, ok := firstLine[q.ID]; ok {
			return nil, fmt.Errorf("%s line %d: id %q is already used on line %d", name, n, q.ID, first)
		}
		firstLine[q.ID] = n
		questions = append(questions, q)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("%s has no questions", name)
	}
	return questions, nil
}

// parseCheckLine decodes one line into a question and checks its fields. It
// fails on a field the format doesn't have, so a typo such as "answr_any"
// stops the run instead of passing every answer.
func parseCheckLine(text string) (checkQuestion, error) {
	var q checkQuestion
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		// The decoder's messages start with "json: ", which says nothing
		// the line number doesn't.
		return q, errors.New(strings.TrimPrefix(err.Error(), "json: "))
	}
	if dec.More() {
		return q, errors.New("more than one JSON object on the line")
	}
	switch {
	case q.ID == "":
		return q, errors.New(`missing "id"`)
	case q.Category == "":
		return q, errors.New(`missing "category"`)
	case strings.TrimSpace(q.Question) == "":
		return q, errors.New(`missing "question"`)
	case q.Want.MaxSeconds < 0:
		return q, errors.New(`"max_seconds" is negative`)
	}
	return q, nil
}

// selectChecks keeps the questions whose id or category appears in only, a
// comma-separated list such as "direct,web-go". An empty only keeps them
// all. It fails when a name in the list matches no question, which is
// usually a typo.
func selectChecks(questions []checkQuestion, only string) ([]checkQuestion, error) {
	if only == "" {
		return questions, nil
	}
	names := strings.Split(only, ",")
	used := map[string]bool{}
	var kept []checkQuestion
	for _, q := range questions {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == q.ID || name == q.Category {
				used[name] = true
				kept = append(kept, q)
				break
			}
		}
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && !used[name] {
			return nil, fmt.Errorf("--only %s: no question has that id or category", name)
		}
	}
	return kept, nil
}

// turnRecord is what one question's turn did, gathered from its events.
type turnRecord struct {
	Route string
	// Tools lists every tool the model asked for, by full name, in call
	// order, each once. Ran holds the ones that ran: a call that was
	// refused approval is in Tools but not in Ran.
	Tools   []string
	Ran     []string
	Sources []string // paths from every sources event, each once
	Answer  string
	Seconds float64
	Error   string // the error event's text, if the turn failed
	// Stats are the timings and token counts from the turn's done event:
	// time to first and last token, time per output token, and the main
	// model's tokens in and out. They stay zero when merud sent none.
	TTFTMillis int64
	TTLTMillis int64
	TPOTMillis float64
	TokensIn   int
	TokensOut  int
}

// grade checks rec against want and returns why it fails, one short reason
// per field that fails. An empty result means the question passed. A turn
// that ended in an error fails on that alone.
func grade(want checkWant, rec turnRecord) []string {
	var reasons []string
	if rec.Error != "" {
		reasons = append(reasons, "merud error: "+rec.Error)
	}
	if len(want.Route) > 0 && !slices.Contains(want.Route, rec.Route) {
		got := rec.Route
		if got == "" {
			got = "none"
		}
		reasons = append(reasons, fmt.Sprintf("route %s, want %s", got, strings.Join(want.Route, " or ")))
	}
	for _, w := range want.Tools {
		if !slices.ContainsFunc(rec.Ran, func(name string) bool { return toolMatches(w, name) }) {
			reasons = append(reasons, fmt.Sprintf("tool %s not called", w))
		}
	}
	if want.NoTools && len(rec.Tools) > 0 {
		reasons = append(reasons, "called "+strings.Join(shortNames(rec.Tools), ", ")+", want no tools")
	}
	answer := strings.ToLower(rec.Answer)
	if len(want.AnswerAny) > 0 && len(containing(answer, want.AnswerAny)) == 0 {
		reasons = append(reasons, "answer lacks all of: "+strings.Join(want.AnswerAny, ", "))
	}
	if len(want.AnswerAll) > 0 {
		if missing := notContaining(answer, want.AnswerAll); len(missing) > 0 {
			reasons = append(reasons, "answer lacks: "+strings.Join(missing, ", "))
		}
	}
	if hit := containing(answer, want.AnswerNone); len(hit) > 0 {
		reasons = append(reasons, "answer holds: "+strings.Join(hit, ", "))
	}
	if len(want.SourcesAny) > 0 && !slices.ContainsFunc(rec.Sources, func(p string) bool {
		return len(containing(strings.ToLower(p), want.SourcesAny)) > 0
	}) {
		reasons = append(reasons, "no source matches any of: "+strings.Join(want.SourcesAny, ", "))
	}
	for _, p := range rec.Sources {
		if hit := containing(strings.ToLower(p), want.SourcesNone); len(hit) > 0 {
			reasons = append(reasons, fmt.Sprintf("source %s matches %s", p, strings.Join(hit, ", ")))
		}
	}
	if want.MaxSeconds > 0 && rec.Seconds >= want.MaxSeconds {
		reasons = append(reasons, fmt.Sprintf("took %.1fs, want under %gs", rec.Seconds, want.MaxSeconds))
	}
	return reasons
}

// toolMatches reports whether the tool a check wants, w, names the tool the
// turn called, name. Three things count, so a check can name a tool as
// `meru tools` lists it or by its short name:
//
//   - the full name: "obsidian.obsidian_search_vault", "read_file";
//   - a prefix that ends at a dot: "obsidian" matches any obsidian tool,
//     and "cmd" any local command;
//   - the name after the first dot: "git-log" matches "cmd.git-log".
func toolMatches(w, name string) bool {
	return w == name || strings.HasPrefix(name, w+".") || shortName(name) == w
}

// shortName drops the server prefix from a tool name, up to the first dot:
// "obsidian.obsidian_search_vault" becomes "obsidian_search_vault". A
// built-in such as "read_file" has no prefix and comes back as it is.
func shortName(name string) string {
	if _, rest, ok := strings.Cut(name, "."); ok {
		return rest
	}
	return name
}

// shortNames applies shortName to each name.
func shortNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = shortName(n)
	}
	return out
}

// containing returns the words that appear in text, ignoring case. text
// must already be lower case.
func containing(text string, words []string) []string {
	var hit []string
	for _, w := range words {
		if strings.Contains(text, strings.ToLower(w)) {
			hit = append(hit, w)
		}
	}
	return hit
}

// notContaining returns the words that don't appear in text, ignoring case.
// text must already be lower case.
func notContaining(text string, words []string) []string {
	var miss []string
	for _, w := range words {
		if !strings.Contains(text, strings.ToLower(w)) {
			miss = append(miss, w)
		}
	}
	return miss
}

// compactJSON is json.Marshal without HTML escaping, so an answer with "<"
// or "&" stays readable in a results file. It returns the JSON with no
// trailing new line.
func compactJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
