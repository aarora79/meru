// This file holds the route rule for what connected tools act on: a
// question about "my last email" needs the gmail tools even when it names
// no server and the router picks search.

package agent

import (
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
)

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
