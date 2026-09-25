// This file holds the user's profile section of the system prompt: the
// facts in the "me" and "preferences" memory folders, which go into every
// turn (ARCHITECTURE.md, "Memory", step 0). It also holds the check that
// gives a "remember that..." question the remember tool.

package agent

import (
	"cmp"
	"context"
	"github.com/aarora79/meru/internal/builtin"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

// profileHeader opens the profile section.
const profileHeader = "What you know about the user:"

// Profile hands the agent the user's memories: the profile that goes into
// every prompt, and the memories recalled for one question. merud passes a
// small adapter around memory.Store and retrieve.SearchMemories; tests pass
// a fake.
type Profile interface {
	// Profile lists the memories in rpc.ProfileKinds. It may return some
	// memories along with an error, when one file can't be read and the
	// rest can.
	Profile() ([]memory.Memory, error)
	// Recall returns the memories outside the profile kinds that best fit
	// query, best first. It fails when the embedding or the store fails.
	Recall(ctx context.Context, query string) ([]retrieve.Memory, error)
}

// profileSection reads the profile and formats it for the system prompt.
// It returns "" when there is no profile or it holds nothing. It reads the
// files on every turn, so a turn sees a hand edit at once. They are a few
// small files: merud reads 20 of them in about 0.6 ms, too little to earn a
// cache. prompt records the section's size, added to the recalled
// memories', as meru.context.tokens section "memories".
//
// A read that fails is a warning, not the turn's error: the section keeps
// whatever memories did read, and none when the folder couldn't be read.
func (a *Agent) profileSection(ctx context.Context) string {
	if a.profile == nil {
		return ""
	}
	start := time.Now()
	mems, err := a.profile.Profile()
	if err != nil {
		a.log.WarnContext(ctx, "read the user's profile; the prompt holds what could be read", "err", err)
	}
	section, dropped := formatProfile(mems, maxProfileChars)
	if dropped > 0 {
		a.log.DebugContext(ctx, "profile over its cap; left out the oldest memories",
			"left_out", dropped, "cap_chars", maxProfileChars)
	}
	chars := utf8.RuneCountInString(section)
	a.log.DebugContext(ctx, "profile read", "memories", len(mems)-dropped, "chars", chars,
		"ms", time.Since(start).Milliseconds())
	return section
}

// formatProfile turns mems into the profile section: profileHeader, then
// one "- " line per memory. The lines go in rpc.ProfileKinds order ("me"
// first, then "preferences") and oldest first inside each kind, so the
// user's name, usually saved first, leads. A memory's text becomes one line,
// with its line breaks turned into spaces.
//
// The section stays within limit characters. When the memories hold more,
// it keeps the newest ones that fit and returns how many it left out: a
// newer fact more often corrects an older one than the other way round.
// It returns "" when no memory has any text.
func formatProfile(mems []memory.Memory, limit int) (section string, dropped int) {
	type entry struct {
		m    memory.Memory
		line string
	}
	var entries []entry
	for _, m := range mems {
		// strings.Fields splits at every run of spaces and line breaks, so
		// joining the pieces with one space gives a single line.
		text := strings.Join(strings.Fields(m.Text), " ")
		if text != "" {
			entries = append(entries, entry{m: m, line: "- " + text})
		}
	}
	if len(entries) == 0 {
		return "", 0
	}

	// Pick the newest entries that fit, newest first.
	byAge := slices.Clone(entries)
	slices.SortFunc(byAge, func(x, y entry) int { return newerFirst(x.m, y.m) })
	used := utf8.RuneCountInString(profileHeader)
	keep := map[string]bool{}
	for _, e := range byAge {
		// +1 for the line break before the line.
		n := 1 + utf8.RuneCountInString(e.line)
		if used+n > limit {
			dropped++
			continue
		}
		used += n
		keep[e.m.ID] = true
	}
	if len(keep) == 0 {
		return "", dropped
	}

	// Write the kept entries in reading order.
	slices.SortFunc(entries, func(x, y entry) int { return readingOrder(x.m, y.m) })
	lines := []string{profileHeader}
	for _, e := range entries {
		if keep[e.m.ID] {
			lines = append(lines, e.line)
		}
	}
	return strings.Join(lines, "\n"), dropped
}

// readingOrder compares two memories for the section: by kind, in
// rpc.ProfileKinds order with any other kind last, then oldest first. It
// returns a negative number when x comes first, as slices.SortFunc wants.
func readingOrder(x, y memory.Memory) int {
	if c := cmp.Compare(kindRank(x.Kind), kindRank(y.Kind)); c != 0 {
		return c
	}
	return -newerFirst(x, y)
}

// newerFirst compares two memories by age, newest first. Created is a date
// with no time of day, so two facts saved on one day tie on it; the file's
// modification time breaks the tie, and the ID breaks any tie left, so the
// order never depends on the order List returned.
func newerFirst(x, y memory.Memory) int {
	if c := y.Created.Compare(x.Created); c != 0 {
		return c
	}
	if c := y.Modified.Compare(x.Modified); c != 0 {
		return c
	}
	return strings.Compare(y.ID, x.ID)
}

// kindRank returns kind's place in rpc.ProfileKinds, or the length of that
// list for any other kind.
func kindRank(kind string) int {
	kinds := rpc.ProfileKinds()
	if i := slices.Index(kinds, kind); i >= 0 {
		return i
	}
	return len(kinds)
}

// rememberTool is the name of merud's built-in remember tool
// (builtin.Remember). The agent names it here rather than import the
// builtin package, which it has no other use for.
const rememberTool = "remember"

// asksToRemember reports whether question holds "remember" as a whole word
// while the tools on offer include remember. See Handle, which uses it to
// add tools to a route that has none.
func asksToRemember(question string, specs []engine.ToolSpec) bool {
	if !namesFolder(question, []string{rememberTool}) {
		return false
	}
	return slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return s.Name == rememberTool })
}

// asksForWeb reports whether question names the web ("web", "internet" or
// "online", as whole words) and specs include web_search, the tool such a
// question needs.
func asksForWeb(question string, specs []engine.ToolSpec) bool {
	if !namesFolder(question, []string{"web", "internet", "online"}) {
		return false
	}
	return slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return s.Name == builtin.WebSearch })
}
