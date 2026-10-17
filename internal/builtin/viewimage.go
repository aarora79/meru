// This file holds view_image, the built-in tool that shows the answer model
// a picture from the user's folders, so it can read a receipt, a screenshot
// or a chart that a search found. The picture travels in the result's
// Images, never in its text, so dispatch logs only the one line that names
// it. See ARCHITECTURE.md, "Built-in tools".

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// ViewImage is the image viewer's name, as the model sees it.
const ViewImage = "view_image"

// viewImageArgs is the JSON object the model sends to view_image. The
// struct tag names the JSON key the field reads.
type viewImageArgs struct {
	Path string `json:"path"`
}

// viewCappedKey is the key under which the agent marks a call past the
// turn's limit on pictures. An unexported struct type as the key means no
// other package can set or read the mark by accident.
type viewCappedKey struct{}

// WithViewCapped returns a copy of ctx that tells view_image this turn has
// shown the model as many pictures as it may: limit, which the refusal
// names. The agent counts a turn's pictures, since a tool call can't see
// its turn; dispatch passes ctx on to the tool untouched.
func WithViewCapped(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, viewCappedKey{}, limit)
}

// viewCapped returns the limit WithViewCapped set on ctx, and whether it
// set one. The ", ok" form of a type assertion gives false, not a panic,
// when the value is missing.
func viewCapped(ctx context.Context) (int, bool) {
	limit, ok := ctx.Value(viewCappedKey{}).(int)
	return limit, ok
}

// viewImage reads the picture at the path the model gives, under the same
// rules as search_images, and returns it for the model to look at on its
// next round: a line of text that names the file, the bytes in Images, and
// the file as an image source, so the clients list it under the answer. It
// fails, with text the model reads, on a missing path, a path the rules
// refuse, and a call past the turn's limit.
func (t *Tools) viewImage(ctx context.Context, raw json.RawMessage) (dispatch.Result, error) {
	if limit, capped := viewCapped(ctx); capped {
		return dispatch.Result{}, fmt.Errorf("view_image: this turn has looked at %d pictures, the most one turn takes. "+
			"Answer from those, and tell the user which pictures you didn't open", limit)
	}
	var a viewImageArgs
	if err := decode(ViewImage, raw, &a); err != nil {
		return dispatch.Result{}, err
	}
	p := strings.TrimSpace(a.Path)
	if p == "" {
		return dispatch.Result{}, errors.New("view_image: pass path, the path of a PNG, JPEG, GIF or WebP picture in the indexed folders")
	}
	b, err := t.readImageFile(ViewImage, p)
	if err != nil {
		return dispatch.Result{}, err
	}
	shown := t.show(p)
	// The picture takes the next citation number, so the answer can cite
	// it as [n] like a search result.
	n := dispatch.CiteNumbers(ctx, 1)
	return dispatch.Result{
		Text: fmt.Sprintf("[%d] %s (%s) is attached to this result. "+
			"Look at it and answer from what it shows; cite it as [%d].", n, shown, size(int64(len(b))), n),
		Images:  [][]byte{b},
		Sources: []rpc.Citation{{N: n, Path: shown, Kind: "image"}},
	}, nil
}

// viewImageSpec returns view_image's spec.
func viewImageSpec() engine.ToolSpec {
	return engine.ToolSpec{
		Name: ViewImage,
		Description: "Shows you a picture from the user's indexed folders, such as a receipt, a ticket, " +
			"a screenshot or a chart, so you can read and describe it. Pass the path a search, a folder " +
			"listing or the user gave. Call it once per picture.",
		Parameters: mustSchema(map[string]any{
			"path": prop("string", "The path of a PNG, JPEG, GIF or WebP picture."),
		}, "path"),
	}
}
