// This file holds what the model learns about its past chats with the
// user: the "From earlier conversations" section of the system prompt,
// past sessions that match the question, found by retrieve.SearchSessions,
// one line each with the date, the summary and the message that matched;
// the note that says where every chat is kept, for the file tools to read;
// and the rule that sends a direct question about past chats to the search
// route, which offers those tools. ARCHITECTURE.md, "Facts and episodes",
// says why: it lets Meru recall last week's decision without a reminder,
// and answer about any chat when the three best matches aren't enough.

package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
)

// earlierHeader returns the section's first two lines. The second tells
// the model the lines are its own past chats with the user, and only the
// few that best match the question. A real turn showed why. Asked "check
// my previous conversations with you, what were they all about", a model
// that saw three lines told the user it had no access to its past
// conversations, then listed those three, out of more than two hundred.
// chats is the sessions folder as the model reads it, such as
// ~/.meru/sessions, when the file tools can read it, and "" when they
// can't; the line then points at the folder only when the model can open
// it. The line also says the lines carry no citation numbers: the cite
// rule for files asks the model to cite by number, and these have none.
// It stays one line, which the section's tests count on.
func earlierHeader(chats string) string {
	more := "Every chat is kept, but you see only these. "
	if chats != "" {
		more = "Every chat is in " + chats + "; to see more, use grep, list_folder and read_file there. "
	}
	return "From earlier conversations:\n" +
		"These are your own past chats with the user: only the few that best match the question, one per line. " +
		more + "Never tell the user you have no access to past chats. " +
		"Use these when they help, but they carry no citation numbers, so don't cite them."
}

// The limits on the section.
const (
	// earlierSessions is how many past sessions a turn recalls at most.
	earlierSessions = 3
	// maxEarlierSummary and maxEarlierMatch cap the two parts of one line,
	// so a single session can't fill the section.
	maxEarlierSummary = 400
	maxEarlierMatch   = 300
)

// earlierSection searches past sessions for query, leaving out the session
// sessionID, and returns the prompt section, or "" when nothing matches.
// Handle calls it on the routes that search files.
//
// A failed search is a warning, not the turn's error: the turn goes on
// without the section. The section's size goes to meru.context.tokens as
// "sessions".
func (a *Agent) earlierSection(ctx context.Context, query, sessionID string) string {
	start := time.Now()
	results, err := a.search.SearchSessions(ctx, query, sessionID, earlierSessions)
	if err != nil {
		if ctx.Err() == nil {
			a.log.WarnContext(ctx, "recall of earlier conversations failed; answering without them", "err", err)
		}
		return ""
	}
	section, lines := formatEarlier(earlierHeader(a.chatsFolder()), results, time.Now(), maxEarlierChars)
	chars := utf8.RuneCountInString(section)
	if chars > 0 {
		obs.RecordContextTokens(ctx, "sessions", chars/4)
	}
	a.log.DebugContext(ctx, "earlier conversations recalled", "found", len(results), "lines", lines,
		"chars", chars, "ms", time.Since(start).Milliseconds())
	return section
}

// formatEarlier writes the section for results, best first, under header
// and within limit characters, and returns it with the number of sessions
// it holds. Each line reads:
//
//   - 2026-09-17 (7 days ago): Chose two raised beds for the garden. The user said: "how many …"
//
// The date is the day the session started, in local time, with how long
// ago that was, since a small model can't work out "last week" from a date
// alone. A session with no summary yet shows only its matching message. A
// line that would pass limit is left out, and so is every line after it. It
// returns "" when no line fits.
func formatEarlier(header string, results []retrieve.SessionResult, now time.Time, limit int) (string, int) {
	var b strings.Builder
	b.WriteString(header)
	used := utf8.RuneCountInString(header)
	n := 0
	for _, r := range results {
		var parts []string
		if s := oneLine(r.Summary, maxEarlierSummary); s != "" {
			parts = append(parts, s)
		}
		if r.Match != nil {
			who := "The user said"
			if r.Match.Role == "assistant" {
				who = "You said"
			}
			if m := oneLine(r.Match.Text, maxEarlierMatch); m != "" {
				parts = append(parts, fmt.Sprintf("%s: %q", who, m))
			}
		}
		if len(parts) == 0 {
			continue
		}
		line := fmt.Sprintf("\n- %s: %s", when(r.Started, now), strings.Join(parts, " "))
		size := utf8.RuneCountInString(line)
		if used+size > limit {
			break
		}
		b.WriteString(line)
		used += size
		n++
	}
	if n == 0 {
		return "", 0
	}
	return b.String(), n
}

