// This file holds what the index knows about images: which files count as
// one, the check that a file's bytes really are an image, and how an image
// becomes one chunk with one vector. builtin uses the same checks for an
// image the user attaches, so the two can't drift. See ARCHITECTURE.md,
// "Indexing".

package index

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
)

// MaxImageBytes caps an image Meru reads, 20 MiB. A phone photo runs from 2
// to 12 MB, so this leaves room, and the embedding model sees an image at a
// few hundred pixels across whatever its size. Images get their own cap,
// not [index] max_file_mb, which is set for text.
const MaxImageBytes = 20 << 20

// imageSniffBytes is how much of a file net/http's DetectContentType reads
// to name its type.
const imageSniffBytes = 512

// imageExts lists the file name endings Meru treats as images, and
// imageTypes the types DetectContentType reports for them. These are the
// formats Ollama embeds and its vision models read. HEIC isn't one: Ollama
// refuses it, and Go's standard library can't decode it.
var (
	imageExts  = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}
	imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
)

// IsImageName reports whether name ends in one of the image endings, such
// as ".png" or ".JPG". The name alone proves nothing: IsImageData checks
// the bytes.
func IsImageName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range imageExts {
		if ext == e {
			return true
		}
	}
	return false
}

// IsImageData reports whether head, the first bytes of a file, starts like
// a PNG, JPEG, GIF or WebP file. DetectContentType reads the magic bytes
// each format opens with, so a text file renamed to .png fails.
func IsImageData(head []byte) bool {
	t := http.DetectContentType(head[:min(len(head), imageSniffBytes)])
	for _, it := range imageTypes {
		if t == it {
			return true
		}
	}
	return false
}

// ReadsImages reports whether this indexer reads image files: [index]
// images is on and the embedding model takes images.
func (ix *Indexer) ReadsImages() bool { return ix.images }

// imageChunk is the one chunk an image's document holds. The model never
// reads an image's chunk text; keyword search does, so the text names the
// file and the folder it sits in, and "find the screenshot in img" can
// match by name as well as by look.
func imageChunk(p string) store.Chunk {
	name := filepath.Base(p)
	return store.Chunk{
		Heading: name,
		Text:    "Image " + name + " in " + filepath.Dir(p),
	}
}

// errImageRefused marks an image Ollama wouldn't embed, such as a file
// that sniffs as a PNG but is cut short. That fails the one file; any
// other embed error stops the scan, as it would fail every file after it.
var errImageRefused = errors.New("ollama refused the image")

// embedImage turns one image's bytes into its vector, through the same
// Embed call as text. An HTTP 400 from Ollama comes back wrapped in
// errImageRefused.
func (ix *Indexer) embedImage(ctx context.Context, data []byte) (engine.Vector, error) {
	vecs, err := ix.eng.Embed(ctx, []engine.Input{{Image: data}})
	// errors.As finds an *engine.APIError anywhere in err's chain and
	// copies it into apiErr.
	var apiErr *engine.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest:
		return nil, fmt.Errorf("%w: %s", errImageRefused, apiErr.Message)
	case err != nil:
		return nil, err
	case len(vecs) != 1:
		return nil, fmt.Errorf("engine returned %d vectors for 1 image", len(vecs))
	}
	return vecs[0], nil
}
