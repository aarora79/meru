// This file holds the web-first step of a turn (ARCHITECTURE.md, "Web
// first"). When a question asks for the web, gives a web address, or names
// a thing the user's files don't cover, merud runs web_search or web_fetch
// itself before the model's first round, through dispatch like any other
// call, and puts what came back in the prompt under "From the web". It
// also holds the tests that decide it: the phrases that ask for the web,
// the web addresses in a question, and the detector for named things.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
)

// Why a turn went to the web first. The turn's info log line and span
// carry one of these as web_first, a fixed set of three.
const (
	webFirstAsked = "asked" // the question asked for the web or gave a URL, or the scope is web
	webFirstNamed = "named" // the question named a thing the user's files don't cover
	webFirstNone  = "none"
)

// Limits on the web-first step.
const (
	// maxFetchURLs is how many URLs from the question merud reads before
	// the model's first round. A question rarely gives more than two, and
	// each page can fill thousands of tokens.
	maxFetchURLs = 2
	// maxWebChars caps the "From the web" section, about 2,000 tokens:
	// eight search results fit whole, and a fetched page gives its opening.
	// The model can read on with web_fetch.
	maxWebChars = 8000
	// maxWebQuery caps the words sent to SearXNG.
	maxWebQuery = 300
	// maxKeyWords is how many of the question's key words join a named
	// thing in its search.
	maxKeyWords = 5
	// maxQuotedWords is the most words a quoted term may have and still
	// count as a name. The router's labelled questions quote sentences to
	// rewrite or translate, of five words and more.
	maxQuotedWords = 4
)

// weakScore is the fused score at or below which the best excerpt from the
// user's files counts as weak. Vector search ranks every chunk, so it
// always hands back a nearest chunk, however unrelated. Reciprocal-rank
// fusion gives the top chunk of one list 1/(60+1), about 0.0164, and a
// chunk that both lists found at least 2/(60+50), about 0.0182 (see
// retrieve's rrf and its lists of 50). So a best score of 1/61 or less
// means only one of the two searches found the chunk.
const weakScore = 1.0 / 61

// webCiteRule opens the "From the web" section. It mirrors citeRule for
// the user's files, and carries the rule a real session broke: asked about
// a product newer than its training, the model searched for an older
// product with a similar name and answered about that one.
const webCiteRule = "Below, under \"From the web\", is what Meru found on the web for this question before you started. " +
	"When it helps, answer from it and cite each source you use by its URL. " +
	"Never swap in a product, company or person you know for the one the user named. " +
	"If the results are about something else, say so, and call web_search again with other words."

// noResultsWebFirst takes noResults' place on a turn that went to the web
// first and found nothing in the user's files. noResults says "answer from
// what you know", which would pull the model away from the web results
// right below it.
const noResultsWebFirst = "A search of the user's files found nothing relevant to this question, so don't cite any files."

// webPhrases returns the phrases that ask for the web, as lower-case word
// sequences, longest first, so webQuery strips "search the web" whole
// rather than "web" alone. Each is matched as whole words: "website" isn't
// "web", and "research" alone isn't a phrase, since "my research folder"
// is about the user's files.
func webPhrases() [][]string {
	return [][]string{
		{"search", "the", "internet"}, {"search", "the", "web"}, {"search", "the", "net"},
		{"look", "it", "up"}, {"look", "this", "up"}, {"look", "that", "up"}, {"look", "them", "up"},
		{"do", "some", "research"}, {"do", "research"}, {"deep", "research"},
		{"web", "search"}, {"internet", "search"}, {"search", "online"},
		{"research", "about"}, {"research", "this"}, {"research", "it"}, {"research", "that"},
		{"google", "it"}, {"google", "this"}, {"google", "that"}, {"google", "for"},
		{"look", "up"}, {"the", "net"},
		{"web"}, {"internet"}, {"online"}, {"websearch"},
	}
}

// askPhrase returns where the first phrase from webPhrases starts in ws,
// the question's lower-case words, and how many words it spans. ok is
// false when no phrase is there.
func askPhrase(ws []string) (start, n int, ok bool) {
	for i := range ws {
		for _, p := range webPhrases() {
			// slices.Equal compares two slices item by item.
			if i+len(p) <= len(ws) && slices.Equal(ws[i:i+len(p)], p) {
				return i, len(p), true
			}
		}
	}
	return 0, 0, false
}

