# XML streaming

**In one line:** `xml.Decoder.Token` reads an XML document one piece at a time (a
start tag, an end tag, or the text between), so a program can handle a large
file without building all of it in memory.

## Why Go has it

The `encoding/xml` package offers two ways in. `xml.Unmarshal` fills a struct
from the whole document at once: short to write, but every element becomes a
value in memory. `xml.Decoder` hands back one *token* per call instead, and the
program keeps only what it needs. A spreadsheet sheet can hold 100,000 rows, so
Meru's readers stream.

## Smallest example

```go
// countRows counts the <row> elements in raw, one token at a time.
func countRows(raw []byte) (int, error) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	n := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return n, nil // the end of the document
		}
		if err != nil {
			return 0, err // broken XML
		}
		// A type switch picks the branch by the token's concrete type.
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "row" {
				n++
			}
		}
	}
}
```

`t.Name.Local` is the tag's name without its namespace prefix, so `<x:row>` and
`<row>` both match. `xml.CharData` holds the text between tags, and
`xml.EndElement` a closing tag.

## How much memory it saves

A test program read a 50 MB sheet of 180,691 rows both ways on a Mac. With
`Token`, the heap peaked at 98 MB: the file's own 50 MB, plus tokens the garbage
collector hadn't freed yet. With `Unmarshal` into a struct of rows and cells, it
peaked at 254 MB and took 60% longer.

## Where Meru uses it

- `internal/index/xlsx.go`: reads the workbook, the shared strings, the styles
  and each sheet. `eachStartEnd` walks a part and calls a function per tag;
  `readSheet` and `cellValue` read cells as they go.
- `internal/index/docx.go`: `docxBody` walks a Word document and writes its text,
  paragraph by paragraph.

## Mistakes to avoid

- `Token` returns a token whose bytes the decoder may reuse on the next call. Copy
  `xml.CharData` (with `string(t)` or a `strings.Builder`) before you call `Token`
  again.
- Don't trust a document to be small because the file is. A ZIP file of a few KB
  can unpack into gigabytes of XML, so Meru caps the bytes it unpacks before the
  decoder sees them. The decoder itself refuses entities a document defines, so a
  "billion laughs" file, whose entities expand into each other, can't grow in
  memory.
