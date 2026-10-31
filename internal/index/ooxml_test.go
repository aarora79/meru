// This file tests the ZIP layer under the Word and Excel readers: the
// signature checks and every limit on what one file may unpack into. It
// also holds the helpers the other Office tests use to build a .docx or
// .xlsx in memory, so no binary fixture sits in testdata.

package index

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// zipParts returns a ZIP archive holding parts, entry name to content.
func zipParts(t *testing.T, parts map[string]string) []byte {
	t.Helper()
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

// zipZeros returns a ZIP archive with one entry per size in sizes, each
// that many zero bytes. Zeros compress to almost nothing, which is how a
// ZIP bomb works.
func zipZeros(t *testing.T, sizes ...int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	zeros := make([]byte, 1<<20)
	for i, size := range sizes {
		f, err := w.Create(fmt.Sprintf("part%d.xml", i))
		if err != nil {
			t.Fatal(err)
		}
		for left := size; left > 0; left -= len(zeros) {
			if _, err := f.Write(zeros[:min(left, len(zeros))]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// xlsxFile builds a workbook from sheets, each a name and the XML inside
// its <sheetData>. sharedStrings and styles may be "" to leave the part
// out. A sheet whose name starts with "~" is hidden, without the "~".
func xlsxFile(t *testing.T, sheets [][2]string, sharedStrings, styles string) []byte {
	t.Helper()
	var wb, rels strings.Builder
	wb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	parts := map[string]string{}
	for i, s := range sheets {
		name, state := s[0], ""
		if hidden, ok := strings.CutPrefix(name, "~"); ok {
			name, state = hidden, ` state="hidden"`
		}
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d"%s r:id="rId%d"/>`, name, i+1, state, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		parts[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + s[1] + `</sheetData></worksheet>`
	}
	wb.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	parts["xl/workbook.xml"] = wb.String()
	parts["xl/_rels/workbook.xml.rels"] = rels.String()
	if sharedStrings != "" {
		parts["xl/sharedStrings.xml"] = `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` + sharedStrings + `</sst>`
	}
	if styles != "" {
		parts["xl/styles.xml"] = `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` + styles + `</styleSheet>`
	}
	return zipParts(t, parts)
}

// docxFile builds a Word document whose <w:body> holds body. styles is
// the inside of word/styles.xml, or "" to leave the part out.
func docxFile(t *testing.T, body, styles string) []byte {
	t.Helper()
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	parts := map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document ` + ns + `><w:body>` + body + `</w:body></w:document>`,
	}
	if styles != "" {
		parts["word/styles.xml"] = `<w:styles ` + ns + `>` + styles + `</w:styles>`
	}
	return zipParts(t, parts)
}

func TestOpenOOXML(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"password-protected", append(append([]byte{}, oleSignature...), make([]byte, 100)...), errEncrypted},
		{"not a zip", []byte("name,amount\nA,1\n"), errNotOOXML},
		{"empty", nil, errNotOOXML},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := openOOXML(tt.data); !errors.Is(err, tt.want) {
				t.Errorf("openOOXML = %v, want %v", err, tt.want)
			}
		})
	}

	t.Run("broken zip", func(t *testing.T) {
		data := zipParts(t, map[string]string{"a.xml": "<a/>"})
		if _, err := openOOXML(data[:len(data)-30]); err == nil {
			t.Error("openOOXML of a cut-off archive succeeded")
		}
	})

	t.Run("too many entries", func(t *testing.T) {
		parts := map[string]string{}
		for i := range maxZipEntries + 1 {
			parts[fmt.Sprintf("p%d", i)] = ""
		}
		_, err := openOOXML(zipParts(t, parts))
		if err == nil || !strings.Contains(err.Error(), "entries") {
			t.Errorf("openOOXML with %d entries = %v, want a limit error", maxZipEntries+1, err)
		}
	})
}

func TestPart(t *testing.T) {
	o, err := openOOXML(zipParts(t, map[string]string{"xl/workbook.xml": "<workbook/>"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := o.part("xl/workbook.xml"); err != nil || string(got) != "<workbook/>" {
		t.Errorf("part = %q, %v", got, err)
	}
	if _, err := o.part("../../.zshrc"); !errors.Is(err, errMissingPart) {
		t.Errorf("part of a name not in the archive = %v, want errMissingPart", err)
	}
}

// TestPartLimits checks the two caps on unpacked bytes, lowered so the
// test unpacks a few KB: one part past the part cap, and parts under it
// that add up past the file cap. The real caps are far larger, and an
// archive of zeros shows how little a ZIP bomb takes on disk.
func TestPartLimits(t *testing.T) {
	t.Run("one part", func(t *testing.T) {
		o, err := openOOXML(zipZeros(t, 4097))
		if err != nil {
			t.Fatal(err)
		}
		o.partCap = 4096
		if _, err := o.part("part0.xml"); err == nil || !strings.Contains(err.Error(), "part0.xml unpacks to more than 4096 bytes") {
			t.Errorf("part past the part cap = %v, want a limit error", err)
		}
	})
	t.Run("whole file", func(t *testing.T) {
		o, err := openOOXML(zipZeros(t, 3000, 3000))
		if err != nil {
			t.Fatal(err)
		}
		o.partCap, o.fileCap = 4096, 5000
		if _, err := o.part("part0.xml"); err != nil {
			t.Fatalf("first part: %v", err)
		}
		if _, err := o.part("part1.xml"); err == nil || !strings.Contains(err.Error(), "the file unpacks to more than 5000 bytes") {
			t.Errorf("parts past the file cap = %v, want a limit error", err)
		}
	})
	t.Run("the caps a file opens with", func(t *testing.T) {
		o, err := openOOXML(zipZeros(t, 1))
		if err != nil {
			t.Fatal(err)
		}
		if o.partCap != maxPartBytes || o.fileCap != maxUnpackedBytes {
			t.Errorf("caps = %d, %d; want %d, %d", o.partCap, o.fileCap, maxPartBytes, maxUnpackedBytes)
		}
	})
}
