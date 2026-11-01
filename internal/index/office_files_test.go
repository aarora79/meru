// This file tests Word, Excel and delimited text files through the paths
// the rest of Meru uses: Check and ReadText for the file tools, ReadTable,
// and a scan into the store.

package index

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/store"
)

// officeTree writes one file of each new kind, plus a .txt of tab-separated
// columns and an old .xls, under a fresh folder, and returns an Indexer
// over it and the folder.
func officeTree(t *testing.T) (*Indexer, *fakeSink, string) {
	t.Helper()
	root := tempRoot(t)
	sheet := `<row r="1"><c r="A1" t="inlineStr"><is><t>City</t></is></c><c r="B1" t="inlineStr"><is><t>Amount</t></is></c></row>` +
		`<row r="2"><c r="A2" t="inlineStr"><is><t>Amsterdam</t></is></c><c r="B2"><v>64.5</v></c></row>`
	writeFiles(t, root, map[string]string{
		"budget.xlsx":   string(xlsxFile(t, [][2]string{{"Invoices", sheet}, {"Notes", `<row r="1"><c r="A1"><v>1</v></c></row>`}}, "", "")),
		"contract.docx": string(docxFile(t, para(`<w:outlineLvl w:val="0"/>`, "Renewal")+para("", "Renews on 3 May."), "")),
		"trip.csv":      "City,Amount\nRome,3\n",
		"wine.tsv":      "Wine\tYear\nBarolo\t2015\n",
		"cols.txt":      "a\tb\n",
		"old.xls":       string(oleSignature),
	})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	return ix, sink, root
}

func TestOfficeCheck(t *testing.T) {
	ix, _, root := officeTree(t)
	tests := map[string]string{
		"budget.xlsx": "", "contract.docx": "", "trip.csv": "", "wine.tsv": "", "cols.txt": "",
		"old.xls": ReasonUnsupported,
	}
	for name, want := range tests {
		c, err := ix.Check(filepath.Join(root, name))
		if err != nil || c.Reason != want {
			t.Errorf("Check(%s) = %q, %v; want %q", name, c.Reason, err, want)
		}
	}
}

func TestOfficeReadText(t *testing.T) {
	ix, _, root := officeTree(t)
	tests := []struct {
		file string
		want Text
	}{
		{"budget.xlsx", Text{Kind: KindXLSX, Pages: []string{"City | Amount\nAmsterdam | 64.5\n", "1\n"}, Names: []string{"Invoices", "Notes"}}},
		{"contract.docx", Text{Kind: KindDocx, Pages: []string{"# Renewal\n\nRenews on 3 May.\n"}}},
		{"trip.csv", Text{Kind: KindCSV, Pages: []string{"City | Amount\nRome | 3\n"}}},
		{"wine.tsv", Text{Kind: KindCSV, Pages: []string{"Wine | Year\nBarolo | 2015\n"}}},
		{"cols.txt", Text{Kind: KindText, Pages: []string{"a\tb\n"}}},
	}
	for _, tt := range tests {
		got, reason, err := ix.ReadText(filepath.Join(root, tt.file))
		if err != nil || reason != "" || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ReadText(%s) = %+v, %q, %v; want %+v", tt.file, got, reason, err, tt.want)
		}
	}
}

func TestReadTable(t *testing.T) {
	ix, _, root := officeTree(t)
	tables, reason, err := ix.ReadTable(filepath.Join(root, "budget.xlsx"))
	if err != nil || reason != "" || len(tables) != 2 || tables[0].Name != "Invoices" || tables[0].Rows[1][1] != "64.5" {
		t.Errorf("ReadTable(budget.xlsx) = %+v, %q, %v", tables, reason, err)
	}
	tables, _, err = ix.ReadTable(filepath.Join(root, "wine.tsv"))
	if err != nil || len(tables) != 1 || tables[0].Name != "wine" {
		t.Errorf("ReadTable(wine.tsv) = %+v, %v", tables, err)
	}
	if _, _, err := ix.ReadTable(filepath.Join(root, "contract.docx")); !errors.Is(err, ErrNotTable) {
		t.Errorf("ReadTable of a Word file = %v, want ErrNotTable", err)
	}
	if _, reason, _ := ix.ReadTable(filepath.Join(root, "old.xls")); reason != ReasonUnsupported {
		t.Errorf("ReadTable of an .xls = %q, want %q", reason, ReasonUnsupported)
	}
}

func TestScanOfficeFiles(t *testing.T) {
	ix, sink, root := officeTree(t)
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Indexed != 5 || rep.Skipped[ReasonUnsupported] != 1 {
		t.Errorf("Scan = %+v; want 5 indexed and the .xls skipped", rep)
	}
	want := map[string]string{"budget.xlsx": KindXLSX, "contract.docx": KindDocx, "trip.csv": KindCSV, "wine.tsv": KindCSV}
	for name, kind := range want {
		e, ok := sink.entry(filepath.Join(root, name))
		if !ok || e.doc.Kind != kind || len(e.chunks) == 0 {
			t.Errorf("%s stored as %+v with %d chunks; want kind %s", name, e.doc, len(e.chunks), kind)
		}
	}
	e, _ := sink.entry(filepath.Join(root, "budget.xlsx"))
	c := e.chunks[0]
	if c.Heading != "Invoices" || c.StartLine != 2 || c.EndLine != 2 || c.Text != "Sheet Invoices in "+filepath.Join(root, "budget.xlsx")+". Columns: City, Amount.\nRow 2: City Amsterdam; Amount 64.5" {
		t.Errorf("first budget.xlsx chunk = %+v", c)
	}
}

// TestTableChunkFormatRechunks checks that a table file stored under the
// hash of an earlier chunk format is chunked again on the next scan, once,
// while a Markdown file with the same bytes keeps its plain hash (#208).
func TestTableChunkFormatRechunks(t *testing.T) {
	data := []byte("City,Amount\nRome,3\n")
	if contentHash(KindCSV, data) == contentHash(KindMarkdown, data) {
		t.Fatal("a table's hash doesn't carry tableChunkFormat")
	}
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"trip.csv": string(data)})
	ix, sink, _ := newTestIndexer(t, testConfig(root))
	if _, err := ix.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "trip.csv")
	// Make the store hold the file as an older merud left it: the bytes'
	// hash alone, and chunks with no path.
	e, _ := sink.entry(p)
	sum := sha256.Sum256(data)
	e.doc.Hash = hex.EncodeToString(sum[:])
	if err := sink.ReplaceDocument(t.Context(), e.doc, []store.Chunk{{Text: "Sheet trip. Columns: City, Amount."}}, e.vecs[:1]); err != nil {
		t.Fatal(err)
	}
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e, _ = sink.entry(p)
	if rep.Indexed != 1 || !strings.Contains(e.chunks[0].Text, "in "+p) {
		t.Errorf("scan after a format change = %+v, chunk %q; want the file chunked again with its path", rep, e.chunks[0].Text)
	}
	if rep, _ = ix.Scan(t.Context()); rep.Unchanged != 1 || rep.Indexed != 0 {
		t.Errorf("the scan after that = %+v; want the file unchanged", rep)
	}
}
