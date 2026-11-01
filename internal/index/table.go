// This file holds what Excel sheets and delimited text files share once
// read: the Table type, the limits on one table, ReadTable for the file
// tools, the guess at which row names the columns, the text a table shows
// in read_file and grep, and chunkTable, which cuts a table into chunks of
// rows for search.

package index

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/store"
)

// Table is one sheet of a spreadsheet, or a whole delimited text file, as
// text. Every value is the text a person would read: numbers as Excel
// saved them, dates as 2006-01-02. Rows[0] is the sheet's first row, so
// Rows[i] is spreadsheet row i+1, the number a person sees in Excel.
type Table struct {
	Name   string     // the sheet's name; for a CSV file, its name without the extension
	Rows   [][]string // ragged: a row holds cells up to its last non-empty one
	Hidden bool       // a sheet Excel hides
}

// Limits on one table. maxRows counts every row up to the last, empty ones
// too, so a sheet with one cell in row 1,000,000 stops at the limit.
const (
	maxRows      = 100_000
	maxCols      = 1_024
	maxCells     = 2_000_000 // across every sheet of a file
	maxIndexRows = 10_000    // rows per sheet that go into search
)

// headerScanRows is how many rows from the top the header guess looks at.
const headerScanRows = 20

// cellCounter counts the cells of one file against a cap, across its
// sheets. The zero value counts against maxCells; tests set max lower.
type cellCounter struct{ n, max int }

// add counts cells more, and fails once the file passes the cap.
func (c *cellCounter) add(cells int) error {
	limit := c.max
	if limit == 0 {
		limit = maxCells
	}
	c.n += cells
	if c.n > limit {
		return fmt.Errorf("the file has more than %s cells; Meru reads up to %s", thousands(limit), thousands(limit))
	}
	return nil
}

// tooManyRows is the error for a sheet past maxRows.
func tooManyRows(sheet string) error {
	return fmt.Errorf("sheet %s has more than %s rows; Meru reads up to %s", sheet, thousands(maxRows), thousands(maxRows))
}

// tooManyCols is the error for a sheet past maxCols.
func tooManyCols(sheet string) error {
	return fmt.Errorf("sheet %s has more than %s columns; Meru reads up to %s", sheet, thousands(maxCols), thousands(maxCols))
}

// thousands writes n with commas between groups of three digits, such as
// 100,000.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ReadTable returns the tables of the spreadsheet at p, which Check or Walk
// kept: one per sheet for an .xlsx file, one for a delimited text file. It
// opens p the way ReadText does, through an os.Root on its folder, and
// applies max_file_mb. reason is set when the file is one the indexer
// skips. It fails with ErrNotTable for any other kind of file, and when
// the file can't be parsed or passes a limit.
func (ix *Indexer) ReadTable(p string) (tables []Table, reason string, err error) {
	data, kind, reason, err := ix.readKind(p)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	if kind != KindXLSX && kind != KindCSV {
		return nil, "", fmt.Errorf("%s: %w", p, ErrNotTable)
	}
	tables, err = readTables(kind, p, data)
	return tables, "", err
}

// ErrNotTable means ReadTable got a file that isn't a spreadsheet or a
// delimited text file.
var ErrNotTable = errors.New("not an Excel or delimited text file")

// readTables parses data, the bytes of the file at p, as kind, which is
// KindXLSX or KindCSV.
func readTables(kind, p string, data []byte) ([]Table, error) {
	if kind == KindXLSX {
		return xlsxTables(data)
	}
	t, err := csvTable(tableName(p), filepath.Ext(p), data)
	if err != nil {
		return nil, err
	}
	return []Table{t}, nil
}

// tableName is the name a delimited text file's one table takes: the file
// name without its extension.
func tableName(p string) string {
	base := filepath.Base(p)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// tablesText renders tables for read_file and grep: one page per table,
// one line per row with its cells joined by " | ", so line N of a page is
// row N of the sheet. A line break inside a cell becomes a space, to keep
// that rule. names holds the table names.
func tablesText(tables []Table) (pages, names []string) {
	for _, t := range tables {
		var b strings.Builder
		for _, row := range t.Rows {
			for j, cell := range row {
				if j > 0 {
					b.WriteString(" | ")
				}
				b.WriteString(oneLine(cell))
			}
			b.WriteString("\n")
		}
		pages = append(pages, b.String())
		names = append(names, t.Name)
	}
	return pages, names
}

// oneLine replaces the line breaks in s with spaces.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// HeaderRow guesses which row of t names its columns, and returns its
// number, counting from 1, or 0 when no row looks like one. It takes the
// first row, within the top headerScanRows, whose non-empty cells cover at
// least half the width of the widest row there and are mostly not
// numbers. A title row such as "Q3 expenses" covers one cell, so it loses
// to the row of names below it.
func HeaderRow(t Table) int {
	top := t.Rows[:min(len(t.Rows), headerScanRows)]
	widest := 0
	for _, row := range top {
		widest = max(widest, filled(row))
	}
	if widest == 0 {
		return 0
	}
	for i, row := range top {
		n := filled(row)
		if n == 0 || 2*n < widest {
			continue
		}
		numbers := 0
		for _, cell := range row {
			if looksNumeric(cell) {
				numbers++
			}
		}
		if 2*numbers < n {
			return i + 1
		}
	}
	return 0
}

// filled counts the cells of row that hold something.
func filled(row []string) int {
	n := 0
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			n++
		}
	}
	return n
}

