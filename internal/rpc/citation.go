// This file holds the helpers both clients use to show a turn's sources:
// String, which writes one citation as a line of text; Cited, which picks
// the sources an answer refers to; FileURL and Hyperlink, which make a
// source line a link to its file; and ShortPath, which merud uses to show a
// path under the home folder as ~/...

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
//	[1] ~/notes/garden.md, "Planting", lines 3–8
//
// The heading appears when the excerpt has one. A text file gives a line
// range (or "line 7" for one line) and a PDF gives a page; an image says
// "(image)" after its path; a web page gives its title, then its URL:
//
//	[5] Release History — https://go.dev/doc/devel/release
//
// Having a String method makes Citation print this way with fmt's %s and
// %v too.
func (c Citation) String() string {
	if c.Kind == SourceWeb && c.URL != "" {
		if c.Heading == "" || c.Heading == c.URL {
			return fmt.Sprintf("[%d] %s", c.N, c.URL)
		}
		return fmt.Sprintf("[%d] %s — %s", c.N, c.Heading, c.URL)
	}
	// strings.Builder collects pieces of a string without copying the whole
	// thing on each addition.
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] %s", c.N, c.Path)
	// An image's heading would only repeat its name.
	if c.Kind == "image" {
		b.WriteString(" (image)")
		return b.String()
	}
	if c.Heading != "" {
		fmt.Fprintf(&b, ", %q", c.Heading)
	}
	line, lines := "line", "lines"
	if c.Kind == SourceTable {
		line, lines = "row", "rows"
	}
	switch {
	case c.StartLine > 0 && c.EndLine > c.StartLine:
		fmt.Fprintf(&b, ", %s %d–%d", lines, c.StartLine, c.EndLine)
	case c.StartLine > 0:
		fmt.Fprintf(&b, ", %s %d", line, c.StartLine)
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

// Link returns the address a source line links to: a web page's URL, or
// the file:// URL of a file, from FileURL with home. It returns "" when
// there is none.
func (c Citation) Link(home string) string {
	if c.Kind == SourceWeb {
		return c.URL
	}
	return FileURL(c.Path, home)
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

// URLPath returns the path a parsed file:// URL names, with forward
// slashes, the reverse of FileURL. A Windows drive can land in two places,
// and both keep it:
//
//   - file:///C:/Users/x, which FileURL writes, parses to the path
//     /C:/Users/x, so the leading slash goes.
//   - file://C:/Users/x, which Go's url.URL writes for a path that starts
//     with a drive, and which models write too, parses with the drive as
//     the host: host C: and path /Users/x. The drive goes back in front.
//
// The caller turns the slashes into the system's with filepath.FromSlash.
func URLPath(u *url.URL) string {
	p := u.Path
	switch {
	case isDrive(u.Host):
		p = u.Host + p
	case len(p) > 2 && p[0] == '/' && isDrive(p[1:3]):
		p = p[1:]
	}
	return p
}

// isDrive reports whether s is a Windows drive, a letter and a colon such
// as "C:".
func isDrive(s string) bool {
	if len(s) != 2 || s[1] != ':' {
		return false
	}
	c := s[0] | 0x20 // 0x20 turns an upper-case ASCII letter into lower case
	return c >= 'a' && c <= 'z'
}

// ShortPath writes p under the home folder as ~/..., using the OS's path
// separator, the way merud shows a source's path. Any other path, or any
// path when home is "", stays as it is. FileURL undoes it.
func ShortPath(home, p string) string {
	if home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return "~" + string(filepath.Separator) + rel
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