// asksForWeb reports whether question asks for the web in words that
// webPhrases lists, such as "search the web", "look it up" or "online",
// and specs include web_search, the tool such a question needs.
func asksForWeb(question string, specs []engine.ToolSpec) bool {
	if !offersWebSearch(specs) {
		return false
	}
	_, _, ok := askPhrase(words(question))
	return ok
}

// urlPattern matches an http or https address in a question: the scheme,
// then everything up to a space, a quote or a bracket. It is a
// package-level value because regexp.MustCompile runs once; a compiled
// regexp never changes after that.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"'()\[\]{}]+`)

// webURLs returns the http and https addresses in question, each once, at
// most maxFetchURLs, with any full stop, comma or other mark that ends
// the sentence taken off the end.
func webURLs(question string) []string {
	var out []string
	for _, u := range urlPattern.FindAllString(question, -1) {
		u = strings.TrimRight(u, ".,;:!?")
		if len(out) < maxFetchURLs && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// givesURL reports whether question holds an http or https address and
// specs include web_fetch, the tool that reads one.
func givesURL(question string, specs []engine.ToolSpec) bool {
	return len(webURLs(question)) > 0 && offersTool(specs, builtin.WebFetch)
}

// offersTool reports whether specs hold the tool called name.
func offersTool(specs []engine.ToolSpec, name string) bool {
	return slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return s.Name == name })
}

// webQuery returns the words to search the web for when the user asked
// for the web: the question with its URLs, the phrases that asked for the
// web, the filler words at either end and a closing mark such as "?" taken
// out, such as "Acme Flow pricing" from "can you search the web for Acme
// Flow pricing?". A
// follow-up too short to stand alone gets the session's latest earlier
// question with a subject after it, as a search of the files does (see
// searchQuery). The result is cut to maxWebQuery characters.
func webQuery(question string, history []engine.Message) string {
	tokens := strings.Fields(urlPattern.ReplaceAllString(question, " "))
	// keys holds each token in lower case with its punctuation taken off,
	// the form webPhrases uses.
	keys := make([]string, len(tokens))
	for i, tok := range tokens {
		keys[i] = strings.ToLower(strings.TrimFunc(tok, notWordRune))
	}
	// Take out every phrase that asks for the web, as in "look it up
	// online". Each pass removes one, so the loop ends.
	for {
		start, n, ok := askPhrase(keys)
		if !ok {
			break
		}
		tokens = slices.Delete(tokens, start, start+n)
		keys = slices.Delete(keys, start, start+n)
	}
	// Drop filler words, such as "can you ... for the", from both ends.
	for len(keys) > 0 && (keys[0] == "" || isFiller(keys[0])) {
		tokens, keys = tokens[1:], keys[1:]
	}
	for len(keys) > 0 && (keys[len(keys)-1] == "" || isFiller(keys[len(keys)-1])) {
		tokens, keys = tokens[:len(tokens)-1], keys[:len(keys)-1]
	}
	q := strings.TrimRight(strings.Join(tokens, " "), "?.!,;:")
	if q == "" {
		q = strings.TrimSpace(urlPattern.ReplaceAllString(question, " "))
	}
	// searchQuery joins an earlier question with a line break; a search
	// engine wants one line.
	q = strings.Join(strings.Fields(searchQuery(q, history)), " ")
	return cutRunes(q, maxWebQuery)
}

// namedQuery returns the words to search the web for a named thing: the
// name in double quotes, so the search engine keeps its words together,
// then up to maxKeyWords of the question's other words that aren't
// filler. "How does Acme Flow compare with Meru?" gives `"Acme Flow"
// compare meru`.
func namedQuery(name, question string) string {
	inName := words(name)
	var keys []string
	for _, w := range words(question) {
		if len(keys) == maxKeyWords {
			break
		}
		if !isFiller(w) && !slices.Contains(inName, w) && !slices.Contains(keys, w) {
			keys = append(keys, w)
		}
	}
	return cutRunes(strings.TrimSpace(`"`+name+`" `+strings.Join(keys, " ")), maxWebQuery)
}

