// This file holds the recalled-memories section of the system prompt: the
// memories outside the profile that best fit the question, found by
// meaning, keyword and recency (ARCHITECTURE.md, "Memory", step 2). It
// also records the size of both memory sections as one number.

package agent

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
)

// memoryHeader opens the recalled-memories section.
const memoryHeader = "Things you remember that may matter here:"

// memorySection recalls the memories that fit query and formats them for
// the system prompt. It runs on every route, direct included: a preference
// such as "always ask before sending mail" matters most on a turn that runs
// tools, and a fact about a person matters on a direct question about
// them. It costs one embedding of the query.
//
// It returns "" when the agent has no profile, nothing comes back, or
// recall fails. A failure is a warning, not the turn's error: the answer
// can still come from the rest of the prompt.
func (a *Agent) memorySection(ctx context.Context, query string) string {
	if a.profile == nil {
		return ""
	}
	start := time.Now()
	mems, err := a.profile.Recall(ctx, query)
	if err != nil {
		if ctx.Err() == nil {
			a.log.WarnContext(ctx, "recall failed; answering without recalled memories", "err", err)
		}
		return ""
	}
	section, dropped := formatMemories(mems, maxMemoryChars)
	a.log.DebugContext(ctx, "memories recalled", "memories", len(mems)-dropped, "left_out", dropped,
		"chars", utf8.RuneCountInString(section), "ms", time.Since(start).Milliseconds())
	return section
}

// formatMemories turns mems into the recalled-memories section:
// memoryHeader, then one "- (kind) text" line per memory, best first. The
// kind tells the model what sort of fact it reads: "(people) Sam is the
// user's manager". A memory's text becomes one line.
//
// The section stays within limit characters. A memory whose line doesn't
// fit is left out and counted in dropped, and a shorter one after it may
// still fit. It returns "" when no memory fits or has any text.
func formatMemories(mems []retrieve.Memory, limit int) (section string, dropped int) {
	lines := []string{memoryHeader}
	used := utf8.RuneCountInString(memoryHeader)
	for _, m := range mems {
		text := strings.Join(strings.Fields(m.Text), " ")
		if text == "" {
			continue
		}
		line := "- (" + m.Kind + ") " + text
		// +1 for the line break before the line.
		n := 1 + utf8.RuneCountInString(line)
		if used+n > limit {
			dropped++
			continue
		}
		used += n
		lines = append(lines, line)
	}
	if len(lines) == 1 {
		return "", dropped
	}
	return strings.Join(lines, "\n"), dropped
}

// recordMemoryTokens records the two memory sections, the profile and the
// recalled memories, as one meru.context.tokens value with section
// "memories", so the metric reads as the prompt's whole share of memory.
// A turn with neither section records nothing.
func recordMemoryTokens(ctx context.Context, profile, recalled string) {
	chars := utf8.RuneCountInString(profile) + utf8.RuneCountInString(recalled)
	if chars > 0 {
		obs.RecordContextTokens(ctx, "memories", chars/4)
	}
}
