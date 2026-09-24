// This file holds the two helpers both clients use to show a turn's
// sources: String, which writes one citation as a line of text, and Cited,
// which picks the sources an answer refers to.

package rpc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// String returns the citation as one line of plain text, numbered the way
// the answer cites it:
//
//	[1] ~/notes/garden.md, "Budget", lines 3–8
//
// The heading appears when the excerpt has one. A text file gives a line
// range (or "line 7" for one line) and a PDF gives a page. Having a String
// method makes Citation print this way with fmt's %s and %v too.
func (c Citation) String() string {
	// strings.Builder collects pieces of a string without copying the whole
	// thing on each addition.
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] %s", c.N, c.Path)
	if c.Heading != "" {
		fmt.Fprintf(&b, ", %q", c.Heading)
	}
	switch {
	case c.StartLine > 0 && c.EndLine > c.StartLine:
		fmt.Fprintf(&b, ", lines %d–%d", c.StartLine, c.EndLine)
	case c.StartLine > 0:
		fmt.Fprintf(&b, ", line %d", c.StartLine)
	case c.Page > 0:
		fmt.Fprintf(&b, ", page %d", c.Page)
	}
	return b.String()
}

// citeMarks matches a citation mark in an answer: "[1]", or several numbers
// in one pair of brackets, "[1, 3]". Cited compiles it on each call rather
// than keep the compiled pattern in a package-level variable, which
// AGENTS.md rules out; one compile per answer costs microseconds.
const citeMarks = `\[(\d+(?:\s*,\s*\d+)*)\]`

// Cited returns the sources that answer cites by number, in the order of
// sources. When the answer cites none of them, as a small model sometimes
// forgets to, it returns all of them: the answer was still written with
// those excerpts in front of the model.
func Cited(answer string, sources []Citation) []Citation {
	re := regexp.MustCompile(citeMarks)
	cited := map[int]bool{}
	for _, m := range re.FindAllStringSubmatch(answer, -1) {
		// m[1] is the text inside the brackets, such as "1, 3".
		for _, part := range strings.Split(m[1], ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				cited[n] = true
			}
		}
	}
	var out []Citation
	for _, s := range sources {
		if cited[s.N] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return sources
	}
	return out
}
