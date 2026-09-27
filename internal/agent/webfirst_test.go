// This file tests the pieces of the web-first step that need no turn: the
// phrases that ask for the web, the URLs in a question, the search words
// merud sends, the detector for named things, the check on whether the
// user's files cover a name, and the web notes a turn keeps.

package agent

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/transcript"
)

func TestAsksForWeb(t *testing.T) {
	web := []engine.ToolSpec{spec("web_search")}
	tests := []struct {
		question string
		specs    []engine.ToolSpec
		want     bool
	}{
		{"Search the web: what is SearXNG?", web, true},
		{"look it up online", web, true},
		{"what does the internet say about Go 1.27", web, true},
		{"do a web search about quick and educate yourself", web, true},
		{"can you look up the Acme Flow release date", web, true},
		{"look this up for me: Acme Flow", web, true},
		{"google it", web, true},
		{"do some deep research about Acme Flow", web, true},
		{"research about the Lisbon trip weather", web, true},
		{"find out online when Acme Flow shipped", web, true},
		{"what is the capital of France", web, false},
		{"search the web for it", nil, false}, // no web_search configured
		{"my website is down", web, false},    // "website" isn't "web"
		{"summarise my research folder", web, false},
		{"look at my notes", web, false},
		{"check my google calendar", web, false}, // "google" alone is the mail server's name
	}
	for _, tt := range tests {
		if got := asksForWeb(tt.question, tt.specs); got != tt.want {
			t.Errorf("asksForWeb(%q) = %v, want %v", tt.question, got, tt.want)
		}
	}
}

func TestWebURLs(t *testing.T) {
	tests := []struct {
		question string
		want     []string
	}{
		{"what is at https://acme.example/flow?", []string{"https://acme.example/flow"}},
		{"read https://a.example/x, then http://b.example/y.", []string{"https://a.example/x", "http://b.example/y"}},
		{"(see https://a.example/x) and https://a.example/x again", []string{"https://a.example/x"}},
		{"three: https://a.example https://b.example https://c.example", []string{"https://a.example", "https://b.example"}},
		{"no address here, just acme.example", nil},
		{"ftp://files.example isn't the web", nil},
	}
	for _, tt := range tests {
		if got := webURLs(tt.question); !slices.Equal(got, tt.want) {
			t.Errorf("webURLs(%q) = %q, want %q", tt.question, got, tt.want)
		}
	}
}

func TestWebQuery(t *testing.T) {
	earlier := []engine.Message{
		{Role: engine.RoleUser, Content: "what does Acme Flow do with shared notes"},
		{Role: engine.RoleAssistant, Content: "It copies them."},
	}
	song := []engine.Message{
		{Role: engine.RoleUser, Content: "what does the song maname maname sung by r. devi acvtually mean"},
		{Role: engine.RoleAssistant, Content: "I don't have access to web search."},
	}
	tests := []struct {
		name     string
		question string
		history  []engine.Message
		want     string
	}{
		{"phrase and filler go", "can you search the web for Acme Flow pricing?", nil, "Acme Flow pricing"},
		{"a colon after the phrase", "Search the web: what is SearXNG?", nil, "SearXNG"},
		{"look it up", "Acme Flow release date, look it up", nil, "Acme Flow release date"},
		{"the URL goes", "search the web for https://acme.example/flow reviews", nil, "reviews"},
		{"a follow-up borrows the earlier question", "do a web search about quick", earlier,
			"quick what does Acme Flow do with shared notes"},
		{"a question that stands alone keeps to itself", "search the web for Acme Flow team plans", earlier,
			"Acme Flow team plans"},
		{"only the phrase", "search the web", nil, "search the web"},
		{"a real follow-up still borrows", "search the web for the pricing", earlier,
			"pricing what does Acme Flow do with shared notes"},
		// A follow-up that speaks only of the web searches for the earlier
		// question alone. The first case replays the real turn, typos and
		// all, with an invented song.
		{"a follow-up about the web alone", "you have accerss to web search", song,
			"song maname maname sung by r. devi acvtually mean"},
		{"look it up alone", "look it up", song, "song maname maname sung by r. devi acvtually mean"},
		{"can you look it up", "ok, can you look it up online?", song,
			"song maname maname sung by r. devi acvtually mean"},
		{"two follow-ups in a row", "search the web", append(slices.Clone(song),
			engine.Message{Role: engine.RoleUser, Content: "you have access to web search"},
			engine.Message{Role: engine.RoleAssistant, Content: "I can't search."}),
			"song maname maname sung by r. devi acvtually mean"},
		// A closing instruction means nothing to a search engine.
		{"and tell me", "contoso relay and tell me what it is in three lines", nil, "contoso relay"},
		{"both shapes", "search the web for Acme Flow pricing and tell me what it costs in three lines", nil,
			"Acme Flow pricing"},
		{"a length at the end", "search the web for Acme Flow pricing in three lines", nil, "Acme Flow pricing"},
		{"then explain", "look up Acme Flow team plans, then explain them simply", nil, "Acme Flow team plans"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webQuery(tt.question, tt.history); got != tt.want {
				t.Errorf("webQuery(%q) = %q, want %q", tt.question, got, tt.want)
			}
		})
	}
}

