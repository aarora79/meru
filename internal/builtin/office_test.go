// This file tests read_file and grep on Excel, CSV and Word files, and the
// line they give for an old .xls or .doc file.

package builtin

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/index"
)

// workbook returns an .xlsx file whose sheets hold rows of inline text,
// one sheet per entry in sheets, in order.
func workbook(t *testing.T, names []string, sheets [][][]string) []byte {
	t.Helper()
	parts := map[string]string{}
	var wb, rels strings.Builder
	wb.WriteString(`<workbook xmlns:r="r"><sheets>`)
	rels.WriteString(`<Relationships>`)
	for i, rows := range sheets {
		fmt.Fprintf(&wb, `<sheet name="%s" r:id="rId%d"/>`, names[i], i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		var sheet strings.Builder
		sheet.WriteString(`<worksheet><sheetData>`)
		for r, row := range rows {
			fmt.Fprintf(&sheet, `<row r="%d">`, r+1)
			for c, cell := range row {
				if cell != "" {
					fmt.Fprintf(&sheet, `<c r="%s%d" t="inlineStr"><is><t>%s</t></is></c>`, index.ColumnLetter(c), r+1, cell)
				}
			}
			sheet.WriteString(`</row>`)
		}
		sheet.WriteString(`</sheetData></worksheet>`)
		parts[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = sheet.String()
	}
	wb.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	parts["xl/workbook.xml"] = wb.String()
	parts["xl/_rels/workbook.xml.rels"] = rels.String()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// officeTools returns built-in tools over a folder holding a workbook, a
// CSV file and an old .xls, and the folder.
func officeTools(t *testing.T) (*Tools, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"budget.xlsx": workbook(t, []string{"Summary", "Invoices"}, [][][]string{
			{{"Total", "277"}},
			{{"Invoices for May"}, nil, {"No", "City", "Amount"}, {"14", "Amsterdam", "64.50"}},
		}),
		"trip.csv": []byte("City,Amount\nRome,3\nAmsterdam,12\n"),
		"old.xls":  {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
		"old.doc":  {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := index.New(config.Index{Folders: []string{root}, MaxFileMB: 1}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(root, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil), root
}

func TestReadFileSheets(t *testing.T) {
	tools, root := officeTools(t)
	res := callTool(t, tools, context.Background(), ReadFile, `{"path":`+jsonPath(filepath.Join(root, "budget.xlsx"))+`}`)
	want := "--- sheet Summary ---\nTotal | 277\n--- sheet Invoices ---\nInvoices for May\n\nNo | City | Amount\n14 | Amsterdam | 64.50\n"
	if res.IsError || !strings.HasSuffix(res.Text, want) {
		t.Errorf("read_file of a workbook =\n%s\nwant it to end with\n%s", res.Text, want)
	}
	res = callTool(t, tools, context.Background(), ReadFile, `{"path":`+jsonPath(filepath.Join(root, "trip.csv"))+`}`)
	if res.IsError || !strings.HasSuffix(res.Text, "City | Amount\nRome | 3\nAmsterdam | 12\n") {
		t.Errorf("read_file of a CSV file =\n%s", res.Text)
	}
}

func TestGrepRows(t *testing.T) {
	tools, root := officeTools(t)
	res := callTool(t, tools, context.Background(), Grep, `{"pattern":"amsterdam","path":`+jsonPath(root)+`}`)
	for _, want := range []string{"budget.xlsx (sheet Invoices, row 4): 14 | Amsterdam | 64.50", "trip.csv (row 3): Amsterdam | 12"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("grep result lacks %q:\n%s", want, res.Text)
		}
	}
}

func TestOldOfficeFormats(t *testing.T) {
	tools, root := officeTools(t)
	for name, want := range map[string]string{"old.xls": "save it as .xlsx", "old.doc": "save it as .docx"} {
		res := callTool(t, tools, context.Background(), ReadFile, `{"path":`+jsonPath(filepath.Join(root, name))+`}`)
		if !res.IsError || !strings.Contains(res.Text, want) {
			t.Errorf("read_file of %s = %q, want an error that says to %s", name, res.Text, want)
		}
	}
}
