// This file holds what a turn does after a question that stopped before
// it answered: a note in the prompt that the stopped question's tool
// results are missing, and a notice under an answer that called no tool
// anyway. See ARCHITECTURE.md, "Claims no tool backs".

package agent

import (
	"fmt"

	"github.com/aarora79/meru/internal/transcript"
)

// stoppedNote joins the system prompt when the chat's last question
// stopped before it answered, after %d tool calls. The history leaves such
// a question out (see transcript.Session.History), so without the note the
// model can't know the calls ran and that their results never reached it.
const stoppedNote = "(Meru's note: the user's last question in this chat stopped before it was answered, " +
	"after %d tool calls. Their results aren't in this chat, so you haven't read them. " +
	"If this question needs what they would have returned, call the tools again; " +
	"don't take figures, dates or facts from earlier answers or from memory.)"

// stoppedNotice goes under an answer that called no tool on a turn after
// a stopped question.
const stoppedNotice = "The last question stopped before it finished, and this answer called no tool, " +
	"so it rests on earlier answers, not on what those tool calls would have read. Check it before you rely on it."

// stoppedCalls returns how many tool calls the model made for the chat's
// last question when that question has no answer: the turn was stopped,
// failed or ran out of time. It returns 0 for a new chat, a last question
// that has its answer, and one that stopped before any call. lines is the
// transcript as the turn found it, before its own user line.
//
// A real chat drove this. The user asked for a plan to sell shares without
// short-term gains; the model called a broker's order tool 24 times, and
// the turn was stopped before it answered. The user asked again, the
// history held no trace of the calls, and the model wrote the plan from
// its earlier answer with no call at all, marking every holding long-term.
// Half of them weren't.
func stoppedCalls(lines []transcript.Line) int {
	calls := 0
	for i := len(lines) - 1; i >= 0; i-- {
		switch lines[i].Type {
		case transcript.TypeAssistant:
			return 0
		case transcript.TypeToolCall:
			// A call merud made before the first round has Caller "meru";
			// the model didn't ask for it.
			if lines[i].Caller == "" {
				calls++
			}
		case transcript.TypeUser:
			return calls
		}
	}
	return 0
}

// stoppedText returns stoppedNote for n calls, or "" when n is 0.
func stoppedText(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(stoppedNote, n)
}
