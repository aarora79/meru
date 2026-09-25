// This file holds the route rule for connected tools: toolTarget, which
// spots a question that points at one, and the part of it that works out
// what connected tools act on. A question about "my last email" needs the
// gmail tools even when it names no server and the router picks search.

package agent

import (
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
)

// toolTarget returns what in question points at one of the tools in
// specs, as words for a log line, or "" when nothing does. Handle uses it
// twice: to add tools to a route that lacks them, and to skip the search
// before the answer on a "tools" turn (see aboutFiles). It checks four
// signs, in order, each added after the router missed one:
//
//   - "names a tool server": "search my obsidian vault" routed to search,
//     which offers only the file tools.
//   - "asks Meru to remember": "remember that my name is Dana" routed
//     direct, and the model said it would remember and saved nothing.
//   - "asks for the web": "Search the web: what is SearXNG?" routed direct,
//     and the model, with no tools, wrote a tool call as plain text.
//   - "names what a tool handles": "what was the last email I sent?" routed
//     to search, and the model grepped the user's files.
//
// Each sign compares the question's words with names Meru already holds,
// so the rule stays easy to explain and to test. A wrong guess ("do you
// remember the trip?") offers tools the model need not call, and skips a
// search the model can still run itself with search_files.
func toolTarget(question string, specs []engine.ToolSpec) string {
	switch {
	case namesFolder(question, toolServers(specs)):
		return "names a tool server"
	case asksToRemember(question, specs):
		return "asks Meru to remember"
	case asksForWeb(question, specs):
		return "asks for the web"
	case asksAboutToolNoun(question, specs):
		return "names what a tool handles"
	}
	return ""
}

// toolNouns returns the nouns in the names of the MCP and A2A tools in
// specs, singular and lower case: "gmail" and "message" from
// "google.search_gmail_messages", "calendar" from "google.list_calendars".
// It skips built-in tools, whose names are Meru's own, and the words in
// genericToolWord, which name what nearly every tool does or touches.
func toolNouns(specs []engine.ToolSpec) []string {
	var nouns []string
	for _, t := range specs {
		kind := toolKind(strings.ToLower(t.Name))
		if kind != dispatch.KindMCP && kind != dispatch.KindA2A {
			continue
		}
		// The tool's own name is the part after the server: "search_gmail_messages".
		_, tool, _ := strings.Cut(strings.ToLower(t.Name), ".")
		if kind == dispatch.KindA2A {
			_, tool, _ = strings.Cut(tool, ".")
		}
		for _, w := range strings.FieldsFunc(tool, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
			w = singular(w)
			if len(w) >= 4 && !genericToolWord(w) && !slices.Contains(nouns, w) {
				nouns = append(nouns, w)
			}
		}
	}
	return nouns
}

// genericToolWord reports whether w is a word too common in tool names to
// say what a tool is about: the verbs tools share, and nouns such as
// "file" that would pull a server's tools into every question about the
// user's files. The list is short; a word missing from it costs a prompt
// that holds some tool schemas, and the model need not call any.
func genericToolWord(w string) bool {
	switch w {
	case "search", "list", "read", "write", "create", "update", "delete", "manage",
		"send", "import", "export", "fetch", "query", "find", "open", "close",
		"file", "folder", "content", "link", "shareable", "download", "upload",
		"batch", "item", "info", "detail", "simple", "into", "from",
		"with", "google", "obsidian", "append":
		return true
	}
	return false
}

// singular drops a plain English plural "s", so "messages" and "message"
// compare equal. "address" and "class" keep their "s".
func singular(w string) string {
	if len(w) > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		return w[:len(w)-1]
	}
	return w
}

// asksAboutToolNoun reports whether a word in question matches a noun in
// the connected tools' names. A word matches a noun when, both singular,
// they are equal or end in the same four or more letters: "calendar"
// matches "calendars", "email" matches "gmail" (both end in "mail").
func asksAboutToolNoun(question string, specs []engine.ToolSpec) bool {
	nouns := toolNouns(specs)
	if len(nouns) == 0 {
		return false
	}
	for _, w := range words(question) {
		w = singular(w)
		if len(w) < 4 {
			continue
		}
		for _, n := range nouns {
			if w == n || sharedSuffix(w, n) >= 4 {
				return true
			}
		}
	}
	return false
}

// sharedSuffix returns how many letters a and b share at their ends.
func sharedSuffix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}
