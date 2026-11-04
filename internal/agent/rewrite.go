// This file holds the route rule for writing help on text the question
// itself gives, such as "rewrite: …" or "translate "…" into French". See
// ARCHITECTURE.md, "Routing".

package agent

import (
	"regexp"
	"strings"
)

// writingVerb matches a question that opens with a request to change some
// writing, after an optional "please", "can you" or "could you". (?i) makes
// it ignore case, and \b ends the verb at a word boundary, so "rewrites" and
// "editor" don't match.
var writingVerb = regexp.MustCompile(`(?i)^\s*(?:please\s+|can\s+you\s+|could\s+you\s+)?(?:rewrite|rephrase|reword|paraphrase|proofread|shorten|simplify|translate|polish|edit|improve|fix)\b`)

// givenTextMarks are the marks that show the text to change follows in the
// question: a line break, a colon, or a double quote, straight or curly. A
// single quote isn't one, since "rewrite what's in my notes" holds one.
const givenTextMarks = "\n:\"“”"

// writingOnText reports whether question asks to rewrite, proofread,
// translate or otherwise change text it gives itself, as "rewrite\nMeru is
// great" and `proofread this: "…"` do. Such a turn needs nothing from the
// user's files, so Respond sends it direct whatever the router said.
//
// A real turn showed why. The router sent `rewrite "Meru streamlines my
// workflow by handling my tasks efficiently."` to search, at 0.52 against
// 0.43 for direct, most likely because the text says "my workflow" and "my
// tasks". The search added ten excerpts and 7,000 tokens to the prompt and
// slowed the answer. Two changes to the router's prompt that fixed that
// question lowered its score on the labelled set (docs/fast-router.md), so
// the fix is this rule instead, which leaves the router as it is.
//
// The verb must open the question and one of givenTextMarks must follow it,
// so "rewrite my resume from my documents" still goes where the router
// sends it. It gets wrong a question such as `edit "plan.md" in my notes`,
// which quotes a file name: that turn goes direct, and the model can still
// say it needs the file.
func writingOnText(question string) bool {
	loc := writingVerb.FindStringIndex(question)
	if loc == nil {
		return false
	}
	return strings.ContainsAny(question[loc[1]:], givenTextMarks)
}
