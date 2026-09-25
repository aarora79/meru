// This file chunks Markdown: one section per heading, the heading path as
// each chunk's Heading, and fenced code blocks kept whole.

package index

import (
	"strings"

	"github.com/aarora79/meru/internal/store"
)

// mdSection is the text under one heading, up to the next heading of any
// level.
type mdSection struct {
	heading string // the heading path, such as "Garden > Spring"; "" before the first heading
	title   span   // the heading line itself; empty before the first heading
	body    span   // everything after the heading line
}

// headingSep joins the titles in a heading path.
const headingSep = " > "

// chunkMarkdown splits src into sections by ATX heading ("# Title" to
// "###### Title"), then packs each section's paragraphs into chunks. Chunks
// never cross a section boundary, so each carries one heading path. A
// fenced code block counts as one paragraph, even with blank lines inside,
// so pack splits it only when it alone passes the size limit.
//
// A heading followed by nothing but another heading produces no chunk: its
// title already appears in the path of the sections below it. Setext
// headings (a title underlined with === or ---) count as plain text.
func chunkMarkdown(src string, lim limits) []store.Chunk {
	lines := newLineIndex(src)
	var out []store.Chunk
	for _, sec := range mdSections(src) {
		out = append(out, packSection(src, sec, mdParagraphs(src, sec.body), lim, lines)...)
	}
	return out
}

// packSection packs one section's paragraphs, units, into chunks under the
// section's heading path. The heading line leads the first chunk, so the
// chunk reads the way the file does. A section with no paragraphs produces
// nothing. Markdown and HTML both use it.
func packSection(src string, sec mdSection, units []span, lim limits, lines lineIndex) []store.Chunk {
	if len(units) == 0 {
		return nil
	}
	if sec.title.end > sec.title.start {
		units = append([]span{sec.title}, units...)
	}
	return toChunks(src, pack(src, units, lim), sec.heading, lines)
}

// mdSections cuts src at every heading outside a code fence and works out
// each section's heading path from the heading levels.
func mdSections(src string) []mdSection {
	// titles[i] holds the current title at heading level i+1, so the path
	// for a level-3 heading is titles[0..2] with empty levels left out.
	var titles [6]string
	var secs []mdSection
	cur := mdSection{}
	var fence fenceState
	lineStart := 0
	for lineStart <= len(src) {
		lineEnd := strings.IndexByte(src[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(src)
		} else {
			lineEnd += lineStart
		}
		line := src[lineStart:lineEnd]
		if fence.update(line) {
			// Inside a fence, or on its opening or closing line: never a heading.
		} else if level, title, ok := atxHeading(line); ok {
			cur.body.end = lineStart
			secs = append(secs, cur)
			titles[level-1] = title
			for i := level; i < len(titles); i++ {
				titles[i] = ""
			}
			cur = mdSection{
				heading: headingPath(titles[:level]),
				title:   span{lineStart, lineEnd},
				body:    span{lineEnd, lineEnd},
			}
		}
		lineStart = lineEnd + 1
	}
	cur.body.end = len(src)
	return append(secs, cur)
}

// headingPath joins the non-empty titles with " > ".
func headingPath(titles []string) string {
	var parts []string
	for _, t := range titles {
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, headingSep)
}

// atxHeading reports whether line is a heading such as "## Title", and
// returns its level (1 to 6) and title. Up to three leading spaces are
// allowed, the "#" run must be followed by a space or the end of the line,
// and a closing run of "#" is dropped, as CommonMark specifies.
func atxHeading(line string) (level int, title string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0, "", false
	}
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest := trimmed[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false // "#hashtag", not a heading
	}
	title = strings.TrimSpace(rest)
	if closing := strings.TrimRight(title, "#"); closing == "" || strings.HasSuffix(closing, " ") {
		title = strings.TrimSpace(closing)
	}
	return level, title, true
}

// fenceState tracks whether a line sits inside a fenced code block: one that
// opens with three or more backticks or tildes and closes with at least as
// many of the same character.
type fenceState struct {
	char byte // '`' or '~' while inside a fence; 0 outside
	n    int  // how many of char opened the fence
}

// update reads one line and reports whether it belongs to a fence: its
// opening line, a line inside it, or its closing line. A pointer receiver
// (f *fenceState) lets the method change the fenceState it's called on.
func (f *fenceState) update(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return f.char != 0
	}
	run := 0
	for run < len(trimmed) && (trimmed[run] == '`' || trimmed[run] == '~') && trimmed[run] == trimmed[0] {
		run++
	}
	if f.char != 0 {
		// A closing fence: the same character, at least as long, nothing after.
		if run >= f.n && trimmed[0] == f.char && strings.TrimSpace(trimmed[run:]) == "" {
			f.char, f.n = 0, 0
		}
		return true
	}
	if run >= 3 {
		f.char, f.n = trimmed[0], run
		return true
	}
	return false
}

// mdParagraphs splits a section body into paragraphs, treating a fenced
// code block as part of one paragraph however many blank lines it holds.
func mdParagraphs(src string, body span) []span {
	var out []span
	var fence fenceState
	start := -1
	lineStart := body.start
	for lineStart < body.end {
		lineEnd := strings.IndexByte(src[lineStart:body.end], '\n')
		if lineEnd < 0 {
			lineEnd = body.end
		} else {
			lineEnd += lineStart
		}
		line := src[lineStart:lineEnd]
		inFence := fence.update(line)
		blank := strings.TrimSpace(line) == ""
		switch {
		case blank && !inFence && start >= 0:
			out = append(out, span{start, lineStart})
			start = -1
		case !blank && start < 0:
			start = lineStart
		}
		lineStart = lineEnd + 1
	}
	if start >= 0 {
		out = append(out, span{start, body.end})
	}
	return out
}
