// This file holds the chunking machinery every file kind shares: the token
// estimate, byte ranges ("spans") over a file's text, and pack, which groups
// small pieces such as paragraphs into chunks of about chunk_tokens each,
// with overlap. The per-kind chunkers in markdown.go, code.go, html.go and
// pdf.go decide where the pieces are; pack decides how they group.

package index

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/store"
)

// charsPerToken is the token estimate: one token per four characters. Real
// tokenizers average three to four characters per English token, so the
// estimate errs toward smaller chunks. Meru doesn't run the embedding
// model's own tokenizer because Ollama doesn't expose it, and a chunk a few
// tokens off target does no harm.
const charsPerToken = 4

// estimateTokens returns the estimated token count of s: its characters
// (Unicode code points, not bytes) divided by four, rounded up.
func estimateTokens(s string) int {
	return (utf8.RuneCountInString(s) + charsPerToken - 1) / charsPerToken
}

// limits holds the chunk size and overlap in characters, converted once
// from the config's token counts.
type limits struct {
	max     int // most characters in one chunk
	overlap int // characters each chunk repeats from the one before
}

// newLimits converts [index] chunk_tokens and overlap_tokens to characters.
func newLimits(chunkTokens, overlapTokens int) limits {
	return limits{max: chunkTokens * charsPerToken, overlap: overlapTokens * charsPerToken}
}

// span is a byte range [start, end) of a file's text. Chunkers work in spans
// so each chunk's text is an exact slice of the source, which keeps line
// numbers right.
type span struct{ start, end int }

// chars counts the characters in src's span s.
func chars(src string, s span) int {
	return utf8.RuneCountInString(src[s.start:s.end])
}

// pack groups units, pieces of src in order such as paragraphs, into chunk
// spans of at most lim.max characters. It adds whole units to a chunk while
// they fit, and starts a new chunk when the next one doesn't. A unit too big
// for a chunk by itself gets split first (see split), so no chunk passes the
// limit.
//
// Each new chunk starts with the last lim.overlap characters of the chunk
// before it, cut at a word boundary, when that still fits. A sentence cut at
// a chunk boundary then appears whole in at least one chunk.
func pack(src string, units []span, lim limits) []span {
	var out []span
	var cur span
	have := false // whether cur holds a chunk in progress
	for _, big := range units {
		for _, u := range split(src, big, lim.max) {
			if !have {
				cur, have = u, true
				continue
			}
			if chars(src, span{cur.start, u.end}) <= lim.max {
				cur.end = u.end
				continue
			}
			out = append(out, cur)
			prev := cur
			cur = u
			if o := overlapStart(src, prev, lim.overlap); o < u.start && chars(src, span{o, u.end}) <= lim.max {
				cur.start = o
			}
		}
	}
	if have {
		out = append(out, cur)
	}
	return out
}

// split cuts u into pieces of at most maxChars characters: at line breaks first,
// then between words, and as a last resort every maxChars characters (a long
// line of minified JSON has no better place). A unit that already fits comes
// back whole.
func split(src string, u span, maxChars int) []span {
	if chars(src, u) <= maxChars {
		return []span{u}
	}
	if lines := lineSpans(src, u); len(lines) > 1 {
		return pack(src, lines, limits{max: maxChars})
	}
	if words := wordSpans(src, u); len(words) > 1 {
		return pack(src, words, limits{max: maxChars})
	}
	return hardCut(src, u, maxChars)
}

// lineSpans returns the non-blank lines of src inside u, without their line
// breaks.
func lineSpans(src string, u span) []span {
	var out []span
	start := u.start
	for i := u.start; i <= u.end; i++ {
		if i == u.end || src[i] == '\n' {
			if strings.TrimSpace(src[start:i]) != "" {
				out = append(out, span{start, i})
			}
			start = i + 1
		}
	}
	return out
}

// wordSpans returns the runs of non-space characters in src inside u.
func wordSpans(src string, u span) []span {
	var out []span
	start := -1
	// Ranging over a string yields each character (rune) and its byte offset.
	for i, r := range src[u.start:u.end] {
		i += u.start
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, span{start, i})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, span{start, u.end})
	}
	return out
}

