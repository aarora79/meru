// This file tests the Excel reader: each kind of cell, dates and
// percentages from the number formats, hidden sheets, the row and column
// limits, and a broken file.

package index

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// testStyles are the styles of the test workbooks: style 0 is plain, 1 a
// built-in date (14), 2 a custom date, 3 a percentage, 4 a custom format
// whose letters sit in quotes, so it isn't a date.
const testStyles = `<numFmts>` +
	`<numFmt numFmtId="164" formatCode="yyyy\-mm\-dd"/>` +
	`<numFmt numFmtId="165" formatCode="0.0%"/>` +
	`<numFmt numFmtId="166" formatCode="&quot;day &quot;0"/>` +
	`</numFmts><cellXfs>` +
	`<xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/><xf numFmtId="165"/><xf numFmtId="166"/>` +
	`</cellXfs>`

func TestXLSXTables(t *testing.T) {
	shared := `<si><t>Date</t></si><si><t>City</t></si><si><r><t>Food </t></r><r><t>tour</t></r></si>` +
		`<si><t>Amount</t></si><si><t>Amsterdam</t><rPh><t>ignored</t></rPh></si>`
	expenses := `<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>3</v></c></row>` +
		`<row r="2"><c r="A2" s="1"><v>46145</v></c><c r="B2" t="s"><v>4</v></c><c r="C2" t="inlineStr"><is><t>Museum</t></is></c><c r="D2"><v>84.760000000000005</v></c></row>` +
		// Row 3 is missing, and row 4 skips column B and has a formula.
		`<row r="4"><c r="A4" s="2"><v>46146.5</v></c><c r="C4" t="b"><v>1</v></c><c r="D4"><f>D2*2</f><v>169.52</v></c></row>` +
		`<row r="5"><c r="A5" t="e"><v>#N/A</v></c><c r="B5" s="3"><v>0.125</v></c><c r="C5" s="4"><v>7</v></c><c r="D5" t="str"><f>"x"</f><v>text result</v></c></row>` +
		// An empty, formatted row far down leaves no trailing rows.
		`<row r="900000"><c r="A900000" s="1"/></row>`
	data := xlsxFile(t, [][2]string{{"Expenses", expenses}, {"~Lookup", `<row r="1"><c r="A1"><v>1</v></c></row>`}}, shared, testStyles)

	tables, err := xlsxTables(data)
	if err != nil {
		t.Fatalf("xlsxTables: %v", err)
	}
	want := []Table{
		{Name: "Expenses", Rows: [][]string{
			{"Date", "City", "Food tour", "Amount"},
			{"2026-05-03", "Amsterdam", "Museum", "84.76"},
			nil,
			{"2026-05-04 12:00", "", "TRUE", "169.52"},
			{"#N/A", "12.5%", "7", "text result"},
		}},
		{Name: "Lookup", Rows: [][]string{{"1"}}, Hidden: true},
	}
	if !reflect.DeepEqual(tables, want) {
		t.Errorf("xlsxTables =\n%+v\nwant\n%+v", tables, want)
	}
}

func TestXLSXLimits(t *testing.T) {
	tests := []struct {
		name  string
		sheet string
		want  string
	}{
		{"a value past maxRows", fmt.Sprintf(`<row r="%d"><c r="A%d"><v>1</v></c></row>`, maxRows+1, maxRows+1), "rows"},
		{"a value past maxCols", `<row r="1"><c r="ZZZ1"><v>1</v></c></row>`, "columns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := xlsxTables(xlsxFile(t, [][2]string{{"Log", tt.sheet}}, "", ""))
			if err == nil || !strings.Contains(err.Error(), "sheet Log has more than") || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("xlsxTables = %v, want a %s limit error", err, tt.want)
			}
		})
	}

	t.Run("cells across sheets", func(t *testing.T) {
		// One counter covers every sheet of a file, lowered here to 5.
		cells := cellCounter{max: 5}
		sheet := []byte(`<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c><c r="B1"><v>2</v></c><c r="C1"><v>3</v></c></row></sheetData></worksheet>`)
		if _, err := readSheet(sheet, "A", xlsxBook{}, &cells); err != nil {
			t.Fatalf("first sheet: %v", err)
		}
		if _, err := readSheet(sheet, "B", xlsxBook{}, &cells); err == nil || !strings.Contains(err.Error(), "more than 5 cells") {
			t.Errorf("second sheet past the cell cap = %v, want a limit error", err)
		}
		var zero cellCounter
		if err := zero.add(maxCells); err != nil {
			t.Errorf("the zero counter refused maxCells: %v", err)
		}
		if err := zero.add(1); err == nil {
			t.Error("the zero counter took one cell past maxCells")
		}
	})
}

func TestXLSXBroken(t *testing.T) {
	tests := []struct {
		name  string
		parts map[string]string
	}{
		{"no workbook", map[string]string{"xl/other.xml": "<a/>"}},
		{"bad XML in a sheet", map[string]string{
			"xl/workbook.xml":            `<workbook><sheets><sheet name="S" r:id="rId1" xmlns:r="r"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row><c><v>1</c></row>`,
		}},
		{"an entity the document defines", map[string]string{
			"xl/workbook.xml":            `<!DOCTYPE w [<!ENTITY a "aaaaaaaaaa">]><workbook><sheets><sheet name="&a;"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships/>`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := xlsxTables(zipParts(t, tt.parts)); err == nil {
				t.Error("xlsxTables succeeded on a broken file")
			}
		})
	}
	if _, err := xlsxTables(append([]byte{}, oleSignature...)); !errors.Is(err, errEncrypted) {
		t.Errorf("xlsxTables of a password-protected file = %v, want errEncrypted", err)
	}
}

func TestExcelDate(t *testing.T) {
	tests := []struct {
		serial   float64
		date1904 bool
		want     string
	}{
		{46145, false, "2026-05-03"},
		{61, false, "1900-03-01"},
		{46145.75, false, "2026-05-03 18:00"},
		{0.5, false, "12:00"},
		{0, true, "1904-01-01"},
		{-1, false, "-1"},
	}
	for _, tt := range tests {
		if got := excelDate(tt.serial, tt.date1904); got != tt.want {
			t.Errorf("excelDate(%v, %v) = %q, want %q", tt.serial, tt.date1904, got, tt.want)
		}
	}
}

func TestDateCode(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"yyyy-mm-dd", true},
		{"[$-409]d-mmm-yy;@", true},
		{"m/d/yyyy h:mm", true},
		{"0.00", false},
		{"General", false},
		{`"day "0`, false},
		{`[Red]#,##0`, false},
		{`\d0`, false},
	}
	for _, tt := range tests {
		if got := dateCode(tt.code); got != tt.want {
			t.Errorf("dateCode(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestRefColumn(t *testing.T) {
	tests := []struct {
		ref  string
		col  int
		isOK bool
	}{
		{"A1", 0, true}, {"Z9", 25, true}, {"AA10", 26, true}, {"AMJ1", 1023, true}, {"7", 0, false},
	}
	for _, tt := range tests {
		col, ok := refColumn(tt.ref)
		if col != tt.col || ok != tt.isOK {
			t.Errorf("refColumn(%q) = %d, %v; want %d, %v", tt.ref, col, ok, tt.col, tt.isOK)
		}
	}
}
