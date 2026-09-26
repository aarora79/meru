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
	"github.com/aarora79/meru/internal/rpc"
)

// memoryHeader opens the recalled-memories section.
const memoryHeader = "Things you remember that may matter here:"

// memorySection recalls the memories that fit query and formats them for
// the system prompt. It runs on every route, direct included: a preference
// such as "always ask before sending mail" matters most on a turn that runs
// tools, and a fact about a person matters on a direct question about
// them. It costs one embedding of the query.
//
// It also returns the memories the section holds, in the protocol's shape,
// for the "memories" event that tells the client what this answer
// remembered, so the user can see each one and forget it.
//
// It returns "" and no memories when the agent has no profile, nothing
// comes back, or recall fails. A failure is a warning, not the turn's
// error: the answer can still come from the rest of the prompt.
func (a *Agent) memorySection(ctx context.Context, query string) (string, []rpc.MemoryInfo) {
	if a.profile == nil {
		return "", nil
	}
	start := time.Now()
	mems, err := a.profile.Recall(ctx, query)
	if err != nil {
		if ctx.Err() == nil {
			a.log.WarnContext(ctx, "recall failed; answering without recalled memories", "err", err)
		}
		return "", nil
	}
	section, dropped, kept := formatMemories(mems, maxMemoryChars)
	a.log.DebugContext(ctx, "memories recalled", "memories", len(mems)-dropped, "left_out", dropped,
		"chars", utf8.RuneCountInString(section), "ms", time.Since(start).Milliseconds())
	infos := make([]rpc.MemoryInfo, 0, len(kept))
	for _, m := range kept {
		info := rpc.MemoryInfo{ID: m.MemID, Kind: m.Kind, Text: m.Text, Source: m.Source}
		if !m.Created.IsZero() {
			info.Created = m.Created.Format("2006-01-02")
		}
		infos = append(infos, info)
	}
	return section, infos
}

// formatMemories turns mems into the recalled-memories section:
// memoryHeader, then one "- (kind) text" line per memory, best first. The
// kind tells the model what sort of fact it reads: "(people) Sam is the
// user's manager". A memory's text becomes one line.
//
// The section stays within limit characters. A memory whose line doesn't
// fit is left out and counted in dropped, and a shorter one after it may
// still fit. kept lists the memories the section holds, in its order. It
// returns "" when no memory fits or has any text.
func formatMemories(mems []retrieve.Memory, limit int) (section string, dropped int, kept []retrieve.Memory) {
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
		kept = append(kept, m)
	}
	if len(lines) == 1 {
		return "", dropped, nil
	}
	return strings.Join(lines, "\n"), dropped, kept
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
