// This file holds view_image, the built-in tool that shows the answer model
// a picture from the user's folders, so it can read a receipt, a screenshot
// or a chart that a search found. The picture travels in the result's
// Images, never in its text, so dispatch logs only the one line that names
// it. See ARCHITECTURE.md, "Built-in tools".

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // the blank imports let image.Decode read GIF and PNG files
	"image/jpeg"
	_ "image/png"
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
	b = shrinkImage(b)
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

// maxSide is the longest side, in pixels, of a picture view_image sends.
// The model's prompt cost grows with the pixels: qwen3.6:35b took 3,100
// prompt tokens for a 4,000 by 3,000 phone photo of a hotel bill shrunk to
// 2,048 pixels, 1,756 at 1,536 and 796 at 1,024, and read the bill's total
// right at each size. Full size, eight such photos overflowed the 32,768-
// token prompt. At 1,536 eight cost about 14,000, and small print still
// reads.
const maxSide = 1536

// shrinkImage returns b, a picture's bytes, made small enough for the
// model: a PNG, JPEG or GIF whose longest side is over maxSide comes back
// scaled down to it, as a JPEG at quality 90. It returns b as it was when
// the picture is small already, or when Go can't decode it, as with WebP,
// which the standard library can't read; Ollama then gets the original.
func shrinkImage(b []byte) []byte {
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return b
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if max(w, h) <= maxSide {
		return b
	}
	// The scale keeps the shape: the longest side becomes maxSide.
	scale := float64(maxSide) / float64(max(w, h))
	dw, dh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	var out bytes.Buffer
	if err := jpeg.Encode(&out, boxScale(src, dw, dh), &jpeg.Options{Quality: 90}); err != nil {
		return b
	}
	return out.Bytes()
}

// boxScale returns src scaled down to dw by dh pixels. Each new pixel is
// the average of the block of source pixels it covers, a box filter,
// which keeps thin strokes such as a receipt's digits readable where
// picking one source pixel per new pixel would drop them. It reads every
// source pixel once.
func boxScale(src image.Image, dw, dh int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	// sums holds the running total of each new pixel's red, green, blue
	// and count, so the loop over the source runs once.
	sums := make([]uint64, dw*dh*4)
	for y := 0; y < sh; y++ {
		dy := y * dh / sh
		for x := 0; x < sw; x++ {
			dx := x * dw / sw
			// RGBA returns 16-bit values; >> 8 brings each to 0..255.
			r, g, bl, _ := src.At(sb.Min.X+x, sb.Min.Y+y).RGBA()
			i := (dy*dw + dx) * 4
			sums[i] += uint64(r >> 8)
			sums[i+1] += uint64(g >> 8)
			sums[i+2] += uint64(bl >> 8)
			sums[i+3]++
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for i := 0; i < dw*dh; i++ {
		n := sums[i*4+3]
		if n == 0 {
			continue
		}
		dst.Pix[i*4] = average(sums[i*4], n)
		dst.Pix[i*4+1] = average(sums[i*4+1], n)
		dst.Pix[i*4+2] = average(sums[i*4+2], n)
		dst.Pix[i*4+3] = 255
	}
	return dst
}

// average returns sum / n as one 0..255 color value. Each of the n values
// summed is 0..255, so the average is too, and the conversion keeps it.
func average(sum, n uint64) uint8 {
	return uint8(sum / n) // #nosec G115 -- an average of n values in 0..255 is in 0..255
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