// quotedPattern matches a term in double quotes, straight or curly, of 2
// to 60 characters. Single quotes stay out: an apostrophe, as in "it's",
// would open a quote that never closes.
var quotedPattern = regexp.MustCompile(`["“]([^"”\n]{2,60})["”]`)

// namedThing returns the first specific thing question names, or "" when
// it names none. It knows three shapes, each simple enough to explain:
//
//   - a term of one to maxQuotedWords words in double quotes: `what is
//     "Acme Flow"?` gives "Acme Flow". A longer quote is a sentence, such
//     as a message to rewrite or send, and doesn't count;
//   - a run of capitalised words, such as "Contoso Relay" in "tell me about
//     Contoso Relay". A single capitalised word that starts a sentence
//     doesn't count, since every sentence starts with one; a run of two or
//     more does ("Acme Flow is new"). A single word needs three characters
//     or more, so "Q3" and "Go" don't count;
//   - a word shaped like a product name wherever it sits: a capital after
//     the first letter ("QuickSight", "iPhone"), or letters mixed with
//     digits in four or more characters ("qwen3", "GPT-5").
//
// Words in stopName never count, and they split a run: "I", the months and
// days, "Meru", greetings, and the question words people capitalise in a
// title, such as "What Is Acme Flow". The detector reads English only.
func namedThing(question string) string {
	// FindAllStringSubmatch returns every match, each as the whole match
	// followed by the part in brackets; -1 means no limit.
	for _, m := range quotedPattern.FindAllStringSubmatch(question, -1) {
		if n := len(strings.Fields(m[1])); n > 0 && n <= maxQuotedWords {
			return strings.TrimSpace(m[1])
		}
	}
	var run []string    // the capitalised words so far
	runAtStart := false // whether run began a sentence
	sentenceStart := true
	// flush returns the run as a name when it counts as one, and empties it.
	flush := func() string {
		name := ""
		if len(run) >= 2 || (len(run) == 1 && utf8.RuneCountInString(run[0]) >= 3 && (!runAtStart || productLike(run[0]))) {
			name = strings.Join(run, " ")
		}
		run = nil
		return name
	}
	for _, tok := range strings.Fields(question) {
		core := coreWord(tok)
		switch {
		case core == "" || stopName(core):
			if name := flush(); name != "" {
				return name
			}
		case capitalised(core):
			if len(run) == 0 {
				runAtStart = sentenceStart
			}
			run = append(run, core)
		case productLike(core):
			// A lower-case product word such as "iPhone" or "qwen3" stands
			// alone: it ends any run before it.
			if name := flush(); name != "" {
				return name
			}
			return core
		default:
			if name := flush(); name != "" {
				return name
			}
		}
		// A mark at the end of a word ends a run, and . ! ? : end the
		// sentence as well. A closing quote or bracket comes off first, so
		// "Flow)." still ends a sentence. DecodeLastRuneInString returns
		// the last character, which may take more than one byte.
		closed, _ := utf8.DecodeLastRuneInString(tok)
		last, _ := utf8.DecodeLastRuneInString(strings.TrimRight(tok, `"'”’)]`))
		sentenceStart = strings.ContainsRune(".!?:", last)
		if strings.ContainsRune(".,;:!?", last) || strings.ContainsRune(`"')]”’`, closed) {
			if name := flush(); name != "" {
				return name
			}
		}
	}
	return flush()
}

// coreWord returns tok without the marks around it and without an
// apostrophe and what follows it, so "(Quick's" gives "Quick" and "I'm"
// gives "I". It returns "" for a token with no letter or digit.
func coreWord(tok string) string {
	w := strings.TrimFunc(tok, notWordRune)
	if i := strings.IndexAny(w, "'’"); i >= 0 {
		w = w[:i]
	}
	return w
}

// notWordRune reports whether r can't be part of a word: anything but a
// letter or a digit. Callers trim only the ends of a token with it, so the
// hyphen in "GPT-5" and the dot in "qwen3.6" stay.
func notWordRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// capitalised reports whether w starts with a capital letter.
func capitalised(w string) bool {
	r, _ := utf8.DecodeRuneInString(w)
	return unicode.IsUpper(r)
}

