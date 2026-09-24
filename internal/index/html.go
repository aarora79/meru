// This file chunks HTML: it strips the tags, keeps the text and the
// headings, and packs the paragraphs under each heading like Markdown.

package index

import (
	"strings"

	"github.com/aarora79/meru/internal/store"
	"golang.org/x/net/html"
)

// htmlSkip lists elements whose content is never text a reader sees.
var htmlSkip = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "head": true, "iframe": true, "object": true,
}

// htmlBlocks lists elements that start and end a paragraph. Inline elements
// such as <b> or <a> join their text to the words around them.
var htmlBlocks = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true,
	"header": true, "footer": true, "nav": true, "aside": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"table": true, "tr": true, "td": true, "th": true, "caption": true,
	"blockquote": true, "pre": true, "figure": true, "figcaption": true,
	"hr": true, "br": true, "body": true, "form": true, "fieldset": true,
	"address": true, "details": true, "summary": true,
}

// headingLevels maps h1 to h6 to their levels.
var headingLevels = map[string]int{"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}

// htmlText is the plain text of an HTML page, built up while reading it:
// paragraphs separated by blank lines, with a section starting at each
// heading.
type htmlText struct {
	b        strings.Builder
	titles   [6]string
	sections []mdSection // spans index into b's text
	space    bool        // a space is owed before the next word
}

// chunkHTML reads src with the x/net/html tokenizer, which copes with the
// broken markup real pages hold. It drops tags, comments, scripts and
// styles, decodes entities such as &amp;, and collapses runs of white space
// except inside <pre>. Each h1 to h6 starts a section whose heading path
// works as in Markdown. The chunks carry no line numbers: their text is the
// extracted text, which doesn't line up with lines of the file.
func chunkHTML(src string, lim limits) []store.Chunk {
	var t htmlText
	t.sections = []mdSection{{}}
	skipDepth := 0    // > 0 while inside a skipped element
	preDepth := 0     // > 0 while inside <pre>
	headingLevel := 0 // the h1..h6 level being read, or 0
	var heading strings.Builder

	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break // the end of the input, or input the tokenizer gave up on
		}
		// Token copies the current token out of the tokenizer.
		tok := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch {
			case htmlSkip[tok.Data]:
				if tt == html.StartTagToken {
					skipDepth++
				}
			case headingLevels[tok.Data] > 0:
				t.paragraphBreak()
				headingLevel = headingLevels[tok.Data]
				heading.Reset()
			case tok.Data == "pre":
				t.paragraphBreak()
				preDepth++
			case htmlBlocks[tok.Data]:
				t.paragraphBreak()
			}
		case html.EndTagToken:
			switch {
			case htmlSkip[tok.Data]:
				if skipDepth > 0 {
					skipDepth--
				}
			case headingLevels[tok.Data] > 0 && headingLevel > 0:
				t.startSection(headingLevel, strings.Join(strings.Fields(heading.String()), " "))
				headingLevel = 0
			case tok.Data == "pre":
				if preDepth > 0 {
					preDepth--
				}
				t.paragraphBreak()
			case htmlBlocks[tok.Data]:
				t.paragraphBreak()
			}
		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			if headingLevel > 0 {
				heading.WriteString(tok.Data)
				heading.WriteByte(' ')
				continue
			}
			t.write(tok.Data, preDepth > 0)
		}
	}

	text := t.b.String()
	t.sections[len(t.sections)-1].body.end = len(text)
	var out []store.Chunk
	for _, sec := range t.sections {
		out = append(out, packSection(text, sec, paragraphs(text, sec.body), lim, nil)...)
	}
	return out
}

// write adds a run of page text. Outside <pre> it collapses white space to
// single spaces; inside, it keeps the text as written.
func (t *htmlText) write(s string, pre bool) {
	if pre {
		t.b.WriteString(s)
		return
	}
	const ws = " \t\r\n\f"
	if strings.TrimLeft(s, ws) != s {
		t.space = true // s starts with white space
	}
	for _, w := range strings.Fields(s) {
		if t.space && t.b.Len() > 0 && !t.atParagraphStart() {
			t.b.WriteByte(' ')
		}
		t.b.WriteString(w)
		t.space = true // the next word in s needs a space before it
	}
	// After the last word, a space is owed only if s ended in white space;
	// "<b>bold</b>er" should read "bolder".
	t.space = strings.TrimRight(s, ws) != s
}

// atParagraphStart reports whether the text ends with a paragraph break.
func (t *htmlText) atParagraphStart() bool {
	return strings.HasSuffix(t.b.String(), "\n\n")
}

// paragraphBreak ends the current paragraph with a blank line, once.
func (t *htmlText) paragraphBreak() {
	t.space = false
	if t.b.Len() == 0 || t.atParagraphStart() {
		return
	}
	if strings.HasSuffix(t.b.String(), "\n") {
		t.b.WriteByte('\n')
		return
	}
	t.b.WriteString("\n\n")
}

// startSection closes the section in progress and opens one for a heading
// at level with the given title. The title goes into the text too, as the
// first paragraph of its section.
func (t *htmlText) startSection(level int, title string) {
	t.paragraphBreak()
	t.sections[len(t.sections)-1].body.end = t.b.Len()
	t.titles[level-1] = title
	for i := level; i < len(t.titles); i++ {
		t.titles[i] = ""
	}
	start := t.b.Len()
	t.b.WriteString(title)
	titleEnd := t.b.Len()
	t.paragraphBreak()
	t.sections = append(t.sections, mdSection{
		heading: headingPath(t.titles[:level]),
		title:   span{start, titleEnd},
		body:    span{t.b.Len(), t.b.Len()},
	})
}
