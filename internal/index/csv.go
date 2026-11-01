// This file reads delimited text files, .csv, .tsv and .tab, into one
// table. It picks the delimiter from the first lines (a comma, a
// semicolon, a tab or a pipe), drops a byte-order mark, and reads a file
// that isn't UTF-8 as Windows-1252, the encoding Excel uses on Windows.

package index

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// delimiters are the characters a delimited text file may split its
// fields on, in the order a tie goes when the extension doesn't decide.
var delimiters = []rune{',', '\t', ';', '|'}

// sniffLines is how many lines the delimiter guess reads.
const sniffLines = 20

// noDelimiter splits nothing: a file with no consistent delimiter reads as
// one column. It is the ASCII unit separator, which text files don't hold.
const noDelimiter = '\x1f'

// csvTable reads data, the bytes of a delimited text file named name with
// extension ext, into one table. It fails when the file passes maxRows,
// maxCols or maxCells, and when a line can't be read.
func csvTable(name, ext string, data []byte) (Table, error) {
	text := decodeText(data)
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = pickDelimiter(text, strings.ToLower(ext))
	// LazyQuotes lets a stray quote inside a field through, and
	// FieldsPerRecord -1 lets rows differ in length: exports are often
	// loose, and a strict reader would refuse the whole file over one row.
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	var cells cellCounter
	t := Table{Name: name}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Table{}, fmt.Errorf("read %s: %w", name, err)
		}
		if len(t.Rows) >= maxRows {
			return Table{}, tooManyRows(name)
		}
		if len(rec) > maxCols {
			return Table{}, tooManyCols(name)
		}
		// Drop trailing empty fields, as the Excel reader drops trailing
		// empty cells.
		for len(rec) > 0 && strings.TrimSpace(rec[len(rec)-1]) == "" {
			rec = rec[:len(rec)-1]
		}
		if err := cells.add(len(rec)); err != nil {
			return Table{}, err
		}
		t.Rows = append(t.Rows, rec)
	}
	for len(t.Rows) > 0 && len(t.Rows[len(t.Rows)-1]) == 0 {
		t.Rows = t.Rows[:len(t.Rows)-1]
	}
	return t, nil
}

// decodeText returns data as a string of UTF-8, without a leading
// byte-order mark. Bytes that aren't valid UTF-8 are read as Windows-1252,
// so "€" and accented letters from an Excel export on Windows survive.
func decodeText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data))
	for _, c := range data {
		b.WriteRune(cp1252(c))
	}
	return b.String()
}

// cp1252High holds the characters Windows-1252 puts at 0x80 to 0x9F, where
// Latin-1 has control codes. 0 marks the five codes it leaves undefined.
var cp1252High = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}

// cp1252 returns the character byte c stands for in Windows-1252. Outside
// 0x80 to 0x9F it matches Latin-1, whose codes equal Unicode's.
func cp1252(c byte) rune {
	if c >= 0x80 && c <= 0x9F {
		if r := cp1252High[c-0x80]; r != 0 {
			return r
		}
		return utf8.RuneError
	}
	return rune(c)
}

// pickDelimiter guesses the delimiter of text, a delimited file with
// extension ext, from its first sniffLines lines that hold anything. A
// candidate wins when it appears the same number of times, at least once,
// outside quotes on every one of those lines. When more than one wins, a
// tab wins for .tsv and .tab and a comma for .csv, then the order of
// delimiters. When none wins, the extension's delimiter is used if the
// first line holds it, since a quoted field with a line break can make
// the counts differ; otherwise the file reads as one column.
func pickDelimiter(text, ext string) rune {
	preferred := ','
	if ext == ".tsv" || ext == ".tab" {
		preferred = '\t'
	}
	var lines []string
	for line := range strings.Lines(text) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) == sniffLines {
			break
		}
	}
	if len(lines) == 0 {
		return preferred
	}
	var winners []rune
	for _, d := range delimiters {
		want := countOutsideQuotes(lines[0], d)
		if want == 0 {
			continue
		}
		consistent := true
		for _, line := range lines[1:] {
			if countOutsideQuotes(line, d) != want {
				consistent = false
				break
			}
		}
		if consistent {
			winners = append(winners, d)
		}
	}
	for _, d := range winners {
		if d == preferred {
			return d
		}
	}
	if len(winners) > 0 {
		return winners[0]
	}
	if countOutsideQuotes(lines[0], preferred) > 0 {
		return preferred
	}
	return noDelimiter
}

// countOutsideQuotes counts d in line where it sits outside double quotes.
func countOutsideQuotes(line string, d rune) int {
	n := 0
	quoted := false
	for _, r := range line {
		switch {
		case r == '"':
			quoted = !quoted
		case r == d && !quoted:
			n++
		}
	}
	return n
}
