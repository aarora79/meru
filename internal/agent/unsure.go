// This file holds the rule for a follow-up the router is unsure of: it
// keeps the route the chat's last turn took, in place of the fallback.
// See ARCHITECTURE.md, "Routing".

package agent

import "github.com/aarora79/meru/internal/transcript"

// lastRoute returns the route of the last answered turn in lines, a
// chat's transcript, or "" when no turn has answered yet.
func lastRoute(lines []transcript.Line) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Type == transcript.TypeAssistant && lines[i].Route != "" {
			return lines[i].Route
		}
	}
	return ""
}

// keepRoute returns the route for a turn the router decided as dec, where
// last is the chat's last route (see lastRoute). When the router was
// unsure and the chat has an earlier turn, the turn keeps that turn's
// route; otherwise it keeps dec.Route, the fallback included.
//
// Most questions the router is unsure of are follow-ups: "done?",
// "Continue", "thanks, that helps", "what tools did you use". The
// fallback, search+tools by default, sent each one to the full tool list.
// Two things made that slow. The tool schemas run to about 30,000 tokens
// with a few servers connected, and Ollama keeps one prompt in its cache.
// After a direct turn, whose prompt offers a few tools, the cache shares
// almost nothing with a search+tools prompt, so a real "great job Meru!"
// after a direct turn read 39,844 tokens afresh and took 45 seconds before
// its first word. Keeping the last route keeps the prompt's front the same
// as the cached one. The rules after the router still apply, so a
// follow-up that names a connected tool, such as "read the email she just
// sent", still gets the tools.
func keepRoute(dec Decision, last string) string {
	if dec.Outcome == "low_confidence" && last != "" {
		return last
	}
	return dec.Route
}