// looksNumeric reports whether s reads as a number or a date, once
// currency signs, thousands commas, a percent sign and parentheses are
// set aside. The header guess needs only a rough answer.
func looksNumeric(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	s = strings.Trim(s, "$€£¥₹()%+- ")
	s = strings.ReplaceAll(s, ",", "")
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	// A date such as 2026-05-03 or 03/04/2026 is all digits and separators.
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '-' || r == '/' || r == '.' || r == ':' || r == ' ':
		default:
			return false
		}
	}
	return digits > 0
}

// ColumnNames returns a name for each column of t, as wide as its widest
// row: the text in header row h (counting from 1), or the column's letter,
// such as "Column D", when h is 0 or the header cell is empty or repeats
// an earlier name.
func ColumnNames(t Table, h int) []string {
	width := 0
	for _, row := range t.Rows {
		width = max(width, len(row))
	}
	var header []string
	if h > 0 && h <= len(t.Rows) {
		header = t.Rows[h-1]
	}
	names := make([]string, width)
	seen := map[string]bool{}
	for j := range names {
		name := ""
		if j < len(header) {
			name = strings.TrimSpace(oneLine(header[j]))
		}
		if name == "" || seen[strings.ToLower(name)] {
			name = "Column " + ColumnLetter(j)
		}
		seen[strings.ToLower(name)] = true
		names[j] = name
	}
	return names
}

// ColumnLetter returns the spreadsheet letters for column j, counting from
// 0: A for 0, Z for 25, AA for 26.
func ColumnLetter(j int) string {
	s := ""
	for j++; j > 0; j = (j - 1) / 26 {
		s = string(rune('A'+(j-1)%26)) + s
	}
	return s
}

// chunkTable cuts tables into chunks of rows for search. Each chunk starts
// with the table's name and its column names, and each row is labelled by
// its number and its columns:
//
//	Sheet trip-expenses. Columns: Date, City, Category, Amount.
//	Row 12: Date 2026-05-03; City Amsterdam; Category Food tour; Amount EUR 64.50
//
// Labelling each value with its column lets keyword and vector search both
// match "food tour in Amsterdam" to the row. A chunk's Heading is the
// table's name, and StartLine and EndLine hold its first and last row
// numbers, which the citation prints as rows. Empty rows and the header row
// are left out, and only the first maxIndexRows rows of a table go in; the
// last chunk of a longer table says so.
func chunkTable(tables []Table, lim limits) []store.Chunk {
	var out []store.Chunk
	for _, t := range tables {
		out = append(out, chunkOneTable(t, lim)...)
	}
	return out
}

// chunkOneTable is chunkTable for one table.
func chunkOneTable(t Table, lim limits) []store.Chunk {
	h := HeaderRow(t)
	names := ColumnNames(t, h)
	lead := fmt.Sprintf("Sheet %s. Columns: %s.\n", t.Name, strings.Join(names, ", "))
	if h == 0 {
		lead = fmt.Sprintf("Sheet %s.\n", t.Name)
	}
	room := max(lim.max-utf8.RuneCountInString(lead), 80)

	var out []store.Chunk
	var b strings.Builder
	first, last, used := 0, 0, 0
	flush := func() {
		if first == 0 {
			return
		}
		out = append(out, store.Chunk{Heading: t.Name, Text: lead + strings.TrimRight(b.String(), "\n"), StartLine: first, EndLine: last})
		b.Reset()
		first, used = 0, 0
	}
	for i, row := range t.Rows {
		n := i + 1
		if n == h || filled(row) == 0 {
			continue
		}
		if n > maxIndexRows {
			flush()
			if len(out) > 0 {
				out[len(out)-1].Text += fmt.Sprintf("\nRows after %s are left out of search; read_file reads them.", thousands(maxIndexRows))
			}
			return out
		}
		line := rowLine(n, row, names, h == 0 || n < h)
		size := utf8.RuneCountInString(line) + 1
		if size > room {
			line = string([]rune(line)[:room-2]) + "…"
			size = room
		}
		if used+size > room {
			flush()
		}
		if first == 0 {
			first = n
		}
		last = n
		b.WriteString(line)
		b.WriteString("\n")
		used += size
	}
	flush()
	return out
}

// rowLine writes row n as one line of a chunk: "Row 12: Date 2026-05-03;
// City Amsterdam". A row above the header row, such as a title, or any row
// of a table with no header, has no column names to give, so its cells are
// joined alone.
func rowLine(n int, row, names []string, aboveHeader bool) string {
	var parts []string
	for j, cell := range row {
		cell = strings.TrimSpace(oneLine(cell))
		if cell == "" {
			continue
		}
		if !aboveHeader {
			cell = names[j] + " " + cell
		}
		parts = append(parts, cell)
	}
	return fmt.Sprintf("Row %d: %s", n, strings.Join(parts, "; "))
}
