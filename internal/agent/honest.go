// This file holds what keeps Meru honest about what it did: the rule and
// the line on what Meru can do, which join the system prompt on every turn,
// and the two checks that run after an answer and warn the user when the
// answer claims an action no tool took, or a tool call that didn't happen.
// See ARCHITECTURE.md, "Claims no tool backs".

package agent

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/transcript"
)

// honestyRule joins the system prompt on every turn, right after whoIsWho.
// A real turn showed why. Asked to move a folder it had saved to
// ~/meru-output, a small model called no tool and answered "Done. It's now
// at ~/repos/hello-go/". Meru has no tool that moves files, and nothing
// moved. The user found out only when the folder wasn't there.
//
// A second turn added the middle two sentences. Asked the time, a model
// called no tool and made up "3:25 PM". Asked how it knew, it answered "I
// called the datetime tool", which no call in the session backed.
const honestyRule = "Never say you did something, such as saved, moved, sent, deleted, changed or scheduled, " +
	"unless a tool call in this turn did it and succeeded. " +
	"Never say you called, ran or used a tool or command unless you called it in this conversation. " +
	"Never make up what a tool would give you, such as a time, a file's contents or a search result: call the tool, or say you don't know. " +
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
// tools. When tools holds about_meru, selfNote follows, which sends
// questions about Meru itself to that tool.
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
	line := write + " Meru's own tools can't move, rename or delete files, or run programs" + run
	if slices.ContainsFunc(tools, func(s engine.ToolSpec) bool { return s.Name == builtin.AboutMeru }) {
		line += " " + selfNote
	}
	return line
}

// selfNote follows canDoNote's line when config allows about_meru. A real
// turn showed why. Asked "which model are you using" and then "tell me the
// exact model name", a question the router sent direct, the main model
// answered from its training that it came from another company's lab and
// had no access to version numbers, while merud knew the exact name. The
// line depends only on config, so it stays with the parts of the prompt
// Ollama reuses.
const selfNote = "For questions about yourself or this setup, such as which model you are, " +
	"what you can reach or which folders you read, call about_meru; don't answer from what you learned in training."

// unbackedNotice is the warning a client shows under an answer that claims
// an action when no tool call in the turn succeeded.
const unbackedNotice = "Meru didn't run any tool for this answer, so nothing changed on your computer."

// callNotice returns the warning a client shows under an answer that says
// Meru called a tool when no call backs it. tool is the tool the answer
// names, "" when it names none Meru knows.
func callNotice(tool string) string {
	if tool == "" {
		return "Meru didn't run any tool for this answer, though the answer says it did."
	}
	return "Meru didn't run " + tool + " for this answer, though the answer says it did."
}

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

// The call-claim rules. Each matches a sentence of an answer that says
// Meru called, ran or used a tool or a program. A raw string between
// backquotes can't hold a backquote, so the rules that look for one are
// written in double quotes, where each backslash is doubled.
var (
	// callFirst matches "I called", "I've used", "I just ran" and the like,
	// with one word allowed between. It counts only when the sentence also
	// says tool or command, or names a tool in backquotes (see toolClaims):
	// "I used your notes" claims no call.
	callFirst = regexp.MustCompile(`(?i)\bI(?:'ve|\s+have)?\s+(?:\w+\s+)?(?:called|ran|run|used|invoked|executed|queried|checked|asked)\b`)
	// runProgram matches "I ran date" and "I executed `ls`": a first-person
	// run of a common program. Meru runs no program but the cmd. tools, so
	// the sentence needs no tool word. The group holds the program's name.
	runProgram = regexp.MustCompile("(?i)\\bI(?:'ve|\\s+have)?\\s+(?:\\w+\\s+)?(?:ran|run|executed)\\s+(?:the\\s+)?`?(date|ls|grep|find|cat|curl|python3?|bash|zsh|sh|git|uname|pwd|which)\\b")
	// usingTool matches "using the grep tool" and "via the `date`
	// command". It counts only in a sentence with "I" in it, so "You can
	// search using the grep tool" doesn't match.
	usingTool = regexp.MustCompile("(?i)\\b(?:using|via|through|with|from)\\s+(?:the\\s+|your\\s+)?`?[\\w.-]+`?\\s+(?:tool|command)\\b")
	// firstPerson matches the word I, alone or in I've or I'm.
	firstPerson = regexp.MustCompile(`(?i)\bI\b`)
	// toolResult matches "the datetime tool returned" and "according to
	// the datetime tool": a result the answer says a tool gave.
	toolResult = regexp.MustCompile("(?i)\\b(?:tool|command)\\s+(?:returned|says|said|shows|showed|reported|gave|replied)\\b|\\baccording\\s+to\\s+the\\s+`?[\\w.-]+`?\\s+(?:tool|command)\\b")
	// toolWord matches the words that turn "I used" into a claim of a call.
	toolWord = regexp.MustCompile(`(?i)\b(?:tool|tools|command|commands)\b`)
	// backquoted matches a word in backquotes, such as `datetime`, and
	// holds the word in its group.
	backquoted = regexp.MustCompile("`([^`\\s]+)`")
	// toolLike matches a run of letters, digits, _, . and -, the shape of
	// a tool name such as search_files or notes.search.
	toolLike = regexp.MustCompile(`[\w.-]+`)
)