// when writes t's local date with how many days before now it falls:
// "2026-09-17 (7 days ago)", "(yesterday)" or "(today)".
func when(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	// Count calendar days, not 24-hour spans: a session at 23:00 yesterday
	// is "yesterday" at 08:00 today.
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.Local) }
	days := int(day(now).Sub(day(t)).Hours()+12) / 24 // +12 absorbs a daylight-saving hour
	ago := fmt.Sprintf("%d days ago", days)
	switch {
	case days <= 0: // today, or a clock set back
		ago = "today"
	case days == 1:
		ago = "yesterday"
	}
	return t.Format("2006-01-02") + " (" + ago + ")"
}

// oneLine turns text into one line, its runs of spaces and line breaks
// made single spaces, cut to limit characters with "…" when it was longer.
func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	r := []rune(text)
	return strings.TrimSpace(string(r[:limit-1])) + "…"
}

// joinSections joins two parts of the system prompt with a blank line,
// leaving out a part that is "".
func joinSections(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

// chatsFolder returns the sessions folder as the model reads it, such as
// ~/.meru/sessions, when the turn's tools can read it, and "" when they
// can't. merud lets list_folder, grep and read_file read the folder (see
// internal/builtin/chats.go), and they are on while config allows
// read_file and names an [index] folder, so read_file among the tools
// config allows is the sign. It reads the tools each call, since the
// desktop app can turn read_file off or add the first folder while merud
// runs.
func (a *Agent) chatsFolder() string {
	if a.tools == nil {
		return ""
	}
	if !slices.ContainsFunc(a.tools.Tools(), func(s engine.ToolSpec) bool { return s.Name == builtin.ReadFile }) {
		return ""
	}
	return displayDir(a.home, a.sessionsDir)
}

// chatsNote returns the note on where the past chats are, for the part of
// the system prompt that stays the same from turn to turn, or "" when the
// file tools can't read them. The route rule below makes sure a question
// about past chats gets the tools the note names.
func (a *Agent) chatsNote() string {
	dir := a.chatsFolder()
	if dir == "" {
		return ""
	}
	return "Your past chats with the user are in " + dir + ": one JSONL file per chat, " +
		"named by the UTC time it started, such as 2026/09/2026-09-17T141502-7f3a.jsonl, " +
		"with one JSON object per line for each question, answer and tool call. " +
		"A chat's folder and tags are in its meta lines, such as " +
		`{"type":"meta","folder":"Garden","tags":["bulbs"]}` + "; the newest one wins, " +
		`so to find chats tagged bulbs, grep there for "tags" and bulbs. ` +
		"To answer about earlier conversations, use list_folder, grep and read_file there yourself; " +
		"don't offer to search, search."
}

// chatNouns, pastWords and pastVerbs are the words aboutPastChats looks
// for.
var (
	chatNouns = []string{"chat", "chats", "conversation", "conversations", "session", "sessions"}
	pastWords = []string{"previous", "earlier", "past", "last", "before", "ago", "yesterday", "prior", "old", "older", "history",
		// A chat's tags and folder, which only the chat files hold.
		"tag", "tags", "tagged", "folder"}
	pastVerbs = []string{"talked", "discussed", "chatted"}
)

// aboutPastChats reports whether question asks about earlier chats with
// Meru: it names a chat or conversation along with a word that points back
// in time, as "check my previous conversations" does, or says "we talked"
// or "we discussed".
//
// The router sends such a question direct, since it needs no file and no
// connected tool, and the direct route offers neither the file tools nor
// the recalled sessions. Respond moves it to the search route, which
// offers both. A wrong guess costs one search and the file tools' schemas
// in the prompt.
func aboutPastChats(question string) bool {
	w := words(question)
	has := func(list []string) bool {
		return slices.ContainsFunc(w, func(x string) bool { return slices.Contains(list, x) })
	}
	return (has(chatNouns) && has(pastWords)) || (slices.Contains(w, "we") && has(pastVerbs))
}
