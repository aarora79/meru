// This file holds the helpers both clients use to show a turn's sources:
// String, which writes one citation as a line of text; Cited, which picks
// the sources an answer refers to; and FileURL and Hyperlink, which make a
// source line a link to its file.

package rpc

import (
	"fmt"
	"net/url"
	"path/filepath"
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
// sources. An answer that cites none gets none: the model decides when a
// source matters, and a list of excerpts it didn't use only adds noise
// under a general answer. Before, such an answer listed every excerpt, in
// case a small model had forgotten to cite; in use that printed sources
// under answers that never touched them.
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
	return out
}

// FileURL turns a citation's path into a file:// URL a terminal can open.
// A path that starts with "~" plus the separator gets home in its place,
// the reverse of how merud shortens paths for display. It returns "" when
// the path is "~/..." and home is "", since the URL would point nowhere.
// url.URL escapes what a URL can't hold, so "My Notes" becomes
// "My%20Notes".
func FileURL(path, home string) string {
	if rest, ok := strings.CutPrefix(path, "~"+string(filepath.Separator)); ok {
		if home == "" {
			return ""
		}
		path = filepath.Join(home, rest)
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // a Windows path, C:/Users/..., becomes /C:/Users/...
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// Hyperlink wraps text in the OSC 8 escape codes that make it a link in
// terminals that know them (iTerm2, Ghostty, WezTerm, kitty, VS Code's
// terminal, Windows Terminal). Other terminals skip the codes and show text
// as it is. It returns text alone when url is "". Callers use it only when
// the output is a terminal with styling on, so a pipe or a file gets plain
// text.
func Hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	// ESC ] 8 ; ; URL ESC \ opens the link; the same with no URL closes it.
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
