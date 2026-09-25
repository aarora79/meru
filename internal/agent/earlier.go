// This file holds the "From earlier conversations" section of the system
// prompt: past sessions that match the question, found by
// retrieve.SearchSessions, one line each with the date, the summary and
// the message that matched. ARCHITECTURE.md, "Facts and episodes", says
// why: it lets Meru recall last week's decision without a reminder.

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
)

// earlierHeader opens the section. The line after it tells the model what
// the lines are, and that they carry no citation numbers: the cite rule for
// files asks it to cite by number, and these have none.
const earlierHeader = "From earlier conversations:\n" +
	"These are past sessions between you and the user that match the question, one per line. " +
	"Use them when they help. They aren't files, so don't cite them."

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
	section, lines := formatEarlier(results, time.Now(), maxEarlierChars)
	chars := utf8.RuneCountInString(section)
	if chars > 0 {
		obs.RecordContextTokens(ctx, "sessions", chars/4)
	}
	a.log.DebugContext(ctx, "earlier conversations recalled", "found", len(results), "lines", lines,
		"chars", chars, "ms", time.Since(start).Milliseconds())
	return section
}

// formatEarlier writes the section for results, best first, within limit
// characters, and returns it with the number of sessions it holds. Each
// line reads:
//
//   - 2026-09-17 (7 days ago): Chose two raised beds for the garden. The user said: "how many …"
//
// The date is the day the session started, in local time, with how long
// ago that was, since a small model can't work out "last week" from a date
// alone. A session with no summary yet shows only its matching message. A
// line that would pass limit is left out, and so is every line after it. It
// returns "" when no line fits.
func formatEarlier(results []retrieve.SessionResult, now time.Time, limit int) (string, int) {
	var b strings.Builder
	b.WriteString(earlierHeader)
	used := utf8.RuneCountInString(earlierHeader)
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
