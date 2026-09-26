// This file holds what keeps Meru honest about what it did: the rule and
// the line on what Meru can do, which join the system prompt on every turn,
// and the check that runs after an answer and warns the user when the
// answer claims an action no tool took. See ARCHITECTURE.md, "Agent loop".

package agent

import (
	"regexp"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
)

// honestyRule joins the system prompt on every turn, right after whoIsWho.
// A real turn showed why. Asked to move a folder it had saved to
// ~/meru-output, a small model called no tool and answered "Done. It's now
// at ~/repos/hello-go/". Meru has no tool that moves files, and nothing
// moved. The user found out only when the folder wasn't there.
const honestyRule = "Never say you did something, such as saved, moved, sent, deleted, changed or scheduled, " +
	"unless a tool call in this turn did it and succeeded. " +
	"When none of your tools can do what the user asks, say so first, then offer what you can do."

// canDoNote returns the line that tells the model what Meru's own tools can
// change on the user's computer, built from tools, every tool config
// allows. With write_file among them, it names outputDir, the one folder
// that tool writes in. Meru has no tool that moves, renames or deletes a
// file, so the line says so, and it names the cmd. tools, the only
// programs Meru runs.
//
// It lists what config allows, not what one route offers, so the line
// stays the same from turn to turn and sits with the parts of the prompt
// Ollama reuses (see budget.go). A tool from an MCP server or an A2A
// agent may change things on its own service, such as a mail or a note;
// its own description says so, and the line speaks only of Meru's own
// tools.
func canDoNote(tools []engine.ToolSpec, outputDir string) string {
	write := "You can't write files."
	var cmds []string
	for _, s := range tools {
		switch {
		case s.Name == builtin.WriteFile && outputDir != "":
			write = "You can write files only inside " + outputDir + ", with write_file."
		case toolKind(s.Name) == dispatch.KindCommand:
			cmds = append(cmds, s.Name)
		}
	}
	run := "."
	if len(cmds) > 0 {
		// Sorting keeps the line the same whatever order the tools come in.
		slices.Sort(cmds)
		run = " other than " + strings.Join(cmds, ", ") + "."
	}
	return write + " Meru's own tools can't move, rename or delete files, or run programs" + run
}

// unbackedNotice is the warning a client shows under an answer that claims
// an action when no tool call in the turn succeeded.
const unbackedNotice = "Meru didn't run any tool for this answer, so nothing changed on your computer."

// The action-claim rules. Each regular expression matches one sentence of
// an answer that says an action is done. (?i) makes a pattern ignore case,
// and \b marks a word boundary, so "saved" matches but "unsaved" doesn't.
var (
	// claimDone matches a sentence that opens with "Done" and a stop, such
	// as "Done." or "**All done!**". "Done is better than perfect" has no
	// stop after the word, so it doesn't match.
	claimDone = regexp.MustCompile(`(?i)^[^\pL]*(?:all\s+)?done\s*(?:[.!,:;]|—|–|-|$)`)
	// claimNowAt matches "It's now at ~/Projects/garden" and "The folder is
	// now in ~/Projects". The subject has to be it, they, or a file,
	// folder, note, email or event, so "Go 1.24 is now in beta" doesn't match.
	claimNowAt = regexp.MustCompile(`(?i)\b(?:it|they|the\s+(?:file|files|folder|folders|note|notes|email|event))(?:'s|'re|\s+is|\s+are)\s+now\s+(?:at|in|inside|under|saved)\b`)
	// claimPassive matches "The file has been saved to ~/Projects".
	claimPassive = regexp.MustCompile(`(?i)\b(?:has|have)\s+been\s+(?:moved|saved|sent|deleted|removed|renamed|copied|created|scheduled|updated|written)\b`)
	// claimFirst matches "I've moved the folder" and "I sent the email",
	// with one word allowed between, as in "I just saved".
	claimFirst = regexp.MustCompile(`(?i)\bI(?:'ve|\s+have)?\s+(?:\w+\s+)?(?:moved|saved|sent|deleted|removed|renamed|copied|scheduled)\b`)
	// claimMade matches "I've created", "I updated" and "I wrote". A model
	// also creates, updates and writes text in the chat, so these verbs
	// count only when the sentence names a file, a folder, a note, an email,
	// an event or a path (see namesThing).
	claimMade = regexp.MustCompile(`(?i)\bI(?:'ve|\s+have)?\s+(?:\w+\s+)?(?:created|updated|wrote|written)\b`)
	// namesThing matches a word for something on the computer or in a
	// service, or a path such as ~/notes or /tmp.
	namesThing = regexp.MustCompile(`(?i)\b(?:file|files|folder|folders|directory|note|notes|email|event|reminder|calendar)\b|~/|(?:^|\s)/\w`)
	// notClaim matches a sentence that asks, offers, plans or denies
	// instead of reporting: "I can move it", "Want me to save it",
	// "if you like, I'll", "I couldn't move it". n't covers every
	// "didn't", "can't" and "haven't".
	notClaim = regexp.MustCompile(`(?i)\b(?:if|want\s+me|shall\s+i|should\s+i|would\s+you|do\s+you\s+want|i\s+can|i\s+could|i'll|i\s+will|i'd|i\s+would|i\s+may|i\s+might|let\s+me|once\s+you|when\s+you|not|never|cannot|unable)\b|n't\b`)
	// sentenceEnd splits text after ., ! or ? and a space, so the dot in
	// "main.go" doesn't end a sentence.
	sentenceEnd = regexp.MustCompile(`[.!?]+\s+`)
)

// claimsAction reports whether text says an action is done: a sentence
// matches one of the claim rules above and isn't a question, an offer, a
// condition or a denial. Handle runs it only on a turn in which no tool
// call succeeded, where any such claim can't be true.
//
// It works sentence by sentence, on plain English patterns, so it is easy
// to read and to test, and it will miss some claims and flag some
// sentences that aren't. It misses a claim in words no rule lists, such as
// "Your folder lives in ~/repos now", and one in a language other than
// English. It flags "Done." at the top of a poem the user asked for, where
// the warning is true but not needed. Code blocks are skipped, so a
// comment such as "// I moved this up" doesn't count.
func claimsAction(text string) bool {
	// Models write ’ as often as ', and the rules spell it '.
	text = strings.ReplaceAll(text, "’", "'")
	for _, s := range sentences(withoutCode(text)) {
		if strings.HasSuffix(s, "?") || notClaim.MatchString(s) {
			continue
		}
		if claimDone.MatchString(s) || claimNowAt.MatchString(s) ||
			claimPassive.MatchString(s) || claimFirst.MatchString(s) ||
			(claimMade.MatchString(s) && namesThing.MatchString(s)) {
			return true
		}
	}
	return false
}

// sentences splits text into its sentences, trimmed, one per line of text
// at most. Each keeps its closing mark, so a question still ends in "?".
func sentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		// FindAllStringIndex returns the start and end of each match, and -1
		// asks for every match.
		start := 0
		for _, m := range sentenceEnd.FindAllStringIndex(line, -1) {
			out = appendSentence(out, line[start:m[1]])
			start = m[1]
		}
		out = appendSentence(out, line[start:])
	}
	return out
}

// appendSentence appends s, trimmed, to out, unless it is empty.
func appendSentence(out []string, s string) []string {
	if s = strings.TrimSpace(s); s != "" {
		out = append(out, s)
	}
	return out
}

// withoutCode returns text with its fenced code blocks, the lines between
// two ``` lines and the fences themselves, taken out.
func withoutCode(text string) string {
	var kept []string
	inCode := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if !inCode {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
