// This file tests the chunkers: the token estimate, pack's size and overlap
// rules, and the Markdown, Go, plain-text, HTML and PDF chunkers.

package index

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/store"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"héllo", 2},    // five characters, six bytes
		{"日本語日本語日本", 2}, // eight characters, 24 bytes
		{strings.Repeat("a", 2000), 500},
	}
	for _, tt := range tests {
		if got := estimateTokens(tt.in); got != tt.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// checkSizes fails when any chunk passes the character limit.
func checkSizes(t *testing.T, chunks []store.Chunk, lim limits) {
	t.Helper()
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c.Text); n > lim.max {
			t.Errorf("chunk %d has %d characters, over the limit of %d", i, n, lim.max)
		}
	}
}

func TestPackSizesAndOverlap(t *testing.T) {
	// Twenty paragraphs of about 90 characters each.
	var paras []string
	for i := range 20 {
		paras = append(paras, fmt.Sprintf("Paragraph %02d says something short about topic %02d and then it stops here.", i, i))
	}
	src := strings.Join(paras, "\n\n")
	lim := limits{max: 300, overlap: 40}
	chunks := chunkText(src, lim)
	if len(chunks) < 5 {
		t.Fatalf("got %d chunks, want the text split into several", len(chunks))
	}
	checkSizes(t, chunks, lim)
	for i := 1; i < len(chunks); i++ {
		prev, cur := chunks[i-1].Text, chunks[i].Text
		// The overlap is the tail of the previous chunk, starting at a word.
		head := strings.SplitN(cur, "\n", 2)[0]
		if !strings.HasSuffix(prev, head) {
			t.Errorf("chunk %d starts with %q, which isn't the end of chunk %d (%q)", i, head, i-1, prev)
		}
		if n := utf8.RuneCountInString(head); n > lim.overlap {
			t.Errorf("chunk %d repeats %d characters, more than the overlap of %d", i, n, lim.overlap)
		}
	}
	// Every paragraph appears whole in some chunk.
	for _, p := range paras {
		found := false
		for _, c := range chunks {
			found = found || strings.Contains(c.Text, p)
		}
		if !found {
			t.Errorf("no chunk holds %q whole", p)
		}
	}
}

func TestPackNoOverlap(t *testing.T) {
	src := "one one one\n\ntwo two two\n\nthree three"
	got := chunkText(src, limits{max: 12})
	var texts []string
	for _, c := range got {
		texts = append(texts, c.Text)
	}
	want := []string{"one one one", "two two two", "three three"}
	if !reflect.DeepEqual(texts, want) {
		t.Errorf("chunks = %q, want %q", texts, want)
	}
}

func TestSplitLongUnits(t *testing.T) {
	lim := limits{max: 50}
	tests := []struct {
		name string
		src  string
	}{
		{"many lines", strings.Repeat("a short line of code\n", 20)},
		{"one long line", strings.Repeat("word ", 100)},
		{"no spaces", strings.Repeat("x", 175)},
		{"multi-byte", strings.Repeat("日本語", 60)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunkText(tt.src, lim)
			checkSizes(t, chunks, lim)
			var total int
			for _, c := range chunks {
				total += len(strings.Fields(c.Text))
				if !utf8.ValidString(c.Text) {
					t.Errorf("chunk %q cuts a character in half", c.Text)
				}
			}
			if want := len(strings.Fields(tt.src)); tt.name != "no spaces" && tt.name != "multi-byte" && total != want {
				t.Errorf("chunks hold %d words, want %d", total, want)
			}
		})
	}
}

