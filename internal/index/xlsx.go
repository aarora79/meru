// This file reads Excel workbooks (.xlsx) into tables: the sheet names
// and order from the workbook, the shared strings, the number formats
// that mark a date, and each sheet's cells, read one XML token at a time
// so a large sheet never becomes a tree in memory. It reads the value
// Excel saved with each formula and never evaluates a formula.

package index

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
)

// xlsxSheet is one sheet the workbook lists: its name, the part that holds
// it and whether Excel hides it.
type xlsxSheet struct {
	name   string
	part   string // such as "xl/worksheets/sheet1.xml"
	hidden bool
}

// xlsxBook is what a workbook's shared parts say, which every sheet needs.
type xlsxBook struct {
	sheets   []xlsxSheet
	strings  []string // the shared strings, by index
	dates    []bool   // dates[s] is true when cell style s shows a date
	percent  []bool   // percent[s] is true when cell style s shows a percentage
	date1904 bool     // dates count from 1904, as old Mac workbooks do
}

// xlsxTables returns one table per sheet of the workbook in data, in the
// workbook's order. It fails when the file isn't an .xlsx it can read,
// when it is password-protected, and when it passes a limit.
//
// Parsing a file from someone else is the riskiest work the indexer does,
// so a deferred function turns any panic in it into an error, as pdfPages
// does, and merud keeps running.
func xlsxTables(data []byte) (tables []Table, err error) {
	defer func() {
		if r := recover(); r != nil {
			tables, err = nil, fmt.Errorf("read xlsx: parser failed: %v", r)
		}
	}()
	o, err := openOOXML(data)
	if err != nil {
		return nil, err
	}
	book, err := readBook(o)
	if err != nil {
		return nil, err
	}
	var cells cellCounter
	for _, s := range book.sheets {
		raw, err := o.part(s.part)
		if errors.Is(err, errMissingPart) {
			continue // a chart sheet or a broken link: nothing to read
		}
		if err != nil {
			return nil, err
		}
		rows, err := readSheet(raw, s.name, book, &cells)
		if err != nil {
			return nil, err
		}
		tables = append(tables, Table{Name: s.name, Rows: rows, Hidden: s.hidden})
	}
	if len(tables) == 0 {
		return nil, errors.New("read xlsx: the workbook has no sheets")
	}
	return tables, nil
}

// readBook reads the workbook's sheet list, its relationships, the shared
// strings and the styles.
func readBook(o *ooxml) (xlsxBook, error) {
	var book xlsxBook
	wb, err := o.part("xl/workbook.xml")
	if err != nil {
		return book, fmt.Errorf("read xlsx: %w", err)
	}
	rels, err := o.part("xl/_rels/workbook.xml.rels")
	if err != nil {
		return book, fmt.Errorf("read xlsx: %w", err)
	}
	targets, err := relTargets(rels, "xl")
	if err != nil {
		return book, err
	}
	if err := eachStart(wb, func(d *xml.Decoder, e xml.StartElement) error {
		switch e.Name.Local {
		case "workbookPr":
			v := attr(e, "date1904")
			book.date1904 = v == "1" || v == "true"
		case "sheet":
			// The r:id attribute names the sheet's relationship; its
			// namespace prefix varies, so match on the local name.
			part := targets[attr(e, "id")]
			state := attr(e, "state")
			book.sheets = append(book.sheets, xlsxSheet{name: attr(e, "name"), part: part, hidden: state == "hidden" || state == "veryHidden"})
		}
		return nil
	}); err != nil {
		return book, fmt.Errorf("read xlsx workbook: %w", err)
	}
	if o.has("xl/sharedStrings.xml") {
		raw, err := o.part("xl/sharedStrings.xml")
		if err != nil {
			return book, err
		}
		if book.strings, err = sharedStrings(raw); err != nil {
			return book, fmt.Errorf("read xlsx shared strings: %w", err)
		}
	}
	if o.has("xl/styles.xml") {
		raw, err := o.part("xl/styles.xml")
		if err != nil {
			return book, err
		}
		if book.dates, book.percent, err = cellStyles(raw); err != nil {
			return book, fmt.Errorf("read xlsx styles: %w", err)
		}
	}
	return book, nil
}