// toolClaim is one sentence of an answer that says Meru called a tool.
// names holds the tools it names, each in shortTool's form; empty when it
// names none Meru can tell.
type toolClaim struct {
	names []string
}

// toolClaims returns each sentence of text that says Meru called, ran or
// used a tool or a program, with the tools it names. tools holds the name
// of every tool config allows, so "I used the search_files tool" names
// search_files. A question, an offer or a denial doesn't count, by the same
// notClaim rule claimsAction uses, and code blocks are skipped.
//
// Like claimsAction it reads plain English patterns. It misses a claim in
// words no rule lists, such as "the clock says 3 PM", and one in another
// language. It flags "Here is the command I used" in a how-to answer,
// where Meru ran no command either.
func toolClaims(text string, tools []string) []toolClaim {
	// known is a set: a map whose values are all true.
	known := map[string]bool{}
	for _, name := range tools {
		known[shortTool(name)] = true
	}
	// Models write ’ as often as ', and the rules spell it '.
	text = strings.ReplaceAll(text, "’", "'")
	var out []toolClaim
	for _, s := range sentences(withoutCode(text)) {
		if strings.HasSuffix(s, "?") || notClaim.MatchString(s) {
			continue
		}
		var names []string
		// add appends name in its short form, unless it is there already.
		add := func(name string) {
			if n := shortTool(name); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		hasToolWord := toolWord.MatchString(s)
		namedInQuotes := false
		// FindAllStringSubmatch returns each match with its groups; m[1]
		// is the word inside the backquotes.
		for _, m := range backquoted.FindAllStringSubmatch(s, -1) {
			switch {
			case known[shortTool(m[1])]:
				namedInQuotes = true
				add(m[1])
			case hasToolWord:
				// "the `date` command" names a program Meru never ran.
				add(m[1])
			}
		}
		for _, w := range toolLike.FindAllString(s, -1) {
			if known[shortTool(w)] {
				add(w)
			}
		}
		run := runProgram.FindStringSubmatch(s)
		if run != nil {
			add(run[1])
		}
		if run != nil ||
			(callFirst.MatchString(s) && (hasToolWord || namedInQuotes)) ||
			(usingTool.MatchString(s) && firstPerson.MatchString(s)) ||
			toolResult.MatchString(s) {
			out = append(out, toolClaim{names: names})
		}
	}
	return out
}

// unbackedCall reports whether a claim in claims has no call behind it,
// and returns the tool that claim names, or "" when it names none. called
// holds the tools that ran in this turn and in the earlier turns the
// history holds, in any form shortTool reads. A claim that names tools is
// backed when one of them ran; one that names none is backed when any
// tool ran.
func unbackedCall(claims []toolClaim, called []string) (string, bool) {
	ran := map[string]bool{}
	for _, name := range called {
		ran[shortTool(name)] = true
	}
	for _, c := range claims {
		if len(c.names) == 0 {
			if len(ran) == 0 {
				return "", true
			}
			continue
		}
		if !slices.ContainsFunc(c.names, func(n string) bool { return ran[n] }) {
			return c.names[0], true
		}
	}
	return "", false
}

// unbackedToolClaim reports whether text, the turn's answer, says Meru
// called a tool that no call backs, and returns that tool, "" when the
// claim names none. The calls that count are the turn's own (t.called)
// and those in the session's transcript for this turn and the historyN
// turns before it, the ones the model reads in its history: a follow-up
// such as "how did you get this time?" may answer about an earlier turn.
// It reads the transcript only when the answer holds a claim. A transcript
// it can't read counts as no earlier calls, with a debug line.
func (a *Agent) unbackedToolClaim(ctx context.Context, t *turn, sess *transcript.Session, text string) (string, bool) {
	var names []string
	if a.tools != nil {
		for _, s := range a.tools.Tools() {
			names = append(names, s.Name)
		}
	}
	claims := toolClaims(text, names)
	if len(claims) == 0 {
		return "", false
	}
	called := slices.Clone(t.called)
	earlier, err := sess.CalledTools(a.historyN + 1)
	if err != nil {
		a.log.DebugContext(ctx, "read earlier tool calls", "err", err)
	}
	called = append(called, earlier...)
	return unbackedCall(claims, called)
}

// shortTool returns the last dot-separated part of a tool name, in lower
// case, with a closing stop or comma cut: "notes.search" and "Search." both
// give "search". The model sees "notes.search", the transcript records
// tool "search" on server "notes", and an answer may write either, so the
// check compares this short form.
func shortTool(name string) string {
	name = strings.ToLower(strings.TrimRight(name, ".,:;!"))
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}
