// This file holds the router's prompt and the letter-to-route table. They sit
// together so they can't drift apart: the letters are part of the prompt, and
// config never sets them (docs/fast-router.md, "The prompt contract").

package router

import (
	"strings"

	"github.com/aarora79/meru/internal/engine"
)

// option is one lettered choice in the prompt.
type option struct {
	letter      string // the single letter the model answers with
	route       Route
	description string // what the prompt tells the model this choice means
}

// options lists the four choices in prompt order. Small models lean on the
// description text, so each one says when to pick it.
//
// A [...]T{...} literal is an array whose length the compiler counts.
var options = [...]option{
	{"A", RouteDirect, "Answer from what you already know. No files or tools needed."},
	{"B", RouteSearch, "The answer is in the user's own notes, documents or repositories. Retrieve first."},
	{"C", RouteTools, "The answer needs a tool: live data, an external service, or an action."},
	{"D", RouteSearchTools, "Both: retrieve from the user's files and call a tool."},
}

// routeForLetter maps a token from the model to a route. It trims spaces
// and ignores case, because a tokenizer may emit "A", " A" or "a" for the
// same answer. Anything that isn't exactly one of the four letters after
// trimming, such as "AB", "Alpha" or "1", maps to nothing, and ok is false.
func routeForLetter(token string) (r Route, ok bool) {
	t := strings.TrimSpace(token)
	for _, o := range options {
		if strings.EqualFold(t, o.letter) {
			return o.route, true
		}
	}
	return "", false
}

// buildMessages turns a turn into the messages sent to the fast model: the
// system prompt, if there is one, then a user message that holds the history,
// the question and the lettered options, and ends with "Answer: " so the
// next token is the letter.
//
// History goes newest first, so the turns most likely to matter sit closest
// to the question. Only user and assistant messages go in; tool results are
// long and say little about which route the new question needs.
func buildMessages(turn Turn) []engine.Message {
	// strings.Builder collects text piece by piece without copying it on
	// every append.
	var b strings.Builder

	b.WriteString("Conversation so far:\n")
	n := 0
	for i := len(turn.History) - 1; i >= 0; i-- {
		m := turn.History[i]
		if m.Role != engine.RoleUser && m.Role != engine.RoleAssistant {
			continue
		}
		b.WriteString(string(m.Role) + ": " + m.Content + "\n")
		n++
	}
	if n == 0 {
		b.WriteString("(none)\n")
	}

	b.WriteString("\nQuestion:\n")
	b.WriteString(turn.Question)
	b.WriteString("\n\nPick the best way to answer it.\n\n")
	for _, o := range options {
		b.WriteString(o.letter + " = " + o.description + "\n")
	}
	b.WriteString("\nReply with one letter and nothing else.\nAnswer: ")

	var msgs []engine.Message
	if turn.SystemPrompt != "" {
		msgs = append(msgs, engine.Message{Role: engine.RoleSystem, Content: turn.SystemPrompt})
	}
	return append(msgs, engine.Message{Role: engine.RoleUser, Content: b.String()})
}
