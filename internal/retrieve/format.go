// This file turns results into text for the model: one citation line per
// chunk, and a prompt section that numbers the chunks so the answer can
// cite them as [1], [2] and so on.

package retrieve

import (
	"fmt"
	"strings"
)

// Cite returns the citation line for result r, numbered n:
//
//	[1] notes/budget.md, "Q3 budget", lines 12–40
//
// The heading appears when the chunk has one. Text files give a line range
// (or "line 7" for one line) and PDFs give a page. The path is the one the
// store holds.
func Cite(n int, r Result) string {
	// strings.Builder collects pieces of a string without copying the whole
	// thing on each addition.
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] %s", n, r.Path)
	if r.Heading != "" {
		fmt.Fprintf(&b, ", %q", r.Heading)
	}
	switch {
	case r.StartLine > 0 && r.EndLine > r.StartLine:
		fmt.Fprintf(&b, ", lines %d–%d", r.StartLine, r.EndLine)
	case r.StartLine > 0:
		fmt.Fprintf(&b, ", line %d", r.StartLine)
	case r.Page > 0:
		fmt.Fprintf(&b, ", page %d", r.Page)
	}
	return b.String()
}

// Format returns the prompt section that hands results to the model: a
// line telling it how to cite, then each chunk under its citation line,
// numbered from 1 in result order. It returns "" when there are no results,
// so the agent can leave the section out.
func Format(results []Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Excerpts from the user's files. Cite the ones you use by number, like [1].\n")
	for i, r := range results {
		b.WriteString("\n")
		b.WriteString(Cite(i+1, r))
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(r.Text))
		b.WriteString("\n")
	}
	return b.String()
}
