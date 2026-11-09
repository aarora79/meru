// This file holds what a turn does when it runs out of road: the check for
// a reply that only announces its next step, the nudge that asks the model
// to take that step, and the words for a turn that used every round
// [agent] max_rounds allows. A real session showed both. A turn spent its
// 8 rounds on a web search, a refused fetch, top and pgrep, and the user
// read only "Sorry, I couldn't answer that." The next turn's last round,
// which offers no tools, wrote "Let me find the specific date with 20
// stars." and stopped, and merud logged it as a full answer.

package agent

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxPlanChars is the longest reply plansOnly treats as a plan. An answer
// that opens "Let me explain" and goes on for paragraphs is an answer.
const maxPlanChars = 280

// planOpening matches the start of a reply that announces what the model
// will do next. (?i) ignores case. "Let me know" is left out below: it
// closes an answer, it doesn't plan one.
var planOpening = regexp.MustCompile(`(?i)^(let me|let's|i'll|i will|i'm going to|i am going to|next,? i|now,? i|first,? i)\b`)

// plansOnly reports whether text only announces the model's next step,
// with no answer in it: short, opening on a phrase such as "Let me" or
// "I'll", or ending on a colon after a sentence that opens so, and holding
// no list or table. "Let me know…" and "I'll need…",
// a question back to the user, don't count.
func plansOnly(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxPlanChars {
		return false
	}
	if strings.Contains(text, "\n-") || strings.Contains(text, "\n*") || strings.Contains(text, "|") {
		return false
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "let me know") || strings.HasPrefix(lower, "i'll need") || strings.HasPrefix(lower, "i will need") {
		return false
	}
	if planOpening.MatchString(text) {
		return true
	}
	// A reply that ends on a colon after a plan, as in "Perfect! Now I have
	// all three photos. Let me view the third one and then judge:", stops
	// where its next step should start. A real turn ended there, and the
	// user had to ask "done?".
	if !strings.HasSuffix(text, ":") {
		return false
	}
	ss := sentences(text)
	return len(ss) > 0 && planOpening.MatchString(ss[len(ss)-1])
}

// planNudge is the message a turn adds, once, after a round that only
// announced its next step and called nothing while rounds were left. It
// lives in this turn's messages only; the transcript never records it.
const planNudge = "You said what you would do next but didn't do it. " +
	"Do it now: call the tool, or answer from what you have."

// lastRoundNote joins the last round of a turn that had tools, the round
// that offers none. It tells the model to answer from what it has, and to
// say what it didn't get to, rather than plan another search it can't run.
const lastRoundNote = "This is your last step: no more tool calls are possible. " +
	"Answer the question now from what the tools already returned. " +
	"Then say in one sentence what you didn't get to check, so the user can ask for it."

// capAnswer is the answer to a turn that used every round with nothing to
// show. %d is max_rounds; %s names the tools tried, as tried writes them.
const capAnswer = "Meru used all %d rounds of tool calls this question allows and didn't reach an answer. " +
	"It tried: %s. Continue gives it more rounds with what it found, or ask a narrower question."

// capNote follows the text of a capped turn that only planned its next
// step, as cutOffNote follows a cut answer.
const capNote = "[Meru stopped here: it used all %d rounds of tool calls before the answer was done. " +
	"It tried: %s. Continue picks it up.]"

// maxTried is how many tools capAnswer and capNote name before "and N more".
const maxTried = 8

// tried writes the tools a turn called, each once in the order first
// called, with "×n" for one called more than once and its failures in
// brackets: "web_search, web_fetch (refused), grep ×2". Past maxTried
// tools, the rest are counted. With none it says so.
func tried(calls []triedCall) string {
	if len(calls) == 0 {
		return "no tools"
	}
	type entry struct {
		name   string
		n      int
		failed map[string]bool
	}
	var order []*entry
	byName := map[string]*entry{}
	for _, c := range calls {
		e, ok := byName[c.name]
		if !ok {
			e = &entry{name: c.name, failed: map[string]bool{}}
			byName[c.name] = e
			order = append(order, e)
		}
		e.n++
		if c.outcome != "" && c.outcome != "ok" {
			e.failed[c.outcome] = true
		}
	}
	var parts []string
	for i, e := range order {
		if i == maxTried {
			parts = append(parts, fmt.Sprintf("and %d more", len(order)-maxTried))
			break
		}
		s := e.name
		if e.n > 1 {
			s += fmt.Sprintf(" ×%d", e.n)
		}
		var why []string
		for _, o := range []string{"error", "denied", "declined", "timeout", "cancelled"} {
			if e.failed[o] {
				why = append(why, failedWord(o))
			}
		}
		if len(why) > 0 {
			s += " (" + strings.Join(why, ", ") + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// triedCall is one call the model made this turn: the tool's name as the
// model knew it, and how the call ended, one of dispatch's outcomes.
type triedCall struct {
	name    string
	outcome string
}

// failedWord says how a call failed, in the words capAnswer uses.
func failedWord(outcome string) string {
	switch outcome {
	case "denied":
		return "not allowed"
	case "declined":
		return "you said no"
	case "timeout":
		return "timed out"
	case "cancelled":
		return "cancelled"
	}
	return "failed"
}

// meruNoteStarts are the openings of the notes only Meru writes: the cap
// note after a turn that ran out of rounds, and the tool and web notes the
// history carries after an answer. A model must never write them itself.
var meruNoteStarts = []string{
	"[Meru stopped here:",
	toolNotesStart,
	"(Meru's notes on the pages",
}

// toolNotesStart opens the tool notes the history carries after an answer
// (transcript's toolNotesHead). followsServerTurn reads the notes after it.
const toolNotesStart = "(Meru's record of the tools"

// withoutMeruNotes returns text with any of Meru's own notes the model
// copied into it cut out, and the rest trimmed. A cap note ends at its
// closing bracket; a tool or web note runs to the end of the text, as it
// does in history. A real Continue turn called no tool and wrote back a cap
// note naming calls it never made, then the tool record from the history;
// cut, its text was only the plan "Let me get the actual folio PDF
// content", which the plan nudge then acts on.
func withoutMeruNotes(text string) string {
	for _, start := range meruNoteStarts {
		for {
			i := strings.Index(text, start)
			if i < 0 {
				break
			}
			end := len(text)
			if start[0] == '[' {
				if j := strings.Index(text[i:], "]"); j >= 0 {
					end = i + j + 1
				}
			}
			text = text[:i] + text[end:]
		}
	}
	return strings.TrimSpace(text)
}
