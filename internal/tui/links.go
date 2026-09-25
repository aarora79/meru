// This file makes the web and file links in a finished answer clickable. It
// swaps each link's URL for a marker before Glamour draws the answer, then
// swaps each marker for a short form of the URL, wrapped in the escape codes
// that make it a link, the same trick code.go uses for the copy labels.

package tui

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/aarora79/meru/internal/rpc"
)

// answerLink is one link in an answer's Markdown that the chat draws as a
// clickable URL.
type answerLink struct {
	// start and end are the byte offsets of the part of the source that
	// markLinks replaces: the destination and the ")" after it for a
	// Markdown link, [text](url), or the whole URL, with its angle brackets
	// when it has them, for <url> and for a bare URL in the text.
	start, end int
	// inline is true for a Markdown link, whose text stays as written.
	inline bool
	// url is the full URL the link opens.
	url string
	// depth counts the lists, quotes and headings round the link. Each one
	// indents the text, which leaves the URL less room on a line.
	depth int
}

// linkSwap pairs a marker in the source Glamour draws with what replaces it
// on screen.
type linkSwap struct {
	marker string
	text   string
}

// markdownParser returns a goldmark parser set up as Glamour sets up its
// own: CommonMark plus the GitHub extensions and definition lists. GitHub's
// extensions include Linkify, which turns a bare http or https URL in the
// text into a link, so a parser without them would miss those URLs, and
// both parsers must agree on where each link sits.
func markdownParser() goldmark.Markdown {
	return goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList))
}

// findLinks returns the links in the Markdown src, in the order they
// appear: Markdown links, [text](url); autolinks, <url>; and bare URLs in
// the text. It keeps only http, https and file URLs. It leaves out links
// in code, in images and in tables, because Glamour lists a table's links
// under the table on its own, and links whose place in the source it
// can't find, such as a reference link, [text][ref], whose URL sits
// elsewhere. Glamour then draws those as it always does.
func findLinks(src string) []answerLink {
	source := []byte(src)
	doc := markdownParser().Parser().Parse(text.NewReader(source))

	var links []answerLink
	// last is where the previous link ended. Links come in the order they
	// appear, so each search starts from there and no two spans overlap.
	last := 0
	walk := func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var l answerLink
		var ok bool
		switch n.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindCodeSpan,
			ast.KindHTMLBlock, ast.KindRawHTML, ast.KindImage, east.KindTable:
			return ast.WalkSkipChildren, nil
		case ast.KindLink:
			// n.(*ast.Link) is a type assertion: it says which concrete
			// type sits behind the ast.Node interface, so the code can
			// read the link's own fields.
			l, ok = inlineLink(src, n.(*ast.Link), last)
		case ast.KindAutoLink:
			l, ok = autoLink(src, n.(*ast.AutoLink), last)
		default:
			return ast.WalkContinue, nil
		}
		if ok {
			l.depth = depth(n)
			links = append(links, l)
			last = l.end
		}
		// A link's children are its text, which holds no other link.
		return ast.WalkSkipChildren, nil
	}
	// The walk function never returns an error, so Walk can't either.
	_ = ast.Walk(doc, walk)
	return links
}

// inlineLink finds the destination of the Markdown link n in src, at or
// after from. It returns false for a reference link, for a URL that isn't
// http, https or file, and when the destination isn't in src as written,
// followed at once by ")". That leaves out a link with a title,
// [text](url "title"), and one whose URL uses escapes, which the parser
// has already undone.
func inlineLink(src string, n *ast.Link, from int) (answerLink, bool) {
	dest := string(n.Destination)
	if n.Reference != nil || !linkable(dest) {
		return answerLink{}, false
	}
	// Pos is where the link starts in the source, at its "[".
	from = max(from, n.Pos())
	for {
		i := strings.Index(src[from:], dest)
		if i < 0 {
			return answerLink{}, false
		}
		at := from + i
		after := at + len(dest)
		switch {
		case strings.HasSuffix(src[:at], "](") && strings.HasPrefix(src[after:], ")"):
			return answerLink{start: at, end: after + 1, inline: true, url: dest}, true
		case strings.HasSuffix(src[:at], "](<") && strings.HasPrefix(src[after:], ">)"):
			return answerLink{start: at - 1, end: after + 2, inline: true, url: dest}, true
		}
		from = after
	}
}

// autoLink finds the autolink n, <url> or a bare URL, in src at or after
// from. It returns false for an email address and for a URL that isn't
// http, https or file.
func autoLink(src string, n *ast.AutoLink, from int) (answerLink, bool) {
	if n.AutoLinkType != ast.AutoLinkURL {
		return answerLink{}, false
	}
	source := []byte(src)
	// Label is the URL as written; URL adds "http://" to a bare
	// www.example.com.
	label := string(n.Label(source))
	u := string(n.URL(source))
	if !linkable(u) {
		return answerLink{}, false
	}
	from = max(from, n.Pos())
	i := strings.Index(src[from:], label)
	if i < 0 {
		return answerLink{}, false
	}
	start, end := from+i, from+i+len(label)
	if start > 0 && src[start-1] == '<' && strings.HasPrefix(src[end:], ">") {
		start, end = start-1, end+1
	}
	return answerLink{start: start, end: end, url: u}, true
}