// hardCut cuts u every maxChars characters, never inside a multi-byte character.
func hardCut(src string, u span, maxChars int) []span {
	var out []span
	start, n := u.start, 0
	for i := range src[u.start:u.end] {
		if n == maxChars {
			out = append(out, span{start, u.start + i})
			start, n = u.start+i, 0
		}
		n++
	}
	return append(out, span{start, u.end})
}

// overlapStart returns where the overlap for the chunk after prev begins:
// about n characters before prev's end, moved forward to the start of a
// word. It returns prev.end, meaning no overlap, when n is 0 or prev holds
// no word boundary in that stretch.
func overlapStart(src string, prev span, n int) int {
	if n <= 0 {
		return prev.end
	}
	// Step back n characters, one rune at a time.
	p := prev.end
	for k := 0; k < n && p > prev.start; k++ {
		_, size := utf8.DecodeLastRuneInString(src[:p])
		p -= size
	}
	// Unless p already sits at the start of a word, skip the rest of the
	// word it landed in.
	if p > prev.start {
		if r, _ := utf8.DecodeLastRuneInString(src[:p]); !unicode.IsSpace(r) {
			for p < prev.end {
				r, size := utf8.DecodeRuneInString(src[p:])
				if unicode.IsSpace(r) {
					break
				}
				p += size
			}
		}
	}
	// Then skip the spaces in front of the next word.
	for p < prev.end {
		r, size := utf8.DecodeRuneInString(src[p:])
		if !unicode.IsSpace(r) {
			break
		}
		p += size
	}
	return p
}

// paragraphs splits src inside u into runs of non-blank lines. A blank
// line, or one holding only spaces, ends a paragraph.
func paragraphs(src string, u span) []span {
	var out []span
	start := -1 // start of the paragraph in progress, or -1 between paragraphs
	lineStart := u.start
	for i := u.start; i <= u.end; i++ {
		if i < u.end && src[i] != '\n' {
			continue
		}
		blank := strings.TrimSpace(src[lineStart:i]) == ""
		switch {
		case blank && start >= 0:
			out = append(out, span{start, lineStart})
			start = -1
		case !blank && start < 0:
			start = lineStart
		}
		lineStart = i + 1
	}
	if start >= 0 {
		out = append(out, span{start, u.end})
	}
	return out
}

// lineIndex holds the byte offset where each line of a text starts, so a
// chunk's byte offsets turn into line numbers with a binary search.
type lineIndex []int

// newLineIndex records the start of every line in src.
func newLineIndex(src string) lineIndex {
	idx := lineIndex{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

// line returns the 1-based line number holding byte offset off.
func (li lineIndex) line(off int) int {
	// sort.Search finds the first line that starts after off; the lines
	// before it number exactly the line off sits on.
	return sort.Search(len(li), func(i int) bool { return li[i] > off })
}

// toChunks turns spans of src into chunks under heading. Each chunk's text
// is its span with the surrounding white space trimmed. When lines is
// non-nil, the chunks get StartLine and EndLine; PDF and HTML text don't map
// to lines in the file, so they pass nil. Blank spans produce nothing.
func toChunks(src string, spans []span, heading string, lines lineIndex) []store.Chunk {
	var out []store.Chunk
	for _, s := range spans {
		s = trimSpan(src, s)
		if s.start >= s.end {
			continue
		}
		c := store.Chunk{Heading: heading, Text: src[s.start:s.end]}
		if lines != nil {
			c.StartLine = lines.line(s.start)
			c.EndLine = lines.line(s.end - 1)
		}
		out = append(out, c)
	}
	return out
}

// trimSpan moves s's ends inwards past white space.
func trimSpan(src string, s span) span {
	for s.start < s.end {
		r, size := utf8.DecodeRuneInString(src[s.start:s.end])
		if !unicode.IsSpace(r) {
			break
		}
		s.start += size
	}
	for s.end > s.start {
		r, size := utf8.DecodeLastRuneInString(src[s.start:s.end])
		if !unicode.IsSpace(r) {
			break
		}
		s.end -= size
	}
	return s
}

// chunkText chunks plain text, and source code other than Go: paragraphs
// (blocks between blank lines), packed with overlap, with line numbers.
func chunkText(src string, lim limits) []store.Chunk {
	all := span{0, len(src)}
	return toChunks(src, pack(src, paragraphs(src, all), lim), "", newLineIndex(src))
}
