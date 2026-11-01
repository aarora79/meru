// This file reads Word documents (.docx) into Markdown-like text: headings
// by their style, list items, paragraphs, and each table as one line per
// row with its cells joined by " | ". The indexer then chunks that text as
// Markdown, so a chunk keeps its heading path. Headers, footers,
// footnotes, comments and deleted text stay out.

package index

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aarora79/meru/internal/store"
)

// maxHeadingLevel is the deepest Markdown heading, "######".
const maxHeadingLevel = 6

// DocxText returns the text of the Word document in data, as Markdown-like
// text. It fails when the file isn't a .docx it can read, when it is
// password-protected, and when a part passes the ZIP limits.
//
// Like pdfPages, it turns a panic in the parser into an error, so a
// broken or hostile file can't stop merud.
func DocxText(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("read docx: parser failed: %v", r)
		}
	}()
	o, err := openOOXML(data)
	if err != nil {
		return "", err
	}
	doc, err := o.part("word/document.xml")
	if err != nil {
		return "", fmt.Errorf("read docx: %w", err)
	}
	levels := map[string]int{}
	if o.has("word/styles.xml") {
		raw, err := o.part("word/styles.xml")
		if err != nil {
			return "", err
		}
		if levels, err = headingStyles(raw); err != nil {
			return "", fmt.Errorf("read docx styles: %w", err)
		}
	}
	return docxBody(doc, levels)
}

// chunkDocx chunks a Word document's text as Markdown, so each chunk
// carries its heading path, such as "Term > Renewal". The line numbers
// count lines of the extracted text, not of the file, so they are dropped
// and a citation names the heading instead.
func chunkDocx(data []byte, lim limits) ([]store.Chunk, error) {
	text, err := DocxText(data)
	if err != nil {
		return nil, err
	}
	chunks := chunkMarkdown(text, lim)
	if len(chunks) == 0 {
		return nil, errors.New("read docx: the document holds no text")
	}
	for i := range chunks {
		chunks[i].StartLine, chunks[i].EndLine = 0, 0
	}
	return chunks, nil
}

// headingStyles maps each paragraph style's ID to its heading level, for
// the styles that are headings: one with an outline level (0 is level 1),
// or one named "heading N" or "Title".
func headingStyles(raw []byte) (map[string]int, error) {
	levels := map[string]int{}
	id, name, outline := "", "", -1
	err := eachStartEnd(raw, func(d *xml.Decoder, e xml.StartElement) error {
		switch e.Name.Local {
		case "style":
			id, name, outline = "", "", -1
			if attr(e, "type") == "paragraph" {
				id = attr(e, "styleId")
			}
		case "name":
			name = strings.ToLower(attr(e, "val"))
		case "outlineLvl":
			if n, err := strconv.Atoi(attr(e, "val")); err == nil {
				outline = n
			}
		}
		return nil
	}, func(e xml.EndElement) {
		if e.Name.Local != "style" || id == "" {
			return
		}
		if level := styleLevel(name, outline); level > 0 {
			levels[id] = level
		}
	})
	return levels, err
}

// styleLevel returns the heading level a style's name or outline level
// gives, or 0 for a style that isn't a heading. Outline level 9 means
// body text.
func styleLevel(name string, outline int) int {
	if outline >= 0 && outline < 9 {
		return min(outline+1, maxHeadingLevel)
	}
	if name == "title" {
		return 1
	}
	if rest, ok := strings.CutPrefix(name, "heading "); ok {
		if n, err := strconv.Atoi(rest); err == nil && n > 0 {
			return min(n, maxHeadingLevel)
		}
	}
	return 0
}

// docxPara collects one paragraph as the reader walks it.
type docxPara struct {
	text   strings.Builder
	level  int  // heading level, 0 for body text
	isList bool // the paragraph has numbering, so it is a list item
}

// docxBody walks word/document.xml one token at a time and writes its
// text. levels maps heading styles to their levels.
//
// Paragraphs come out separated by blank lines, which the Markdown
// chunker reads as paragraph breaks. A table comes out as one line per row
// at the top level; a table inside a cell joins that cell's text. Deleted
// text (w:del), field codes (w:instrText) and the fallback copy of a
// drawing's text (mc:Fallback) stay out.
func docxBody(raw []byte, levels map[string]int) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var out strings.Builder
	var p *docxPara  // the paragraph being read, nil between paragraphs
	var row []string // the cells of the top-level table row being read
	var cell strings.Builder
	tableDepth := 0
	skip := 0 // depth inside an element whose text stays out

	// writeBlock adds s to the output as its own paragraph.
	writeBlock := func(s string) {
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(s)
	}
	// endPara finishes paragraph p: in a table it joins the current cell,
	// elsewhere it becomes a block of its own.
	endPara := func() {
		if p == nil {
			return
		}
		text := strings.TrimSpace(p.text.String())
		level, isList := p.level, p.isList
		p = nil
		if text == "" {
			return
		}
		if tableDepth > 0 {
			if cell.Len() > 0 {
				cell.WriteString(" ")
			}
			cell.WriteString(oneLine(text))
			return
		}
		switch {
		case level > 0:
			writeBlock(strings.Repeat("#", level) + " " + oneLine(text))
		case isList:
			writeBlock("- " + text)
		default:
			writeBlock(text)
		}
	}
	// write adds s to the current paragraph, starting one if a run sits
	// outside any paragraph.
	write := func(s string) {
		if p == nil {
			p = &docxPara{}
		}
		p.text.WriteString(s)
	}

	var rows []string // the lines of the top-level table being read
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read docx: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch t.Name.Local {
			case "del", "instrText", "delText", "Fallback":
				skip = 1
			case "p":
				endPara() // a paragraph inside another, as in a text box
				p = &docxPara{}
			case "pStyle":
				if p != nil {
					p.level = levels[attr(t, "val")]
				}
			case "outlineLvl":
				if p != nil {
					if n, err := strconv.Atoi(attr(t, "val")); err == nil && n < 9 {
						p.level = min(n+1, maxHeadingLevel)
					}
				}
			case "numPr":
				if p != nil {
					p.isList = true
				}
			case "t":
				text, err := elementText(d)
				if err != nil {
					return "", fmt.Errorf("read docx: %w", err)
				}
				write(text)
			case "tab":
				write("\t")
			case "br", "cr":
				write("\n")
			case "noBreakHyphen":
				write("-")
			case "tbl":
				endPara()
				tableDepth++
			case "tc":
				if tableDepth == 1 {
					cell.Reset()
				}
			case "tr":
				if tableDepth == 1 {
					row = nil
				}
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "p":
				endPara()
			case "tc":
				endPara()
				if tableDepth == 1 {
					row = append(row, cell.String())
					cell.Reset()
				}
			case "tr":
				if tableDepth == 1 && filled(row) > 0 {
					rows = append(rows, strings.Join(row, " | "))
				}
			case "tbl":
				endPara()
				tableDepth--
				if tableDepth == 0 && len(rows) > 0 {
					writeBlock(strings.Join(rows, "\n"))
					rows = nil
				}
			}
		}
	}
	endPara()
	return out.String() + "\n", nil
}
