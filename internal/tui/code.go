// This file finds the code blocks in a finished answer and puts a dim
// "⧉ copy N" label under each one on screen, so the user can copy a block
// with /copy N, Ctrl-Y or, with [chat] mouse_copy on, a click on its label.

package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// codeBlock is one fenced or indented code block in an answer's Markdown.
type codeBlock struct {
	// text is what /copy puts on the clipboard: the block's lines as the
	// model wrote them, without the fences, the language tag, the
	// indentation that made it a block, or the newline after the last line.
	text string
	// end is the byte offset in the answer just past the block's last line,
	// where markBlocks adds the marker line.
	end int
	// prefix is what starts each of the block's lines in the source before
	// its text: the "> " of a quote, the spaces of a list item, and the four
	// spaces of an indented block. The marker line needs the same prefix to
	// stay inside the block.
	prefix string
}

// findCodeBlocks returns the code blocks in the Markdown src, in the order
// they appear: fenced blocks (``` or ~~~) and indented blocks, at any depth
// inside lists and quotes. Inline code isn't a block. A block with nothing
// but blank lines is left out, because there is nothing in it to copy.
//
// It parses src with goldmark, the CommonMark parser Glamour uses to draw
// the answer, so both agree on where each block starts and ends.
func findCodeBlocks(src string) []codeBlock {
	source := []byte(src)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))

	var blocks []codeBlock
	// ast.Walk visits every node of the parsed document, depth first. The
	// function it calls runs once on the way into a node (entering is true)
	// and once on the way out.
	walk := func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || (n.Kind() != ast.KindFencedCodeBlock && n.Kind() != ast.KindCodeBlock) {
			return ast.WalkContinue, nil
		}
		// Lines holds one segment, a start and stop offset into source,
		// per line of the block's text.
		lines := n.Lines()
		var b strings.Builder
		prefix := ""
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			line := seg.Value(source)
			b.Write(line)
			// A line with text carries the whole prefix; a blank line in a
			// list may not, so the prefix comes from the last line with text.
			if strings.TrimSpace(string(line)) != "" {
				start := strings.LastIndexByte(src[:seg.Start], '\n') + 1
				prefix = src[start:seg.Start]
			}
		}
		if strings.TrimSpace(b.String()) != "" {
			blocks = append(blocks, codeBlock{
				text:   strings.TrimSuffix(b.String(), "\n"),
				end:    lines.At(lines.Len() - 1).Stop,
				prefix: prefix,
			})
		}
		// A code block holds no other blocks, so skip what is inside it.
		return ast.WalkSkipChildren, nil
	}
	// The walk function never returns an error, so Walk can't either.
	_ = ast.Walk(doc, walk)
	return blocks
}

// markerPattern matches the marker line markBlocks adds to block n. The
// marker is one word of letters and digits, so no syntax highlighter splits
// it and Glamour never wraps it, and the model is most unlikely to write
// it. The (\d+) group captures n.
var markerPattern = regexp.MustCompile(`MERUCOPY(\d+)X`)

// marker returns the marker for block n.
func marker(n int) string { return fmt.Sprintf("MERUCOPY%dX", n) }

// markBlocks returns src with one marker line added as the last line of
// each block, first numbered first. Glamour draws the marker as one more
// line of the block, in the right place however it wraps or indents the
// block, and labelBlocks then swaps that line for the label.
//
// This is how the labels find their place. Drawing each code block and the
// prose round it on their own would break a block that sits in a list
// item, and matching Glamour's output to the source line by line breaks as
// soon as a long line wraps. A marker drawn by Glamour itself moves with
// the block at any width.
func markBlocks(src string, blocks []codeBlock, first int) string {
	var b strings.Builder
	last := 0
	for i, blk := range blocks {
		b.WriteString(src[last:blk.end])
		line := blk.prefix + marker(first+i)
		// A block at the very end of the answer may have no newline after
		// its last line.
		if !strings.HasSuffix(src[:blk.end], "\n") {
			line = "\n" + line
		}
		b.WriteString(line + "\n")
		last = blk.end
	}
	b.WriteString(src[last:])
	return b.String()
}

// labelBlocks swaps each marker line in Glamour's output for a dim label,
// "⧉ copy N", indented as the marker was. It returns false when it didn't
// find want markers, which happens when Glamour drew a block differently
// than expected or the screen is too narrow for a marker; the caller then
// draws the answer without labels rather than show a stray marker.
func (m *Model) labelBlocks(rendered string, want int) (string, bool) {
	lines := strings.Split(rendered, "\n")
	found := 0
	for i, l := range lines {
		plain := ansi.Strip(l)
		loc := markerPattern.FindStringSubmatchIndex(plain)
		if loc == nil {
			continue
		}
		found++
		// loc holds byte offsets: the whole match, then the group.
		n, _ := strconv.Atoi(plain[loc[2]:loc[3]])
		// Keep what Glamour put before the marker, such as the indent or a
		// quote's bar, so the label lines up with the block.
		lines[i] = m.style.dim.Render(plain[:loc[0]] + copyLabel(n))
	}
	return strings.Join(lines, "\n"), found == want
}

// copyLabel returns the label shown under block n.
func copyLabel(n int) string { return fmt.Sprintf("⧉ copy %d", n) }

// labelPattern matches a label on screen, for the mouse. The group
// captures the block's number.
var labelPattern = regexp.MustCompile(`⧉ copy (\d+)`)

// labelAt returns the number of the block whose label covers column col
// of the screen line line, or 0 when no label is there.
func labelAt(line string, col int) int {
	plain := ansi.Strip(line)
	for _, loc := range labelPattern.FindAllStringSubmatchIndex(plain, -1) {
		// Columns count cells on screen, not bytes: "⧉" is three bytes
		// wide in the string and one cell wide on screen.
		from := ansi.StringWidth(plain[:loc[0]])
		to := from + ansi.StringWidth(plain[loc[0]:loc[1]])
		if col >= from && col < to {
			n, _ := strconv.Atoi(plain[loc[2]:loc[3]])
			return n
		}
	}
	return 0
}
