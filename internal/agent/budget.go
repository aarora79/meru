// This file holds the context budget: how much of the prompt each section
// may use, and the order the sections go in. See ARCHITECTURE.md, "Agent
// loop", step 2.

package agent

import (
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/engine"
)

// The budget, in characters; a token is about four characters of English.
// A turn on the lite model fills well under a tenth of its 131k-token
// window, so these caps aren't there to fit the window. They keep one
// section from crowding the others out of the model's attention, and keep
// the prompt short enough to start answering fast.
//
//	section                     cap (chars)  about   what goes past it
//	profile (me, preferences)   2,000        500 t   the oldest facts
//	recalled memories           2,400        600 t   the lowest-ranked
//	skill instructions          12,000       3,000 t the second skill, cut
//	earlier conversations       2,400        600 t   the lowest-ranked
//	history                     8,000        2,000 t the oldest turns
//
// File excerpts have no cap of their own: a search keeps 10 chunks of about
// 500 tokens each, so they stay under 5,000 tokens. meru.context.tokens
// records each section's size per turn, the numbers to tune these by.
const (
	maxProfileChars = 2000
	maxMemoryChars  = 2400
	// The first skill always loads whole, even past the cap, because half
	// a skill's steps can mislead the model; the second is cut to fit,
	// with a note. The shipped explainer alone runs to about 5,000 tokens.
	maxSkillChars   = 12000
	maxEarlierChars = 2400
	maxHistoryChars = 8000
)

// The order of the system prompt's sections. The parts that stay the same
// from turn to turn come first: the system prompt, whoIsWho, today's date,
// the profile, the files note, the tools note and the list of skills. The
// note on the file tools comes next, on file turns only (see aboutFiles).
// Then come the parts each question changes: recalled memories, the picked
// skills' instructions, and the file excerpts with earlier conversations.
//
// The order matters for speed. Ollama reuses its work on a prompt's opening
// tokens when the next prompt starts the same way, and it stops reusing at
// the first token that differs. With the changing parts last, a follow-up
// in the same session reprocesses only them, the history and the question.
// With the file-tools note after the shared parts, a web question after a
// file question still reuses everything up to the list of skills.

// today tells the model the date, in merud's local time zone, such as
// "Today is Thursday, 24 September 2026." A model knows only its training
// data, so without this it read "a trip from 15 to 20 September 2026" in a
// visa letter on the 24th and said no visit was on record. The date changes
// once a day, so it can sit among the parts that stay the same from turn to
// turn; the time of day would change every minute and cost the reuse.
func today(now time.Time) string {
	return "Today is " + now.Format("Monday, 2 January 2006") + ". " +
		"For the time of day, a weekday, days between dates or the time in another place, call the datetime tool."
}

// sections is what a turn adds to the system prompt, beyond the parts
// every turn gets.
type sections struct {
	memories    string // recalled memories; "" for none
	skillList   string // every skill's name and description; "" for none
	skillBodies string // the picked skills' instructions; "" for none
	files       string // file excerpts, then earlier conversations; "" for none
	toolsNote   string // toolsNote or commandsNote when the turn offers tools; "" for none
	fileTools   string // fileToolsNote or exploreNote on a file turn with the file tools; "" otherwise
}

// trimHistory drops the oldest messages from history until the rest fits
// in maxChars characters, and returns what remains and how many messages it
// dropped. It drops a user message and the assistant message after it
// together, so the history never starts with an answer to a question that
// isn't there.
func trimHistory(history []engine.Message, maxChars int) ([]engine.Message, int) {
	total := 0
	for _, m := range history {
		total += utf8.RuneCountInString(m.Content)
	}
	start := 0
	for total > maxChars && start < len(history) {
		total -= utf8.RuneCountInString(history[start].Content)
		start++
		// Take the answer with its question.
		for start < len(history) && history[start].Role != engine.RoleUser {
			total -= utf8.RuneCountInString(history[start].Content)
			start++
		}
	}
	return history[start:], start
}
