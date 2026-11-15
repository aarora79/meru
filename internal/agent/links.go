// This file holds the check on web links in an answer: a link that no
// search result, fetched page, tool result or message of the user's in the
// chat holds may be one the model made up. See honest.go for the other
// checks that run after an answer, and ARCHITECTURE.md, "Claims no tool
// backs".

package agent

import (
	"regexp"
	"strings"

	"github.com/aarora79/meru/internal/transcript"
)

// maxLinksNamed is how many unsourced links the notice lists; past that it
// says how many more there are.
const maxLinksNamed = 3

// linkPattern finds an http or https link in text: in prose, in Markdown's
// [text](url), or in an HTML attribute such as href="url". It stops at
// spaces, quotes, angle brackets, brackets and parentheses.
var linkPattern = regexp.MustCompile(`https?://[^\s"'<>()\[\]` + "`" + `|\\]+`)

// linkKey returns the form of link the check compares: the scheme and host
// in lower case, the fragment and any trailing punctuation or slash
// dropped. "https://Rekhta.org/ghazals/x/." and "https://rekhta.org/ghazals/x"
// match, and "...x" and "...x-1" don't.
func linkKey(link string) string {
	link = strings.TrimRight(link, ".,;:!?*_'\"")
	if i := strings.IndexByte(link, '#'); i >= 0 {
		link = link[:i]
	}
	link = strings.TrimRight(link, "/")
	scheme, rest, ok := strings.Cut(link, "://")
	if !ok {
		return link
	}
	host, path, _ := strings.Cut(rest, "/")
	key := strings.ToLower(scheme) + "://" + strings.ToLower(host)
	if path != "" {
		key += "/" + path
	}
	return key
}

// linksIn returns the set of links texts hold, by linkKey.
func linksIn(texts ...string) map[string]bool {
	out := map[string]bool{}
	for _, text := range texts {
		for _, l := range linkPattern.FindAllString(text, -1) {
			out[linkKey(l)] = true
		}
	}
	return out
}

// chatLinks returns the links lines, a chat's transcript, already showed
// the model or the user wrote: every tool result and every user line. A
// link an earlier turn's search found counts, so the answer may repeat it.
// Earlier answers don't count: a link the model made up once stays made up.
func chatLinks(lines []transcript.Line) map[string]bool {
	var texts []string
	for _, l := range lines {
		switch l.Type {
		case transcript.TypeToolResult:
			texts = append(texts, l.Result)
		case transcript.TypeUser:
			texts = append(texts, l.Text)
		}
	}
	return linksIn(texts...)
}

// unsourcedLinks returns the links in answer, as the answer writes them,
// that neither known nor read holds, each once. known is the chat's links
// from earlier turns (chatLinks); read is what this turn showed the model
// (turn.read). Links in code count too: a page the model writes into a
// file carries its links to the user all the same.
//
// A real chat showed why. Asked for a ghazal on a poetry site, a model
// called no tool and gave a link built from the poem's words. It had the
// site's shape, and opened the site's list of ghazals, not the poem.
// Three more made-up links followed, and one went into a page of links the
// model saved for the user.
func unsourcedLinks(answer string, known map[string]bool, read []string) []string {
	readLinks := linksIn(read...)
	seen := map[string]bool{}
	var out []string
	for _, l := range linkPattern.FindAllString(answer, -1) {
		k := linkKey(l)
		if known[k] || readLinks[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimRight(l, ".,;:!?*_'\""))
	}
	return out
}

// linksNotice returns the warning a client shows under an answer with
// unsourced links, or "" for none. It names up to maxLinksNamed of them.
func linksNotice(links []string) string {
	if len(links) == 0 {
		return ""
	}
	named := links[:min(len(links), maxLinksNamed)]
	s := "No search, page or message in this chat gave " + joinLinks(named)
	if more := len(links) - len(named); more > 0 {
		s += " and " + plural(more, "more link")
	}
	if len(links) == 1 {
		return s + ", so the model may have made it up. Check it before you use it."
	}
	return s + ", so the model may have made them up. Check them before you use them."
}

// joinLinks writes links as "a", "a and b", or "a, b and c".
func joinLinks(links []string) string {
	switch len(links) {
	case 1:
		return links[0]
	case 2:
		return links[0] + " and " + links[1]
	}
	return strings.Join(links[:len(links)-1], ", ") + " and " + links[len(links)-1]
}
