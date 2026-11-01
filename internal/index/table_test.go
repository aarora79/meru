// This file tests what tables share: the header guess, column names, the
// text read_file and grep see, and chunking by rows.

package index

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestHeaderRow(t *testing.T) {
	tests := []struct {
		name string
		rows [][]string
		want int
	}{
		{"first row", [][]string{{"City", "Amount"}, {"Rome", "3"}}, 1},
		{"under a title", [][]string{{"Q3 expenses"}, nil, {"Date", "City", "Amount"}, {"2026-05-03", "Rome", "3"}}, 3},
		{"numbers only", [][]string{{"1", "2"}, {"3", "4"}}, 0},
		{"dates are not names", [][]string{{"2026-05-03", "03/04/2026", "x"}, {"Date", "Due", "Note"}}, 2},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		if got := HeaderRow(Table{Rows: tt.rows}); got != tt.want {
			t.Errorf("%s: HeaderRow = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestColumnNames(t *testing.T) {
	tab := Table{Rows: [][]string{{"City", "", "city", "Amount"}, {"a", "b", "c", "d", "e"}}}
	want := []string{"City", "Column B", "Column C", "Amount", "Column E"}
	if got := ColumnNames(tab, 1); !reflect.DeepEqual(got, want) {
		t.Errorf("ColumnNames = %q, want %q", got, want)
	}
	if got := ColumnNames(tab, 0); got[0] != "Column A" {
		t.Errorf("ColumnNames with no header = %q, want letters", got)
	}
	for j, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 701: "ZZ", 702: "AAA"} {
		if got := ColumnLetter(j); got != want {
			t.Errorf("ColumnLetter(%d) = %q, want %q", j, got, want)
		}
	}
}

func TestTablesText(t *testing.T) {
	tables := []Table{
		{Name: "Invoices", Rows: [][]string{{"No", "Note"}, nil, {"14", "two\nlines"}}},
		{Name: "Empty"},
	}
	pages, names := tablesText(tables)
	want := []string{"No | Note\n\n14 | two lines\n", ""}
	if !reflect.DeepEqual(pages, want) || !reflect.DeepEqual(names, []string{"Invoices", "Empty"}) {
		t.Errorf("tablesText = %q, %q; want %q", pages, names, want)
	}
}

func TestChunkTable(t *testing.T) {
	tab := Table{Name: "trip-expenses", Rows: [][]string{
		{"Trip to the Netherlands"},
		{"Date", "City", "Category", "Amount"},
		{"2026-05-03", "Amsterdam", "Food tour", "EUR 64.50"},
		nil,
		{"2026-05-04", "Utrecht", "", "EUR 12.00"},
	}}
	chunks := chunkTable([]Table{tab}, "/home/u/trips/trip-expenses.csv", newLimits(500, 50))
	if len(chunks) != 1 {
		t.Fatalf("chunkTable = %d chunks, want 1: %+v", len(chunks), chunks)
	}
	c := chunks[0]
	wantText := "Sheet trip-expenses in /home/u/trips/trip-expenses.csv. Columns: Date, City, Category, Amount.\n" +
		"Row 1: Trip to the Netherlands\n" +
		"Row 3: Date 2026-05-03; City Amsterdam; Category Food tour; Amount EUR 64.50\n" +
		"Row 5: Date 2026-05-04; City Utrecht; Amount EUR 12.00"
	if c.Text != wantText || c.Heading != "trip-expenses" || c.StartLine != 1 || c.EndLine != 5 {
		t.Errorf("chunk = %q under %q, rows %d-%d; want\n%q", c.Text, c.Heading, c.StartLine, c.EndLine, wantText)
	}
}

func TestChunkTableSplitsAndStops(t *testing.T) {
	rows := [][]string{{"Day", "Note"}}
	for i := range maxIndexRows + 5 {
		rows = append(rows, []string{fmt.Sprint(i), strings.Repeat("x", 40)})
	}
	lim := newLimits(100, 10) // 400 characters a chunk
	chunks := chunkTable([]Table{{Name: "log", Rows: rows}}, "/home/u/log.csv", lim)
	if len(chunks) < 2 {
		t.Fatalf("chunkTable = %d chunks, want many", len(chunks))
	}
	prev := 0
	for _, c := range chunks {
		if !strings.HasPrefix(c.Text, "Sheet log in /home/u/log.csv. Columns: Day, Note.\n") {
			t.Fatalf("chunk doesn't start with the sheet line: %q", c.Text)
		}
		if n := len([]rune(c.Text)); n > lim.max+200 {
			t.Errorf("chunk of %d characters, over the limit", n)
		}
		if c.StartLine != prev+1 && prev != 0 {
			t.Errorf("chunk starts at row %d after one ending at %d", c.StartLine, prev)
		}
		prev = c.EndLine
	}
	last := chunks[len(chunks)-1]
	if last.EndLine != maxIndexRows || !strings.HasSuffix(last.Text, "left out of search; analyze_table and read_file read them.") {
		t.Errorf("last chunk ends at row %d with %q; want row %d and the note", last.EndLine, last.Text[max(0, len(last.Text)-80):], maxIndexRows)
	}

	long := Table{Name: "wide", Rows: [][]string{{"Note"}, {strings.Repeat("y", 5000)}}}
	got := chunkTable([]Table{long}, "/home/u/wide.csv", lim)
	if len(got) != 1 || len([]rune(got[0].Text)) > lim.max || !strings.HasSuffix(got[0].Text, "…") {
		t.Errorf("a long row = %d chunks of %d characters; want one cut chunk", len(got), len([]rune(got[0].Text)))
	}
}
