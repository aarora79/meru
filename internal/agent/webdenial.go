// This file holds the check on an answer that says Meru has no web tools
// while the turn offers them, and the one nudge that follows. A real chat
// showed why. web_search had just returned good results, and the model
// wrote "I don't have a search tool I can reach from here". Later, holding
// a link to the site's top stories, it wrote "I don't have a tool that can
// fetch the live site". The prompt's rule against this didn't stop a small
// model. See ARCHITECTURE.md, "Claims no tool backs".

package agent

import (
	"regexp"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
)

// webDenialNudge is the message a turn adds, once, after a round that
// denied having web tools. Like planNudge, it lives in this turn's messages
// only; the transcript never records it.
const webDenialNudge = "You do have web tools in this turn: web_search finds pages, and web_fetch opens one, " +
	"like a browser. Use them now for what the user asked, or answer from the results you already have. " +
	"Don't say you lack them."

// webDenial matches a sentence that says the model has no web tool. (?i)
// ignores case. It has three shapes:
//
//   - no tool or access: "I don't have a search tool", "I have no browser
//     access", "I don't have an `abrowser` tool", "I don't have access to
//     the web";
//   - no tool that does the job: "I don't have a tool that can fetch";
//   - can't do the job: "I can't browse sites", "I'm unable to search the
//     web".
//
// "I don't have search results" doesn't match: it says what a search found,
// not what Meru can do.
var webDenial = regexp.MustCompile("(?i)\\bI\\s+(?:(?:don['’]t|do\\s+not)\\s+have|have\\s+no)\\s+(?:" +
	"access\\s+to\\s+(?:the\\s+)?(?:web|internet|a\\s+browser)\\b" +
	"|(?:(?:a|an|any)\\s+)?`?(?:\\w+\\s+)?\\w*(?:browser|browsing|web|internet|search|fetch)\\w*`?\\s+(?:tool|tools|capability|capabilities|access)\\b" +
	"|(?:a|an|any)\\s+tool\\s+(?:that|which|to)\\s+(?:can\\s+)?(?:fetch|browse|open|search\\s+the\\s+web)\\b" +
	")|\\bI(?:['’]m|\\s+am)?\\s+(?:can['’]t|cannot|not\\s+able\\s+to|unable\\s+to)\\s+(?:" +
	"browse\\b|search\\s+(?:the\\s+web|online|the\\s+internet)\\b|access\\s+(?:the\\s+)?(?:web|internet)\\b" +
	"|fetch\\s+(?:the\\s+|web\\s+|live\\s+)?(?:site|page|pages|web)\\b|open\\s+(?:web\\s+)?pages?\\b)")

// deniesWebTools reports whether text says the model has no web tool.
func deniesWebTools(text string) bool {
	return webDenial.MatchString(withoutCode(text))
}

// nudgesWebDenial says whether converse should tell the model it has web
// tools: the round offered both web_search and web_fetch and the model
// called none, its text denies having them, the turn has a round left and no
// nudge yet, and no web call of this turn failed. After a failed call, "I
// can't read that page" is true, and the user should read it. Both tools
// must be on offer because the nudge names both: while SearXNG is down the
// turn has web_fetch alone, and "I can't search the web" is true.
func (a *Agent) nudgesWebDenial(t *turn, rep reply, offer []engine.ToolSpec) bool {
	if len(rep.calls) > 0 || t.webDenied || t.webFailed || t.rounds >= a.maxRounds {
		return false
	}
	return offersTool(offer, builtin.WebSearch) && offersTool(offer, builtin.WebFetch) &&
		deniesWebTools(rep.text)
}
