// This file tests the Word reader: headings from styles and outline
// levels, lists, tables, and the text that stays out.

package index

import (
	"errors"
	"testing"
)

// para is a Word paragraph holding text, with props as its paragraph
// properties.
func para(props, text string) string {
	return `<w:p><w:pPr>` + props + `</w:pPr><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

func TestDocxText(t *testing.T) {
	styles := `<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="MyHead"><w:name w:val="Custom"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/></w:style>` +
		`<w:style w:type="character" w:styleId="Heading1Char"><w:name w:val="heading 1 char"/></w:style>`
	body := para(`<w:pStyle w:val="Title"/>`, "Brightline agreement") +
		para(`<w:pStyle w:val="Heading1"/>`, "Term") +
		para(`<w:pStyle w:val="MyHead"/>`, "Renewal") +
		`<w:p><w:r><w:t>Renews on </w:t></w:r><w:del><w:r><w:delText>1 May</w:delText></w:r></w:del><w:r><w:t>3 May 2027.</w:t><w:br/><w:t>Second line</w:t></w:r></w:p>` +
		para(`<w:numPr><w:ilvl w:val="0"/></w:numPr>`, "First item") +
		`<w:p><w:r><w:fldChar/><w:instrText>PAGE</w:instrText><w:t>4</w:t></w:r></w:p>` +
		`<w:tbl>` +
		`<w:tr><w:tc>` + para("", "City") + `</w:tc><w:tc>` + para("", "Amount") + `</w:tc></w:tr>` +
		`<w:tr><w:tc>` + para("", "Amsterdam") + `</w:tc><w:tc>` + para("", "64.50") + para("", "EUR") +
		`<w:tbl><w:tr><w:tc>` + para("", "inner") + `</w:tc></w:tr></w:tbl></w:tc></w:tr>` +
		`</w:tbl>` +
		para(`<w:outlineLvl w:val="2"/>`, "Notes")

	got, err := DocxText(docxFile(t, body, styles))
	if err != nil {
		t.Fatalf("DocxText: %v", err)
	}
	want := "# Brightline agreement\n\n# Term\n\n## Renewal\n\nRenews on 3 May 2027.\nSecond line\n\n- First item\n\n4\n\n" +
		"City | Amount\nAmsterdam | 64.50 EUR inner\n\n### Notes\n"
	if got != want {
		t.Errorf("DocxText =\n%q\nwant\n%q", got, want)
	}
}

func TestChunkDocx(t *testing.T) {
	body := para(`<w:outlineLvl w:val="0"/>`, "Term") + para(`<w:outlineLvl w:val="1"/>`, "Renewal") + para("", "Renews on 3 May 2027.")
	chunks, err := chunkDocx(docxFile(t, body, ""), newLimits(500, 50))
	if err != nil {
		t.Fatalf("chunkDocx: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Heading != "Term > Renewal" || chunks[0].StartLine != 0 || chunks[0].EndLine != 0 {
		t.Errorf("chunkDocx = %+v, want one chunk under Term > Renewal with no lines", chunks)
	}
	if _, err := chunkDocx(docxFile(t, para("", " "), ""), newLimits(500, 50)); err == nil {
		t.Error("chunkDocx of an empty document succeeded")
	}
	if _, err := DocxText(append([]byte{}, oleSignature...)); !errors.Is(err, errEncrypted) {
		t.Errorf("DocxText of a password-protected file = %v, want errEncrypted", err)
	}
	if _, err := DocxText(zipParts(t, map[string]string{"word/other.xml": "<a/>"})); !errors.Is(err, errMissingPart) {
		t.Errorf("DocxText with no document part = %v, want errMissingPart", err)
	}
}
