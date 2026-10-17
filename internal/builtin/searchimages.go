// This file holds search_images, the built-in tool that finds the indexed
// images most like a description in words, or like an image the user
// names by its path. It searches the same index as search_files, limited
// to images. See ARCHITECTURE.md, "Retrieval".

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

// SearchImages is the image search tool's name, as the model sees it.
const SearchImages = "search_images"

// defaultImageLimit is how many images a call returns when the model
// doesn't say. The answer can only list them, so a few are plenty.
const defaultImageLimit = 8

// ImageSearcher runs the search behind search_images. merud passes an
// adapter around retrieve.SearchImages; tests pass a fake.
type ImageSearcher interface {
	SearchImages(ctx context.Context, q retrieve.ImageQuery, n int) ([]retrieve.Result, error)
}

// UseImageSearch turns search_images on: s runs its searches. merud calls
// it once, before the first turn, when the index holds images. Without it,
// search_images stays off.
func (t *Tools) UseImageSearch(s ImageSearcher) {
	t.imageSearch = s
}

// searchImagesArgs is the JSON object the model sends to search_images.
type searchImagesArgs struct {
	Query string `json:"query"`
	Image string `json:"image"`
	Limit int    `json:"limit"`
}

// searchImages runs one image search and returns the images as a numbered
// list for the model, with their citations. With image set it finds images
// like that one; otherwise images that fit query. It fails, with text the
// model reads, on bad arguments, a path it may not read, and a failed
// search.
func (t *Tools) searchImages(ctx context.Context, raw json.RawMessage) (string, []rpc.Citation, error) {
	var a searchImagesArgs
	if err := decode(SearchImages, raw, &a); err != nil {
		return "", nil, err
	}
	a.Query, a.Image = strings.TrimSpace(a.Query), strings.TrimSpace(a.Image)
	if a.Query == "" && a.Image == "" {
		return "", nil, errors.New("search_images: pass query, words for what the picture shows, or image, the path of a picture to find others like")
	}
	if a.Limit == 0 {
		a.Limit = defaultImageLimit
	}
	if a.Limit < 1 || a.Limit > maxSearchLimit {
		return "", nil, fmt.Errorf("search_images: limit %d is out of range; pass 1 to %d", a.Limit, maxSearchLimit)
	}
	q := retrieve.ImageQuery{Text: a.Query}
	what := fmt.Sprintf("%q", a.Query)
	if a.Image != "" {
		b, err := t.readImageFile(a.Image)
		if err != nil {
			return "", nil, err
		}
		q.Image, what = b, "images like "+t.show(a.Image)
	}
	results, err := t.imageSearch.SearchImages(ctx, q, a.Limit)
	if err != nil {
		return "", nil, fmt.Errorf("search_images: the search failed: %v", err)
	}
	if len(results) == 0 {
		return "search_images found no images for " + what + ". The index holds no images yet, or none match.", nil, nil
	}
	first := dispatch.CiteNumbers(ctx, len(results))
	sources := make([]rpc.Citation, len(results))
	var b strings.Builder
	fmt.Fprintf(&b, "Images in the user's folders for %s, best first. You can't see them; "+
		"name the ones that fit and cite them by number, like [%d], so the user can open them.\n", what, first)
	for i, r := range results {
		r.Path = t.show(r.Path)
		b.WriteString(retrieve.Cite(first+i, r) + "\n")
		sources[i] = rpc.Citation{N: first + i, Path: r.Path, Score: r.Score, Kind: r.CitationKind()}
	}
	return b.String(), sources, nil
}

// readImageFile reads the picture at p for an image search. p must be an
// image the file tools may reach, in the [index] folders or Meru's output
// folder, where attached images land, under the same rules as read_file:
// no secret, hidden or ignored file, no symbolic link. read_file itself
// refuses images, since it reads text; this reads the bytes, under the
// 20 MiB cap, and checks they are a PNG, JPEG, GIF or WebP image.
func (t *Tools) readImageFile(p string) ([]byte, error) {
	abs, err := t.absPath(SearchImages, p)
	if err != nil {
		return nil, err
	}
	c, err := t.files.Check(abs)
	switch {
	case errors.Is(err, index.ErrOutsideFolders):
		return nil, fmt.Errorf("search_images: %s is outside the folders Meru reads. It reads only inside: %s", p, t.rootList())
	case err != nil:
		return nil, fmt.Errorf("search_images: %s: %v", p, err)
	case !index.IsImageName(c.Path):
		return nil, fmt.Errorf("search_images: %s isn't a PNG, JPEG, GIF or WebP image", p)
	// To the file tools an image is media; any other reason is a real
	// refusal, such as a secret's name.
	case c.Reason != "" && c.Reason != index.ReasonMedia:
		return nil, fmt.Errorf("search_images: %s is off limits: %s", p, why(c.Reason))
	case c.Info.Size() > imageCap:
		return nil, fmt.Errorf("search_images: %s is %s, and Meru takes images up to %s", p, size(c.Info.Size()), size(imageCap))
	}
	f, err := os.Open(c.Path) // #nosec G304 -- a path Check kept: inside the folders, no link, no secret
	if err != nil {
		return nil, fmt.Errorf("search_images: can't read %s: %v", p, err)
	}
	// defer runs f.Close() when this function returns. Nothing is written,
	// so its error doesn't matter.
	defer f.Close()
	// The file could have changed into a link between Check and Open;
	// SameFile compares the file Open reached with the one Check saw.
	if opened, err := f.Stat(); err != nil || !os.SameFile(c.Info, opened) {
		return nil, fmt.Errorf("search_images: %s changed while Meru opened it", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, imageCap+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("search_images: can't read %s: %v", p, err)
	case len(b) > imageCap:
		return nil, fmt.Errorf("search_images: %s grew past %s while Meru read it", p, size(imageCap))
	case !index.IsImageData(b):
		return nil, fmt.Errorf("search_images: %s has an image's name, but its content isn't an image", p)
	}
	return b, nil
}

// searchImagesSpec returns search_images' spec.
func (t *Tools) searchImagesSpec() engine.ToolSpec {
	return engine.ToolSpec{
		Name: SearchImages,
		Description: "Finds pictures in the user's indexed folders: screenshots, photos and diagrams. " +
			"Pass query to describe what a picture shows, or image, the path of a picture, to find others like it. " +
			"You can't see the results; name them and cite them by number so the user can open them.",
		Parameters: mustSchema(map[string]any{
			"query": prop("string", "What the picture shows, in a few words."),
			"image": prop("string", "The path of a picture to find others like, such as one the user attached or names."),
			"limit": prop("integer", fmt.Sprintf("How many images to return, 1 to %d. Default %d.", maxSearchLimit, defaultImageLimit)),
		}),
	}
}