func TestWebQueryCap(t *testing.T) {
	q := "search the web for " + strings.Repeat("acme ", 200)
	if got := utf8.RuneCountInString(webQuery(q, nil)); got > maxWebQuery {
		t.Errorf("query has %d characters, want at most %d", got, maxWebQuery)
	}
}

func TestNamedThing(t *testing.T) {
	tests := []struct {
		question string
		want     string
	}{
		// Runs of capitalised words.
		{"tell me about Contoso Relay", "Contoso Relay"},
		{"How does Acme Flow compare with Meru?", "Acme Flow"},
		{"What Is Acme Flow", "Acme Flow"}, // a title's question words don't count
		{"Acme Flow is new, what does it do?", "Acme Flow"},
		{"is Acme any good?", "Acme"},
		{"we met Priya Shah at the fair", "Priya Shah"},
		// A single capitalised word that starts a sentence doesn't count.
		{"Kubernetes is hard to learn", ""},
		{"Hello. Kubernetes is hard.", ""},
		{"Thanks! Where is Acme Flow hosted?", "Acme Flow"},
		// Words in the stop list never count, and split a run.
		{"I think I need a new laptop", ""},
		{"I'm planning a trip in May", ""},
		{"what happened on Monday in September?", ""},
		{"hey Meru, how are you?", ""},
		{"can you explain AI to me", ""},
		{"Can You Tell Me About It", ""},
		// Quoted terms, straight or curly, win over the rest.
		{`what is "acme flow"?`, "acme flow"},
		{"what is “acme flow” for Big Teams?", "acme flow"},
		// A quoted sentence is a message, not a name.
		{`rewrite this to sound less angry: "you never reply to my messages on time"`, ""},
		{`translate "where is the train station" into Portuguese`, "Portuguese"},
		// Words shaped like product names count anywhere.
		{"GitHub is down again", "GitHub"},
		{"should I buy an iPhone?", "iPhone"},
		{"how fast is qwen3.6 on a laptop", "qwen3.6"},
		{"the Q3 plan is late", ""}, // too short to count
		{"is Go fast?", ""},         // a lone word needs three characters
		{"is Contoso Cloud down?", "Contoso Cloud"},
		// Plain questions name nothing.
		{"what is the capital of france", ""},
		{"how do I bake bread?", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := namedThing(tt.question); got != tt.want {
			t.Errorf("namedThing(%q) = %q, want %q", tt.question, got, tt.want)
		}
	}
}

func TestNamedQuery(t *testing.T) {
	tests := []struct {
		name, question, want string
	}{
		{"Acme Flow", "How does Acme Flow compare with Meru?", `"Acme Flow" compare meru`},
		{"Acme Flow", "what is Acme Flow", `"Acme Flow"`},
		{"Acme", "is Acme good for large teams with many shared boards and projects", `"Acme" good large teams shared boards`},
	}
	for _, tt := range tests {
		if got := namedQuery(tt.name, tt.question); got != tt.want {
			t.Errorf("namedQuery(%q, %q) = %q, want %q", tt.name, tt.question, got, tt.want)
		}
	}
}

func TestAboutTheUser(t *testing.T) {
	tests := []struct {
		question string
		want     bool
	}{
		{"when is my Lisbon trip?", true},
		{"is the Lisbon flat ours for the week?", true},
		{"what is Acme Flow", false},
		{"tell me about Contoso Relay", false},
		{"myopia and Acme Lenses", false}, // "myopia" isn't "my"
	}
	for _, tt := range tests {
		if got := aboutTheUser(tt.question); got != tt.want {
			t.Errorf("aboutTheUser(%q) = %v, want %v", tt.question, got, tt.want)
		}
	}
}

// TestWeakScore checks the threshold against the fusion it comes from: the
// top chunk of one list scores weakScore, and a chunk both lists
// found at their last place still beats it.
func TestWeakScore(t *testing.T) {
	oneList := 1.0 / (60 + 1)
	bothLast := 2.0 / (60 + 50)
	if oneList > weakScore {
		t.Errorf("top of one list = %v, above weakScore %v", oneList, weakScore)
	}
	if bothLast <= weakScore {
		t.Errorf("last of both lists = %v, not above weakScore %v", bothLast, weakScore)
	}
}

func TestFilesCover(t *testing.T) {
	strong := 2.0 / 61 // first in both lists
	weak := 1.0 / 61   // first in one list only
	tests := []struct {
		name    string
		results []retrieve.Result
		want    bool
	}{
		{"nothing found", nil, false},
		{"a weak score, even with the name", []retrieve.Result{
			result("/n/acme.md", "", "Acme Flow notes", 1, 2, weak)}, false},
		{"a strong score without the name", []retrieve.Result{
			result("/n/garden.md", "", "What is the plan for the garden?", 1, 2, strong)}, false},
		{"a strong score with the name in the text", []retrieve.Result{
			result("/n/garden.md", "", "Garden plan", 1, 2, strong),
			result("/n/tools.md", "", "We moved our notes to acme flow in May.", 3, 4, strong/2)}, true},
		{"the name in a heading", []retrieve.Result{
			result("/n/tools.md", "Acme Flow", "Setup steps.", 1, 2, strong)}, true},
		{"the name in the path", []retrieve.Result{
			result("/n/acme flow/setup.md", "", "Setup steps.", 1, 2, strong)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filesCover("Acme Flow", tt.results); got != tt.want {
				t.Errorf("filesCover = %v, want %v", got, tt.want)
			}
		})
	}
}

// searchText is a web_search result as builtin.formatResults writes it.
const searchText = `Web results for "\"Acme Flow\"", 3 of 9. Cite each result you use by its URL.

[1] Acme Flow — https://acme.example/flow
Acme Flow moves notes between apps.
Published 2026-09-01.

[2] Acme Flow — pricing — https://acme.example/flow/pricing
Plans for small and large teams.

[3] (no title) — https://blog.example/acme
`

// pageText is a web_fetch result as builtin.webFetch writes it.
const pageText = "https://acme.example/flow\nTitle: Acme Flow\n4.1 KB, HTML. Characters 0 to 57 of 57.\n\n" +
	"Acme Flow is a service that moves notes between apps."

func TestWebHitsOf(t *testing.T) {
	tests := []struct {
		name, tool, text string
		want             []webHit
	}{
		{"search results", "web_search", searchText, []webHit{
			{note: transcript.WebNote{URL: "https://acme.example/flow", Title: "Acme Flow", Gist: "Acme Flow moves notes between apps."}},
			{note: transcript.WebNote{URL: "https://acme.example/flow/pricing", Title: "Acme Flow — pricing", Gist: "Plans for small and large teams."}},
			{note: transcript.WebNote{URL: "https://blog.example/acme", Title: "(no title)"}},
		}},
		{"a page's text", "web_fetch", pageText, []webHit{{page: true, note: transcript.WebNote{
			URL: "https://acme.example/flow", Title: "Acme Flow", Gist: "Acme Flow is a service that moves notes between apps."}}}},
		{"an answer to a prompt", "web_fetch", "From https://acme.example/flow (fetched 2026-09-27): It moves notes.",
			[]webHit{{page: true, note: transcript.WebNote{URL: "https://acme.example/flow", Gist: "It moves notes."}}}},
		{"a download", "web_fetch", "Saved acme.pdf in ~/meru-output/downloads.", nil},
		{"no results", "web_search", `SearXNG found no results for "acme". Try other words.`, nil},
		{"another tool", "read_file", pageText, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webHitsOf(tt.tool, tt.text); !slices.Equal(got, tt.want) {
				t.Errorf("webHitsOf =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestKeptNotes(t *testing.T) {
	result := func(url string) webHit {
		return webHit{note: transcript.WebNote{URL: url, Title: "r", Gist: "snippet"}}
	}
	page := func(url string) webHit {
		return webHit{page: true, note: transcript.WebNote{URL: url, Title: "p", Gist: "page text"}}
	}
	long := func(url string) webHit {
		return webHit{page: true, note: transcript.WebNote{URL: url, Gist: strings.Repeat("x", maxGistChars)}}
	}
	urls := func(notes []transcript.WebNote) []string {
		var out []string
		for _, n := range notes {
			out = append(out, n.URL)
		}
		return out
	}
	tests := []struct {
		name string
		hits []webHit
		want []string
	}{
		{"none", nil, nil},
		{"pages come first", []webHit{result("r1"), page("p1"), result("r2"), page("p2")}, []string{"p1", "p2", "r1", "r2"}},
		{"at most five", []webHit{result("r1"), result("r2"), result("r3"), result("r4"), result("r5"), result("r6")},
			[]string{"r1", "r2", "r3", "r4", "r5"}},
		{"each URL once", []webHit{result("r1"), page("r1"), result("r2")}, []string{"r1", "r2"}},
		{"the character cap", []webHit{long("a"), long("b"), long("c"), long("d"), long("e"), long("f")},
			[]string{"a", "b", "c", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := urls(keptNotes(tt.hits)); !slices.Equal(got, tt.want) {
				t.Errorf("keptNotes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOrList(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"web_search"}, "web_search"},
		{[]string{"web_search", "web_fetch"}, "web_search or web_fetch"},
		{[]string{"a", "b", "c"}, "a, b or c"},
	}
	for _, tt := range tests {
		if got := orList(tt.names); got != tt.want {
			t.Errorf("orList(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}