// relTargets maps each relationship ID in a .rels part to the part it
// points at, as a name inside the archive. A target is relative to base,
// the folder of the part that owns the relationships, unless it starts
// with "/". path.Join cleans the result, so "../x" can't climb above the
// archive's root; the name is only ever looked up in the archive's list.
func relTargets(rels []byte, base string) (map[string]string, error) {
	out := map[string]string{}
	err := eachStart(rels, func(d *xml.Decoder, e xml.StartElement) error {
		if e.Name.Local != "Relationship" {
			return nil
		}
		target := attr(e, "Target")
		if strings.HasPrefix(target, "/") {
			target = path.Clean(strings.TrimPrefix(target, "/"))
		} else {
			target = path.Join(base, target)
		}
		out[attr(e, "Id")] = target
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read xlsx relationships: %w", err)
	}
	return out, nil
}

// sharedStrings reads the shared strings part: one <si> per string, whose
// text is a <t> or, for rich text, several runs (<r><t>) joined. Phonetic
// guides (<rPh>) are left out.
func sharedStrings(raw []byte) ([]string, error) {
	var out []string
	d := xml.NewDecoder(bytes.NewReader(raw))
	var b strings.Builder
	inSI, skip := false, 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		// A type switch picks the case by the token's concrete type.
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				b.Reset()
			case "rPh":
				skip++
			case "t":
				if inSI && skip == 0 {
					text, err := elementText(d)
					if err != nil {
						return nil, err
					}
					b.WriteString(text)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, b.String())
				inSI = false
			case "rPh":
				skip--
			}
		}
	}
}

// builtinDates are the built-in number format IDs that show a date or a
// time; Excel doesn't write their codes into the file.
var builtinDates = map[int]bool{14: true, 15: true, 16: true, 17: true, 18: true, 19: true,
	20: true, 21: true, 22: true, 45: true, 46: true, 47: true}

// cellStyles reads the styles part and reports, for each cell style, the
// index a cell's s attribute holds, whether it shows a date and whether it
// shows a percentage.
func cellStyles(raw []byte) (dates, percent []bool, err error) {
	codes := map[int]string{}
	inXfs := false
	err = eachStartEnd(raw, func(d *xml.Decoder, e xml.StartElement) error {
		switch e.Name.Local {
		case "numFmt":
			id, _ := strconv.Atoi(attr(e, "numFmtId"))
			codes[id] = attr(e, "formatCode")
		case "cellXfs":
			inXfs = true
		case "xf":
			if inXfs {
				id, _ := strconv.Atoi(attr(e, "numFmtId"))
				code, custom := codes[id]
				dates = append(dates, builtinDates[id] || (custom && dateCode(code)))
				percent = append(percent, id == 9 || id == 10 || (custom && percentCode(code)))
			}
		}
		return nil
	}, func(e xml.EndElement) {
		if e.Name.Local == "cellXfs" {
			inXfs = false
		}
	})
	return dates, percent, err
}

// formatLetters returns code, a number format such as `yyyy-mm-dd` or
// `"Total: "0.00`, without its quoted text, its bracketed parts (colors
// and locales such as [$-409]) and its backslash-escaped characters, in
// lower case: the letters that decide what the format shows.
func formatLetters(code string) string {
	var b strings.Builder
	inQuote, inBracket := false, false
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '[':
			inBracket = true
		case c == ']':
			inBracket = false
		case inBracket:
		case c == '\\' || c == '_' || c == '*':
			i++ // the next character is shown as is, or is padding
		default:
			b.WriteByte(c)
		}
	}
	return strings.ToLower(b.String())
}

// dateCode reports whether a custom number format shows a date: it holds
// d, m or y outside quotes and brackets. "General" holds none of them.
func dateCode(code string) bool {
	letters := formatLetters(code)
	if letters == "general" {
		return false
	}
	return strings.ContainsAny(letters, "dmy")
}

// percentCode reports whether a custom number format shows a percentage.
func percentCode(code string) bool {
	return strings.Contains(formatLetters(code), "%")
}

// readSheet reads one sheet's cells into rows, Rows[i] being row i+1. It
// places each cell by its reference, such as C7, and drops trailing empty
// cells and rows. It fails when a cell that holds a value sits past maxRows
// or maxCols, or cells pushes the file past maxCells.
func readSheet(raw []byte, name string, book xlsxBook, cells *cellCounter) ([][]string, error) {
	var rows [][]string
	d := xml.NewDecoder(bytes.NewReader(raw))
	rowNum := 0
	var row []string
	inRow := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read sheet %s: %w", name, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				n, err := strconv.Atoi(attr(t, "r"))
				if err != nil || n <= rowNum {
					n = rowNum + 1 // no number, or one out of order: take the next
				}
				rowNum, row, inRow = n, nil, true
			case "c":
				if !inRow {
					continue
				}
				col := len(row)
				if c, ok := refColumn(attr(t, "r")); ok && c >= len(row) {
					col = c
				}
				value, err := cellValue(d, t, book)
				if err != nil {
					return nil, fmt.Errorf("read sheet %s, row %d: %w", name, rowNum, err)
				}
				// Excel often writes empty, formatted cells and rows far
				// past the data, so the limits count only cells that hold
				// a value.
				if value == "" {
					continue
				}
				if col >= maxCols {
					return nil, tooManyCols(name)
				}
				for len(row) < col {
					row = append(row, "")
				}
				row = append(row, value)
			}
		case xml.EndElement:
			if t.Name.Local == "row" && inRow {
				inRow = false
				if len(row) == 0 {
					continue
				}
				if rowNum > maxRows {
					return nil, tooManyRows(name)
				}
				if err := cells.add(len(row)); err != nil {
					return nil, err
				}
				for len(rows) < rowNum-1 {
					rows = append(rows, nil)
				}
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}

