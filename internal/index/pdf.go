// This file chunks PDFs: it pulls the text out of each page with the
// pure-Go ledongthuc/pdf reader and packs each page's paragraphs on their
// own, so every chunk knows its page.

package index

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/aarora79/meru/internal/store"
	"github.com/ledongthuc/pdf"
)

// errNoText means a PDF opened but held no text, which usually means a
// scanned document: pictures of pages, with no text layer. Meru doesn't run
// OCR (optical character recognition).
var errNoText = errors.New("pdf has no text layer (a scan?)")

// chunkPDF extracts the text of each page of data and chunks it. Chunks
// never cross a page, and each carries its 1-based page number. PDF text
// has no reliable paragraph marks, so paragraphs here are whatever blank
// lines the extractor produced; pack splits long runs at line breaks.
//
// It fails when the file isn't a PDF the library can read, is encrypted, or
// holds no text at all. The caller skips such a file and logs why.
//
// PDF parsing is the least tested path in the indexer: the library panics
// on some malformed files instead of returning an error, so chunkPDF
// recovers from a panic and reports it as an error. A deferred function
// that calls recover() stops a panic from crashing merud.
func chunkPDF(data []byte, lim limits) (chunks []store.Chunk, err error) {
	defer func() {
		if r := recover(); r != nil {
			chunks, err = nil, fmt.Errorf("read pdf: parser failed: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}
	for n := 1; n <= r.NumPage(); n++ {
		page := r.Page(n)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return nil, fmt.Errorf("read pdf page %d: %w", n, err)
		}
		pageChunks := toChunks(text, pack(text, paragraphs(text, span{0, len(text)}), lim), "", nil)
		for i := range pageChunks {
			pageChunks[i].Page = n
		}
		chunks = append(chunks, pageChunks...)
	}
	if len(chunks) == 0 {
		return nil, errNoText
	}
	return chunks, nil
}
