// This file holds Continue: a turn that picks up an answer which stopped
// short, at the length limit or the turn's deadline. The client sends an
// ask with Continue set and no new question. merud reads the last turn
// from the session's transcript, which holds the question, every tool
// call with its result, and the answer so far, and hands the model a
// summary of it, so the work it did isn't lost or done again. See
// ARCHITECTURE.md, "Continuing an answer".

package agent

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// continueWords is what a Continue turn's user line holds when the client
// sends no text: what the user pressed, not the summary the model reads.
const continueWords = "Continue"

// errNothingToContinue is the error for a Continue whose session's last
// answer ended whole, or that has no answer at all.
var errNothingToContinue = errors.New("the last answer in this chat is complete, so there is nothing to continue")

// The hand-off's size. resultChars of each result is enough to recall what
// a call found; past handoffChars the oldest calls drop out, since a long
// hand-off would fill the window that cut the answer off.
const (
	resultChars  = 600
	handoffChars = 8000
)

// continuation reads lines, the last turn of a session, and returns its
// question and the message the model reads in place of a question: what
// the last answer was for, the calls it made with the start of each
// result, and what to do next. The answer so far is in the history the
// prompt already holds, so the message leaves it out.
//
// It fails with errNothingToContinue when the turn has no answer, or its
// answer ended whole: only "cut_off", "window_full" and "timeout" stop short with work
// worth keeping.
func continuation(lines []transcript.Line) (question, message string, err error) {
	if len(lines) == 0 {
		return "", "", errNothingToContinue
	}
	question = lines[0].Text
	var ended string
	var calls []string
	// byID finds a call's line in calls by its call ID, to add the result.
	byID := map[string]int{}
	for _, l := range lines {
		switch l.Type {
		case transcript.TypeAssistant:
			ended = l.Outcome
		case transcript.TypeToolCall:
			byID[l.CallID] = len(calls)
			calls = append(calls, "- "+rpc.ToolName(l.Kind, l.Server, l.Tool)+" "+string(l.Args))
		case transcript.TypeToolResult:
			i, ok := byID[l.CallID]
			if !ok {
				continue
			}
			if l.Outcome != "ok" {
				calls[i] += " → " + l.Outcome
				continue
			}
			calls[i] += "\n  " + strings.ReplaceAll(clip(l.Result, resultChars), "\n", "\n  ")
		}
	}
	if ended != endCutOff && ended != endWindowFull && ended != endTimeout {
		return "", "", errNothingToContinue
	}

	// Keep the newest calls that fit; the model needs the latest work most.
	kept, size := 0, 0
	for i := len(calls) - 1; i >= 0; i-- {
		if size+utf8.RuneCountInString(calls[i]) > handoffChars {
			break
		}
		size += utf8.RuneCountInString(calls[i])
		kept++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Continue your answer to my last question: %q\n\n", question)
	b.WriteString("Your answer above stopped short before it was done. ")
	if len(calls) == 0 {
		b.WriteString("It made no tool calls.\n\n")
	} else {
		b.WriteString("These are the tool calls it made, with the start of each result:\n\n")
		if dropped := len(calls) - kept; dropped > 0 {
			fmt.Fprintf(&b, "(%d earlier calls left out.)\n", dropped)
		}
		b.WriteString(strings.Join(calls[len(calls)-kept:], "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("Pick up where the answer stopped. Use these results, and call a tool only for what is still missing. " +
		"Don't repeat what you already wrote.")
	return question, b.String(), nil
}

// clip returns the first n characters of s, with "…" when it cut any.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
