// This file holds the tool notes a turn leaves for the next turns: one
// short line per tool call the turn made, which History adds after the
// answer, so a follow-up knows which tools ran and what they returned. A
// real session showed why. analyze_table summed five rows to 555.13; asked
// how it got there, the model, whose history held only the questions and
// answers, listed six rows that don't exist and four tool steps that never
// ran. The notes come from the tool lines the transcript already holds, so
// nothing new is stored, and an incognito chat reads them from memory.

package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The limits on one turn's tool notes. Six calls of about 200 characters
// fit in 1,200, under a sixth of the history's 8,000 (the agent's
// maxHistoryChars), so two turns of notes still leave most of it for the
// questions and answers.
const (
	toolNoteTurns    = 2    // the latest turns whose answers carry notes
	maxToolNotes     = 6    // calls with a line of their own; the rest by name
	maxNoteArgChars  = 160  // a call's arguments, as compact JSON
	maxNoteTextChars = 300  // the start of its result, on one line
	maxToolNoteChars = 1200 // one turn's notes in all
)

// toolNotesHead is the line above a turn's tool notes. Like webNotesHead it
// says who wrote them and that they aren't part of the answer, so the model
// doesn't copy them into one, and it says what they are for.
const toolNotesHead = "(Meru's record of the tools this turn ran, for later questions. " +
	"It isn't part of the answer above; never copy it into an answer. " +
	"Use it to say how you got that answer; run a tool again for anything it leaves out.)"

// WebSearchTool and WebFetchTool name the two web tools, whose results the
// web notes already keep. They are exported so the agent's tests can check
// them against internal/builtin's names; this package can't import
// builtin, which imports it.
const (
	WebSearchTool = "web_search"
	WebFetchTool  = "web_fetch"
)

// toolCall is one call of a turn as the transcript recorded it: the
// tool_call line and, once the call ended, its tool_result line.
type toolCall struct {
	call   Line
	result *Line // nil when the call never ended, such as in a cancelled turn
}

// name is the tool's name as the model knew it: the tool alone for one of
// merud's own ("analyze_table"), and server.tool otherwise ("notes.search").
func (c toolCall) name() string {
	if c.call.Server == "" || c.call.Server == "meru" {
		return c.call.Tool
	}
	return c.call.Server + "." + c.call.Tool
}

// toolNotes returns a turn's notes after a blank line: toolNotesHead, then
// one line per call in the order they ran, such as
//
//   - analyze_table {"path":"~/trip.csv"} ok: Matched 3 of 6 rows… | 1207.00 |
//
// It returns "" for a turn with no calls. A web tool's line names the tool
// and its outcome only, since the web notes hold what it found. Calls past
// maxToolNotes, or past maxToolNoteChars in all, are named in one last
// line with a count each, "and 4 more: grep ×3, list_folder", so a
// question about the tools still sees every one.
func toolNotes(calls []toolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + toolNotesHead)
	used, written := 0, 0
	var rest []string
	for _, c := range calls {
		line := "\n- " + noteLine(c)
		n := utf8.RuneCountInString(line)
		// Once one call misses the cut, the rest go by name too, so the
		// lines keep the order the calls ran in.
		if len(rest) > 0 || written == maxToolNotes || used+n > maxToolNoteChars {
			rest = append(rest, c.name())
			continue
		}
		b.WriteString(line)
		used += n
		written++
	}
	if len(rest) > 0 {
		fmt.Fprintf(&b, "\n- and %d more: %s", len(rest), byCount(rest))
	}
	return b.String()
}

// noteLine writes one call: its name, its arguments, how it ended and the
// start of its result.
func noteLine(c toolCall) string {
	line := c.name()
	if args := compactArgs(c.call.Args); args != "" {
		line += " " + args
	}
	if c.result == nil {
		return line + " didn't finish"
	}
	outcome := c.result.Outcome
	if outcome == "" {
		outcome = "ok"
	}
	line += " " + outcome
	if c.call.Tool == WebSearchTool || c.call.Tool == WebFetchTool {
		return line + " (what it found is in the web notes)"
	}
	if text := resultExcerpt(c.result.Result); text != "" {
		line += ": " + text
	}
	return line
}

// compactArgs writes a call's arguments as compact JSON, cut to
// maxNoteArgChars. Arguments that aren't valid JSON, as in a line a crash
// tore, are cut as they stand rather than dropped. "{}" counts as none.
func compactArgs(raw json.RawMessage) string {
	var b bytes.Buffer
	text := string(raw)
	// json.Compact strips the white space between tokens.
	if err := json.Compact(&b, raw); err == nil {
		text = b.String()
	}
	text = strings.TrimSpace(text)
	if text == "" || text == "{}" || text == "null" {
		return ""
	}
	return cutRunes(text, maxNoteArgChars)
}

// resultExcerpt returns the start of a tool's result on one line, cut to
// maxNoteTextChars. Lines that start "Cite " go: a result's "Cite this
// table as [11]." names a number from its own turn, and the next turn
// numbers its sources afresh. A "Saved at …" line moves to the front (see
// savedLead).
func resultExcerpt(result string) string {
	var kept, saved []string
	for _, line := range strings.Split(result, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "Cite "):
			continue
		case strings.HasPrefix(line, savedLead):
			// The path of a file the call saved, such as a downloaded
			// attachment, comes first, so the cut never drops it.
			saved = append(saved, line)
			continue
		}
		kept = append(kept, line)
	}
	kept = append(saved, kept...)
	text := strings.Join(strings.Fields(strings.Join(kept, " ")), " ")
	return cutRunes(text, maxNoteTextChars)
}

// savedLead starts the line a tool's result ends with when Meru saved a
// file for it, such as an email attachment: "Saved at ~/meru-output/…". A
// real chat downloaded a photo for a greeting card, and two turns later,
// asked to put the photo in, the model searched the user's folders for one:
// the cut to maxNoteTextChars had kept the attachment's long ID and dropped
// the path. resultExcerpt puts this line first.
const savedLead = "Saved at "

// cutRunes returns s cut to at most n characters, with "…" in place of the
// last one when it cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// byCount names each tool in names once, in the order it first appears,
// with "×n" after a tool that ran more than once: "grep ×3, list_folder".
func byCount(names []string) string {
	counts := map[string]int{}
	var order []string
	for _, n := range names {
		if counts[n] == 0 {
			order = append(order, n)
		}
		counts[n]++
	}
	parts := make([]string, len(order))
	for i, n := range order {
		parts[i] = n
		if counts[n] > 1 {
			parts[i] = fmt.Sprintf("%s ×%d", n, counts[n])
		}
	}
	return strings.Join(parts, ", ")
}
