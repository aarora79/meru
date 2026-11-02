// This file makes sure an answer links each file the turn saved. A real
// session showed why: write_file saved a report and handed the model a
// link, the model answered "Click the link above", pointing at a tool
// result the user never sees, and asked for the link on the next turn, it
// made up a file name that didn't exist. Meru knows which files it wrote,
// so it adds the links itself when the answer leaves them out.

package agent

import (
	"net/url"
	"slices"
	"strings"
)

// savedFile is one file write_file wrote this turn: its name and its
// file:// link, as builtin.Written read them from the tool's result.
type savedFile struct {
	name, link string
}

// savedLinks returns the line to add under answer for the files in saved
// that answer doesn't link, such as
//
//	Saved: [report.html](file:///Users/you/meru-output/report.html)
//
// with two blank lines before it, or "" when the answer links them all or
// the turn saved nothing. A file counts as linked when the answer holds
// its link or its full path. A file written twice in one turn is listed
// once.
func savedLinks(saved []savedFile, answer string) string {
	var links, seen []string
	for _, f := range saved {
		if slices.Contains(seen, f.link) {
			continue
		}
		seen = append(seen, f.link)
		if strings.Contains(answer, f.link) {
			continue
		}
		// url.Parse undoes the escaping, so a path with a space matches the
		// answer's plain text.
		if u, err := url.Parse(f.link); err == nil && u.Path != "" && strings.Contains(answer, u.Path) {
			continue
		}
		links = append(links, "["+f.name+"]("+f.link+")")
	}
	if len(links) == 0 {
		return ""
	}
	return "\n\nSaved: " + strings.Join(links, ", ")
}