// refColumn returns the column of a cell reference such as "C7", counting
// from 0, and false when ref has no letters.
func refColumn(ref string) (int, bool) {
	col := 0
	i := 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		col = col*26 + int(ref[i]-'A'+1)
		if col > maxCols {
			return maxCols, true
		}
	}
	if i == 0 {
		return 0, false
	}
	return col - 1, true
}

// cellValue reads the rest of the cell element c, whose start the decoder
// has just read, and returns its value as text:
//
//   - t="s": the shared string the value indexes;
//   - t="inlineStr": the text in <is>;
//   - t="b": TRUE or FALSE;
//   - t="e", t="str" or t="d": the text as saved (an error such as #N/A, a
//     formula's text result, or an ISO date);
//   - otherwise a number: a date when the cell's style shows one, a
//     percentage when it shows one, and the number itself otherwise.
//
// A formula (<f>) is skipped; the value Excel saved with it is the value.
func cellValue(d *xml.Decoder, c xml.StartElement, book xlsxBook) (string, error) {
	typ := attr(c, "t")
	style, _ := strconv.Atoi(attr(c, "s"))
	var v strings.Builder
	var inline strings.Builder
	depth := 1
	where := ""
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "v":
				where = "v"
			case "t":
				where = "t"
			default:
				where = ""
			}
		case xml.EndElement:
			depth--
			where = ""
		case xml.CharData:
			switch where {
			case "v":
				v.Write(t)
			case "t":
				inline.Write(t)
			}
		}
	}
	raw := v.String()
	switch typ {
	case "s":
		i, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || i < 0 || i >= len(book.strings) {
			return "", nil // a broken index: treat the cell as empty
		}
		return book.strings[i], nil
	case "inlineStr":
		return inline.String(), nil
	case "b":
		if strings.TrimSpace(raw) == "1" {
			return "TRUE", nil
		}
		if raw == "" {
			return "", nil
		}
		return "FALSE", nil
	case "e", "str", "d":
		return raw, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw, nil
	}
	switch {
	case style >= 0 && style < len(book.dates) && book.dates[style]:
		return excelDate(f, book.date1904), nil
	case style >= 0 && style < len(book.percent) && book.percent[style]:
		return number(math.Round(f*100*1e9)/1e9) + "%", nil
	}
	return number(f), nil
}

// number writes f in the fewest digits that read back as f, so a value
// Excel saved as 84.760000000000005 shows as 84.76. Very large or small
// numbers keep Go's exponent form.
func number(f float64) string {
	if math.Abs(f) >= 1e15 || (f != 0 && math.Abs(f) < 1e-6) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// excelDate turns serial, a count of days since Excel's start date with
// the time of day as its fraction, into 2006-01-02, or 2006-01-02 15:04
// when it has a time. Excel's 1900 system counts from 30 December 1899,
// which absorbs its old mistake of treating 1900 as a leap year for every
// date from March 1900 on; the 1904 system counts from 1 January 1904.
func excelDate(serial float64, date1904 bool) string {
	start := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		start = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if serial < 0 || serial > 2_958_465 { // past 31 December 9999
		return number(serial)
	}
	days := math.Floor(serial)
	secs := math.Round((serial - days) * 86400)
	t := start.AddDate(0, 0, int(days)).Add(time.Duration(secs) * time.Second)
	if secs == 0 {
		return t.Format("2006-01-02")
	}
	if days == 0 && !date1904 {
		return t.Format("15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// attr returns the value of the attribute of e whose local name is name,
// whatever its namespace prefix, or "".
func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// elementText reads the text up to the end of the element whose start the
// decoder has just read, and returns it.
func elementText(d *xml.Decoder) (string, error) {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			b.Write(t)
		}
	}
	return b.String(), nil
}

// eachStart calls fn for every start tag in raw, in order. It stops at
// fn's first error.
func eachStart(raw []byte, fn func(*xml.Decoder, xml.StartElement) error) error {
	return eachStartEnd(raw, fn, nil)
}

// eachStartEnd calls start for every start tag in raw and, when end isn't
// nil, end for every end tag, in order. It stops at start's first error.
//
// xml.Decoder.Token hands back one token at a time: a start tag, an end
// tag or the text between. Reading that way keeps memory to the part's
// bytes; xml.Unmarshal would build every element of a large sheet at once.
// The decoder doesn't expand entities a document defines, so a "billion
// laughs" file can't grow in memory.
func eachStartEnd(raw []byte, start func(*xml.Decoder, xml.StartElement) error, end func(xml.EndElement)) error {
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := start(d, t); err != nil {
				return err
			}
		case xml.EndElement:
			if end != nil {
				end(t)
			}
		}
	}
}
