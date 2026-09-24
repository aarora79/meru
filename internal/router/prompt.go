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
// description text, so each one names the kinds of question that belong to
// it and says what it excludes, to push it apart from its neighbours.
//
// The order and the letters were measured (docs/fast-router.md,
// "Calibration"): A to D in this order beat every other order tried, and
// beat the words "direct", "search", "tools" and "both", which the
// tokenizer splits.
//
// A [...]T{...} literal is an array whose length the compiler counts.
var options = [...]option{
	{"A", RouteDirect, "General knowledge, chit-chat, maths, coding or writing help. Needs nothing about the user and nothing recent or live."},
	{"B", RouteSearch, "Look in the user's own saved notes, documents, code repos or past chats. Nothing live, no action."},
	{"C", RouteTools, "Live, recent or outside data (web, news, scores, weather, prices, email inbox, calendar) or an action (send, book, create, schedule). Nothing from the user's notes."},
	{"D", RouteSearchTools, "Needs the user's notes or files AND a live lookup or an action."},
}

// example is one worked question in the prompt and the route it takes.
type example struct {
	question string
	route    Route
}

// examples shows the model two questions per route. They lifted accuracy on
// the labelled set more than any wording change did. None of them repeats a
// question in testdata/routes.jsonl, so the held-out score stays honest.
// Keep them short: the router runs on every turn.
var examples = [...]example{
	{"what's the boiling point of water in Denver", RouteDirect},
	{"fix the grammar: me and him was late", RouteDirect},
	{"what did I note about the gym contract", RouteSearch},
	{"which of my scripts use ffmpeg", RouteSearch},
	{"is it windy in Chicago now", RouteTools},
	{"move my Monday standup to 10", RouteTools},
	{"text Maya the gate code from my notes", RouteSearchTools},
	{"does the hotel in my trip notes have rooms free", RouteSearchTools},
}

// letterFor returns the prompt letter for route r, or "" if r is none of the
// four.
func letterFor(r Route) string {
	for _, o := range options {
		if o.route == r {
			return o.letter
		}
	}
	return ""
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
// system prompt, if there is one, then a user message that holds the lettered
// options, the user's indexed folders, the examples, the history and the
// question, and ends with "Answer: " so the next token is the letter.
//
// The folders matter because the model can't otherwise tell that "meru" in
// "what database does meru use" names one of the user's own projects.
//
// The fixed part (options, folders and examples) comes first and the parts that
// change each turn come last. Ollama reuses the work it did on a prompt's
// opening tokens when the next prompt starts the same way, so the fixed part
// costs almost nothing after the first turn. The order also helped
// accuracy: with the options first, the question sits right before the
// answer cue.
//
// History goes newest first, so the turns most likely to matter sit closest
// to the question. Only user and assistant messages go in; tool results are
// long and say little about which route the new question needs.
func buildMessages(turn Turn) []engine.Message {
	// strings.Builder collects text piece by piece without copying it on
	// every append.
	var b strings.Builder

	b.WriteString("Decide how to answer the user's next question.\n\n")
	for _, o := range options {
		b.WriteString(o.letter + " = " + o.description)
		if o.route == RouteSearch && len(turn.Folders) > 0 {
			b.WriteString(" The user's files are in " + strings.Join(turn.Folders, ", ") +
				"; questions about projects kept there, by name, are B.")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nExamples:\n")
	for _, e := range examples {
		b.WriteString(e.question + " -> " + letterFor(e.route) + "\n")
	}

	b.WriteString("\nConversation so far:\n")
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
	b.WriteString("\n\nReply with one letter and nothing else.\nAnswer: ")

	var msgs []engine.Message
	if turn.SystemPrompt != "" {
		msgs = append(msgs, engine.Message{Role: engine.RoleSystem, Content: turn.SystemPrompt})
	}
	return append(msgs, engine.Message{Role: engine.RoleUser, Content: b.String()})
}