func TestChunkTextLines(t *testing.T) {
	src := "first para\nline two\n\n\nsecond para\n\nthird para\nand more\n"
	got := chunkText(src, limits{max: 20})
	want := []store.Chunk{
		{Text: "first para\nline two", StartLine: 1, EndLine: 2},
		{Text: "second para", StartLine: 5, EndLine: 5},
		{Text: "third para\nand more", StartLine: 7, EndLine: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chunks:\n got %+v\nwant %+v", got, want)
	}
}

func TestChunkMarkdown(t *testing.T) {
	src := `Intro before any heading.

# Garden

The garden plan.

## Spring

Sow in trays.

` + "```sh" + `
# not a heading, inside a fence

make build
` + "```" + `

### Tomatoes

Six plants.

## Autumn ##

Nothing yet.

# Empty

## Under empty

Text.
`
	got := chunkMarkdown(src, limits{max: 2000, overlap: 50})
	type row struct {
		heading    string
		start, end int
		first      string
	}
	var rows []row
	for _, c := range got {
		rows = append(rows, row{c.Heading, c.StartLine, c.EndLine, strings.SplitN(c.Text, "\n", 2)[0]})
	}
	want := []row{
		{"", 1, 1, "Intro before any heading."},
		{"Garden", 3, 5, "# Garden"},
		{"Garden > Spring", 7, 15, "## Spring"},
		{"Garden > Spring > Tomatoes", 17, 19, "### Tomatoes"},
		{"Garden > Autumn", 21, 23, "## Autumn ##"},
		// "# Empty" has no text of its own, so it makes no chunk.
		{"Empty > Under empty", 27, 29, "## Under empty"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("chunks:\n got %+v\nwant %+v", rows, want)
	}
	if !strings.Contains(got[2].Text, "make build") {
		t.Errorf("the fenced block left its section: %q", got[2].Text)
	}
}

func TestChunkMarkdownLongSection(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Notes\n\n")
	for i := range 30 {
		fmt.Fprintf(&b, "Paragraph %d of the notes, with enough words to take some room.\n\n", i)
	}
	// A fence with blank lines inside that fits in one chunk.
	b.WriteString("```\nline one\n\nline two\n\nline three\n```\n\nAfter the fence.\n")
	lim := limits{max: 400, overlap: 40}
	chunks := chunkMarkdown(b.String(), lim)
	checkSizes(t, chunks, lim)
	if len(chunks) < 4 {
		t.Fatalf("got %d chunks, want the section split", len(chunks))
	}
	fenced := false
	for _, c := range chunks {
		if c.Heading != "Notes" {
			t.Errorf("chunk heading = %q, want Notes", c.Heading)
		}
		if strings.Contains(c.Text, "line one") {
			fenced = strings.Contains(c.Text, "line one\n\nline two\n\nline three")
		}
	}
	if !fenced {
		t.Error("the fenced block was split although it fits in one chunk")
	}
}

func TestChunkMarkdownHugeFence(t *testing.T) {
	src := "# Code\n\n```\n" + strings.Repeat("fmt.Println(\"a line of code\")\n", 40) + "```\n"
	lim := limits{max: 300}
	chunks := chunkMarkdown(src, lim)
	checkSizes(t, chunks, lim)
	if len(chunks) < 3 {
		t.Errorf("got %d chunks; a fence over the limit should split", len(chunks))
	}
}

func TestATXHeading(t *testing.T) {
	tests := []struct {
		line  string
		level int
		title string
		ok    bool
	}{
		{"# Title", 1, "Title", true},
		{"### Deep one ###", 3, "Deep one", true},
		{"   ## Indented", 2, "Indented", true},
		{"    # code block", 0, "", false},
		{"#hashtag", 0, "", false},
		{"####### seven", 0, "", false},
		{"# C#", 1, "C#", true},
		{"#", 1, "", true},
	}
	for _, tt := range tests {
		level, title, ok := atxHeading(tt.line)
		if level != tt.level || title != tt.title || ok != tt.ok {
			t.Errorf("atxHeading(%q) = %d, %q, %v; want %d, %q, %v", tt.line, level, title, ok, tt.level, tt.title, tt.ok)
		}
	}
}

func TestChunkGo(t *testing.T) {
	src := `// Package demo shows the chunker.
package demo

import "fmt"

// Greeter says hello.
type Greeter struct{ name string }

// Hello greets.
func (g *Greeter) Hello() {
	fmt.Println("hello", g.name)
}

func main() {
	(&Greeter{}).Hello()
}

const (
	A = 1
	B = 2
)
`
	got := chunkCode("demo.go", src, limits{max: 2000})
	type row struct {
		heading    string
		start, end int
	}
	var rows []row
	for _, c := range got {
		rows = append(rows, row{c.Heading, c.StartLine, c.EndLine})
	}
	want := []row{
		{"package demo", 1, 2},
		{"import", 4, 4},
		{"Greeter", 6, 7},
		{"Greeter.Hello", 9, 12},
		{"main", 14, 16},
		{"A, B", 18, 21},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("chunks:\n got %+v\nwant %+v", rows, want)
	}
	if !strings.HasPrefix(got[3].Text, "// Hello greets.") {
		t.Errorf("the method chunk should start with its doc comment: %q", got[3].Text)
	}
}

func TestChunkGoLongFunction(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\n\nfunc Long() {\n")
	for i := range 40 {
		fmt.Fprintf(&b, "\tx%d := %d\n\t_ = x%d\n\n", i, i, i)
	}
	b.WriteString("}\n")
	lim := limits{max: 200, overlap: 20}
	chunks := chunkCode("big.go", b.String(), lim)
	checkSizes(t, chunks, lim)
	n := 0
	for _, c := range chunks {
		if c.Heading == "Long" {
			n++
		}
	}
	if n < 3 {
		t.Errorf("Long split into %d chunks named Long, want several", n)
	}
}

func TestChunkCodeFallsBack(t *testing.T) {
	tests := []struct{ name, src string }{
		{"broken.go", "package x\n\nfunc {\n\nstill text\n"},
		{"script.py", "def a():\n    return 1\n\n\ndef b():\n    return 2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunkCode(tt.name, tt.src, limits{max: 20})
			if len(chunks) < 2 {
				t.Fatalf("got %d chunks, want blank-line blocks", len(chunks))
			}
			if chunks[0].StartLine != 1 || chunks[0].Heading != "" {
				t.Errorf("first chunk = %+v, want line 1 and no heading", chunks[0])
			}
		})
	}
}

func TestChunkHTML(t *testing.T) {
	src := `<!doctype html>
<html><head><title>Ignored title</title><style>body { color: red }</style></head>
<body>
<script>var secret = "no";</script>
<p>Before any heading &amp; more.</p>
<h1>Trip <em>plan</em></h1>
<p>We <b>fly</b> on Monday.<br>Back Friday.</p>
<h2>Costs</h2>
<ul><li>Hotel</li><li>Train</li></ul>
<pre>keep   this
  spacing</pre>
<!-- a comment -->
<h1>Empty</h1>
</body></html>`
	got := chunkHTML(src, limits{max: 2000})
	type row struct{ heading, text string }
	var rows []row
	for _, c := range got {
		if c.StartLine != 0 || c.EndLine != 0 {
			t.Errorf("html chunk has line numbers %d-%d; want none", c.StartLine, c.EndLine)
		}
		rows = append(rows, row{c.Heading, c.Text})
	}
	want := []row{
		{"", "Before any heading & more."},
		{"Trip plan", "Trip plan\n\nWe fly on Monday.\n\nBack Friday."},
		{"Trip plan > Costs", "Costs\n\nHotel\n\nTrain\n\nkeep   this\n  spacing"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("chunks:\n got %q\nwant %q", rows, want)
	}
}

// minimalPDF builds a small valid PDF with one page per string in pages,
// each drawing its text in Helvetica. It works out the cross-reference
// table's byte offsets itself, so the test needs no binary fixture.
func minimalPDF(pages []string) []byte {
	var objs []string
	n := len(pages)
	// Object 1 is the catalog, 2 the page tree, 3 the font; then a page
	// object and a content stream for each page.
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
	for i, text := range pages {
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func TestChunkPDF(t *testing.T) {
	data := minimalPDF([]string{"Hello from page one", "Second page text"})
	chunks, err := chunkPDF(data, limits{max: 2000})
	if err != nil {
		t.Fatalf("chunkPDF: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want one per page: %+v", len(chunks), chunks)
	}
	for i, want := range []string{"Hello from page one", "Second page text"} {
		if chunks[i].Page != i+1 || !strings.Contains(chunks[i].Text, want) {
			t.Errorf("chunk %d = page %d %q; want page %d holding %q", i, chunks[i].Page, chunks[i].Text, i+1, want)
		}
	}
}

func TestChunkPDFFailures(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"not a pdf", []byte("just text")},
		{"truncated", minimalPDF([]string{"x"})[:60]},
		{"no text", minimalPDF([]string{""})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := chunkPDF(tt.data, limits{max: 2000}); err == nil {
				t.Error("chunkPDF succeeded; want an error")
			}
		})
	}
}