// linkable reports whether u is a URL the chat links: http, https or file,
// with nothing in it that could break the escape codes round a link, such
// as a space or a control character. A javascript: or mailto: URL stays as
// Glamour draws it.
func linkable(u string) bool {
	for _, r := range u {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "file":
		return true
	}
	return false
}

// depth counts the lists, quotes, definitions and headings that hold n.
func depth(n ast.Node) int {
	d := 0
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case ast.KindListItem, ast.KindBlockquote, ast.KindHeading, east.KindDefinitionDescription:
			d++
		}
	}
	return d
}

// linkIndent is how many columns each list, quote or heading round a link
// may take from its line. Glamour indents a nested list by two columns and
// puts a two-column bullet or bar in front; four covers both.
const linkIndent = 4

// minLinkRoom is the fewest columns a link needs. Below it, the chat leaves
// the link as Glamour draws it.
const minLinkRoom = 12

// linkRoom returns how many columns a URL at the given depth may take on a
// screen width columns wide: the width less Glamour's margin of
// answerIndent on each side and linkIndent per level. It errs short, so
// the URL never runs past the edge; a URL cut a few columns early costs
// less than one cut off by the screen.
func linkRoom(width, depth int) int {
	return width - 2*answerIndent - linkIndent*depth
}

// linkMarker returns the marker for piece n of the answer's links, at
// least width columns wide. It reads as a URL, "MERULINK:3Xxxxx", so
// Glamour keeps it as a link's destination, and holds no "." or "-", so
// Glamour never breaks it across lines. The x's pad it to the width of
// what replaces it, so Glamour wraps the line round the marker as it
// would round the URL.
func linkMarker(n, width int) string {
	m := fmt.Sprintf("MERULINK:%dX", n)
	if pad := width - len(m); pad > 0 {
		m += strings.Repeat("x", pad)
	}
	return m
}

// markLinks returns src with each link's URL swapped for a marker, and the
// swaps that put the URLs back after Glamour has drawn it. A Markdown link
// keeps its text, so the screen shows the text and then the URL, as
// Glamour draws one.
//
// With clickable on, each URL becomes one marker, and its swap is the URL
// shortened to fit the room on its line (shortURL) inside an OSC 8 link to
// the full URL. With clickable off, as with NO_COLOR, nothing on screen
// can open a link, so the swap is the full URL as text. A URL longer than
// the line goes in pieces as wide as the room, one to a line, so no piece
// runs past the edge and none breaks at a "." as Glamour breaks a URL.
//
// A link too deep in lists and quotes for the screen keeps its source as
// written.
func markLinks(src string, links []answerLink, width int, clickable bool) (string, []linkSwap) {
	var b strings.Builder
	var swaps []linkSwap
	last := 0
	for _, l := range links {
		room := linkRoom(width, l.depth)
		if room < minLinkRoom {
			continue
		}
		b.WriteString(src[last:l.start])
		var pieces []string
		if clickable {
			pieces = []string{shortURL(l.url, room)}
		} else {
			pieces = splitWidth(l.url, room)
		}
		for i, p := range pieces {
			mk := linkMarker(len(swaps)+1, ansi.StringWidth(p))
			shown := p
			if clickable {
				shown = rpc.Hyperlink(l.url, p)
			}
			swaps = append(swaps, linkSwap{marker: mk, text: shown})
			switch {
			case i > 0:
				b.WriteString(" <" + mk + ">")
			case l.inline:
				b.WriteString(mk + ")")
			default:
				b.WriteString("<" + mk + ">")
			}
		}
		last = l.end
	}
	b.WriteString(src[last:])
	return b.String(), swaps
}

// swapLinks replaces each marker in Glamour's output with its text. It
// returns false when a marker doesn't show exactly once, or sits on a line
// wider than width, which means Glamour drew the answer in a way markLinks
// didn't plan for. The caller then draws the answer without the links, so
// no stray marker reaches the screen.
func swapLinks(rendered string, swaps []linkSwap, width int) (string, bool) {
	for _, s := range swaps {
		if strings.Count(rendered, s.marker) != 1 {
			return "", false
		}
		i := strings.Index(rendered, s.marker)
		lineStart := strings.LastIndexByte(rendered[:i], '\n') + 1
		lineEnd := strings.IndexByte(rendered[i:], '\n')
		if lineEnd < 0 {
			lineEnd = len(rendered)
		} else {
			lineEnd += i
		}
		if ansi.StringWidth(rendered[lineStart:lineEnd]) > width {
			return "", false
		}
	}
	for _, s := range swaps {
		rendered = strings.Replace(rendered, s.marker, s.text, 1)
	}
	return rendered, true
}

// shortURL returns u as the screen shows it, at most width columns wide:
// without "https://", "http://" or "file://", without a "/" at the end,
// and cut with "…" when it is still too long. Cutting from the end keeps
// the host, which says where the link goes, and as much of the path as
// fits.
func shortURL(u string, width int) string {
	s := u
	for _, scheme := range []string{"https://", "http://", "file://"} {
		if len(s) >= len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			s = s[len(scheme):]
			break
		}
	}
	if len(s) > 1 {
		s = strings.TrimSuffix(s, "/")
	}
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// splitWidth cuts s into pieces at most width columns wide, in order.
func splitWidth(s string, width int) []string {
	var pieces []string
	for ansi.StringWidth(s) > width {
		pieces = append(pieces, ansi.Truncate(s, width, ""))
		s = ansi.TruncateLeft(s, width, "")
	}
	return append(pieces, s)
}
