//go:build e2e

// This file checks Word, Excel and CSV files with the real binaries: merud
// indexes a CSV file, a TSV file and a Word document, skips an old .xls,
// and meru cites the rows of a sheet that answer a question.

package e2e

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// officeFolder makes a folder with a wine list in CSV, a bank export in
// TSV, a Word document and an old .xls file, and returns its path with
// symlinks resolved.
func officeFolder(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru-office-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	var docx bytes.Buffer
	w := zip.NewWriter(&docx)
	f, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Brightline renews on 3 May 2027.</w:t></w:r></w:p></w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"wine-cellar.csv": []byte("Wine,Year,Note\nBarolo Cannubi,2015,set aside for our 15th anniversary\nRioja Reserva,2019,weeknight\n"),
		"bank-export.tsv": []byte("Date\tPayee\tAmount\n2026-05-03\tTram pass\t12.00\n"),
		"contract.docx":   docx.Bytes(),
		"old.xls":         {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAnswerCitesSheetRows(t *testing.T) {
	t.Parallel()
	dir := officeFolder(t)
	s := startIndexStack(t, dir)

	// The CSV, TSV and Word files count; the .xls doesn't.
	waitForStatus(t, s.home, "3 files", "Scanning:   no")

	const answer = "The Barolo Cannubi 2015 [1]."
	s.fake.enqueue(t, fastModel, searchRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: answer})
	res := runMeru(t, s.home, "Which wine did I set aside for our 15th anniversary?")
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s\nlog:\n%s", res.code, res.stderr, s.home.log())
	}

	// The rows reached the model labelled with their columns.
	prompt := lastMainPrompt(t, s.fake)
	for _, want := range []string{
		"Sheet wine-cellar. Columns: Wine, Year, Note.",
		"Row 2: Wine Barolo Cannubi; Year 2015; Note set aside for our 15th anniversary",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("main model's prompt lacks %q:\n%s", want, prompt)
		}
	}

	// meru cites the sheet by its rows.
	wantOut := answer + "\n\nSources:\n[1] " + filepath.Join(dir, "wine-cellar.csv") + `, "wine-cellar", rows 2–3`
	if !strings.HasPrefix(res.stdout, wantOut) {
		t.Errorf("stdout = %q\nwant it to start with %q", res.stdout, wantOut)
	}
}