// productLike reports whether w is shaped like a product name: at least
// three characters with a capital after the first letter and a small
// letter somewhere ("QuickSight", "iOS"), or at least four with both
// letters and digits ("qwen3", "GPT-5"). "Q3" and "MP3" are too short to
// count.
func productLike(w string) bool {
	rs := []rune(w)
	var innerUpper, lower, letter, digit bool
	for i, r := range rs {
		switch {
		case unicode.IsUpper(r):
			letter = true
			if i > 0 {
				innerUpper = true
			}
		case unicode.IsLower(r):
			letter, lower = true, true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return (len(rs) >= 3 && innerUpper && lower) || (len(rs) >= 4 && letter && digit)
}

// stopName reports whether w, a capitalised word, never names a thing on
// its own: "I", Meru's own name, the months and days, greetings, and the
// short words a title capitalises. The list is short on purpose; a word
// missing from it costs one web search whose results the model can set
// aside.
func stopName(w string) bool {
	switch strings.ToLower(w) {
	case "i", "meru", "ai", "ok", "okay", "pdf", "url", "api", "faq",
		"january", "february", "march", "april", "may", "june", "july", "august",
		"september", "october", "november", "december",
		"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
		"today", "tomorrow", "yesterday",
		"hi", "hello", "hey", "thanks", "thank", "please", "yes", "no",
		"what", "who", "when", "where", "why", "how", "which", "can", "could", "would",
		"should", "will", "is", "are", "was", "do", "does", "did", "tell", "show",
		"the", "a", "an", "and", "or", "of", "for", "in", "on", "to", "with", "about",
		"my", "your", "our", "me", "you", "we", "it", "this", "that":
		return true
	}
	return false
}

// aboutTheUser reports whether question says "my", "mine", "our" or
// "ours". Such a question is about the user's own life, such as "when is
// my Lisbon trip?", and a name in it goes to no search engine: the
// user's files or tools hold the answer, or nothing does.
func aboutTheUser(question string) bool {
	for _, w := range words(question) {
		switch w {
		case "my", "mine", "our", "ours":
			return true
		}
	}
	return false
}

// filesCover reports whether results, the excerpts a search of the user's
// files found, best first, cover name. They do when the best excerpt
// scores above weakScore and at least one excerpt's text, heading or path
// holds the name, in any case. The keyword search matches any word of the
// question, "what" and "is" included, so a strong score alone doesn't
// show that the files know the name.
func filesCover(name string, results []retrieve.Result) bool {
	if len(results) == 0 || results[0].Score <= weakScore {
		return false
	}
	want := strings.ToLower(name)
	for _, r := range results {
		if strings.Contains(strings.ToLower(r.Text+"\n"+r.Heading+"\n"+r.Path), want) {
			return true
		}
	}
	return false
}

// webCall is one call the web-first step makes: the tool and its
// arguments.
type webCall struct {
	tool string
	args map[string]any
}

// askedCalls returns the calls for a question that asked for the web: a
// web_fetch for each URL in it, when web_fetch is on, or else one
// web_search on webQuery, when web_search is on. all is every tool config
// allows. It returns nil when the tool the question needs is off.
func askedCalls(question string, history []engine.Message, all []engine.ToolSpec) []webCall {
	if urls := webURLs(question); len(urls) > 0 {
		if !offersTool(all, builtin.WebFetch) {
			return nil
		}
		calls := make([]webCall, len(urls))
		for i, u := range urls {
			calls[i] = webCall{tool: builtin.WebFetch, args: map[string]any{"url": u}}
		}
		return calls
	}
	if !offersWebSearch(all) {
		return nil
	}
	return []webCall{{tool: builtin.WebSearch, args: map[string]any{"query": webQuery(question, history)}}}
}

// namedCalls returns the call for a named thing the user's files don't
// cover: one web_search on namedQuery. It returns nil when web_search is
// off.
func namedCalls(name, question string, all []engine.ToolSpec) []webCall {
	if !offersWebSearch(all) {
		return nil
	}
	return []webCall{{tool: builtin.WebSearch, args: map[string]any{"query": namedQuery(name, question)}}}
}

// askedWebFirst reports whether question, in auto scope, asks for the web
// (see asksForWeb) or gives a URL (see givesURL) while the tool it needs is
// on. A question that asks for the web while web_search is off, or gives a
// URL while web_fetch is off, gets a debug line and no web-first step.
func (a *Agent) askedWebFirst(ctx context.Context, question string) bool {
	if a.tools == nil {
		return false
	}
	all := a.tools.Tools()
	if len(webURLs(question)) > 0 {
		if givesURL(question, all) {
			return true
		}
		a.log.DebugContext(ctx, "no web first: the question gives a URL, and web_fetch is off")
		return false
	}
	if _, _, ok := askPhrase(words(question)); !ok {
		return false
	}
	if asksForWeb(question, all) {
		return true
	}
	a.log.DebugContext(ctx, "no web first: the question asks for the web, and web_search is off")
	return false
}

// webFirstOf returns why t went to the web first, or webFirstNone.
func webFirstOf(t *turn) string {
	if t.webFirst == "" {
		return webFirstNone
	}
	return t.webFirst
}

// namedWebFirst reports whether a turn in auto scope goes to the web for a
// named thing, and returns the name. Every test must pass:
//
//   - the turn searched the user's files first (fileTurn, with a Searcher
//     and "auto" retrieval), so it isn't a direct question and the route
//     didn't point at a connected tool;
//   - no connected tool is the question's target (target is ""): a mail,
//     calendar or notes question stays with that tool;
//   - web_search is on;
//   - the question isn't about the user's own life (see aboutTheUser);
//   - it names a thing (see namedThing), and the excerpts don't cover it
//     (see filesCover).
func (a *Agent) namedWebFirst(question, target string, searched bool, results []retrieve.Result) (string, bool) {
	if !searched || target != "" || a.tools == nil || !offersWebSearch(a.tools.Tools()) || aboutTheUser(question) {
		return "", false
	}
	name := namedThing(question)
	if name == "" || filesCover(name, results) {
		return "", false
	}
	return name, true
}

// runWebFirst makes calls through dispatch, as merud's own calls (see
// dispatch.CallerMeru), before the model's first round, and returns the
// "From the web" section built from the ones that succeeded, or "" when
// none did. The calls go through runTools, so each gets its tool_call and
// tool_result events, transcript lines and tool_calls row like a call the
// model made, and its results join the turn's web notes.
//
// A call that fails, such as a web_search while SearXNG is down, is left
// out of the section with a debug line: the model still has the web tools
// and can try again. It fails only when emit fails or ctx ends.
func (a *Agent) runWebFirst(ctx context.Context, t *turn, calls []webCall) (string, error) {
	if len(calls) == 0 {
		return "", nil
	}
	tcs := make([]engine.ToolCall, len(calls))
	for i, c := range calls {
		// Marshal of a map of strings can't fail.
		args, _ := json.Marshal(c.args)
		tcs[i] = engine.ToolCall{Name: c.tool, Arguments: args}
	}
	ran, err := a.runCalls(ctx, t, tcs, dispatch.CallerMeru)
	if err != nil {
		return "", err
	}
	var parts []string
	for i, r := range ran {
		if r.outcome != dispatch.OutcomeOK || strings.TrimSpace(r.msg.Content) == "" {
			a.log.DebugContext(ctx, "web-first call gave nothing; the model can search itself",
				"tool", calls[i].tool, "outcome", r.outcome)
			continue
		}
		text := r.msg.Content
		if calls[i].tool == builtin.WebFetch {
			text = fmt.Sprintf("[%d] %s", len(parts)+1, text)
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "", nil
	}
	// Each part gets an equal share of the section, so a long first page
	// doesn't crowd out the second.
	share := maxWebChars / len(parts)
	for i, p := range parts {
		if utf8.RuneCountInString(p) > share {
			parts[i] = cutRunes(p, share) + "\n[Meru cut this here. Call web_fetch for the rest.]"
		}
	}
	section := webCiteRule + "\n\nFrom the web\n\n" + strings.Join(parts, "\n\n")
	obs.RecordContextTokens(ctx, "web", utf8.RuneCountInString(section)/4)
	return section, nil
}

// cutRunes cuts s to at most n characters, never splitting a character.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
