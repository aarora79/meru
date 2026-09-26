// This file makes the small previews of attached images that the
// composer's chips and the question bubbles show. The Bridge builds each
// one in Go as a data: URL, which the page's Content-Security-Policy
// allows for images, so the page never reads a file itself.

package desktop

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// Limits on a preview.
const (
	// thumbSide is the longest side of a preview, in pixels: twice the
	// 48 or so CSS pixels a chip shows, for a sharp preview on a Retina
	// screen.
	thumbSide = 160
	// rawThumbMax is the largest image sent to the page as it stands,
	// 64 KiB. A smaller one costs less to send than to shrink, and needs
	// no decoder, which lets a small WebP show too.
	rawThumbMax = 64 << 10
	// thumbReadMax is the largest image the Bridge reads for a preview,
	// the 20 MiB merud takes for an image.
	thumbReadMax = 20 << 20
	// thumbPixelsMax refuses to decode an image of more than 50 million
	// pixels. The header says the size before any decoding, and a small
	// file can claim a huge one, which would fill memory.
	thumbPixelsMax = 50_000_000
)

// thumbnail returns a data: URL holding a preview of the image at path,
// for the page to show, or "" when it can't make one. path must sit in
// the uploads folder under the output folder, where merud copies what
// the user attaches: the Bridge reads no other file for a preview.
//
// An image up to rawThumbMax goes out as it stands. A larger PNG, JPEG or
// GIF is decoded, shrunk to fit thumbSide on its longest side, laid on
// white (a JPEG has no transparency) and sent as a JPEG. A larger WebP
// gets no preview: the standard library has no WebP decoder, and a new
// module for a preview isn't worth it; the chip shows an icon instead.
func (b *Bridge) thumbnail(path string) string {
	if b.outputDir == "" || !inside(filepath.Join(b.outputDir, "uploads"), path) {
		return ""
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Size() > thumbReadMax {
		return ""
	}
	f, err := os.Open(path) // #nosec G304 -- a copy in Meru's uploads folder, checked above
	if err != nil {
		return ""
	}
	// defer runs f.Close() when thumbnail returns.
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, thumbReadMax))
	if err != nil {
		return ""
	}
	return thumbFor(data)
}

// thumbFor returns the preview data: URL for data, an image file's bytes,
// or "" when data isn't a PNG, JPEG, GIF or WebP image or can't be
// shrunk. See thumbnail for the rules.
func thumbFor(data []byte) string {
	kind := http.DetectContentType(data)
	var decode func(io.Reader) (image.Image, error)
	var config func(io.Reader) (image.Config, error)
	switch kind {
	case "image/png":
		decode, config = png.Decode, png.DecodeConfig
	case "image/jpeg":
		decode, config = jpeg.Decode, jpeg.DecodeConfig
	case "image/gif":
		decode, config = gif.Decode, gif.DecodeConfig
	case "image/webp":
		// No decoder; only a small one goes out as it stands.
	default:
		return ""
	}
	if len(data) <= rawThumbMax {
		return dataURL(kind, data)
	}
	if decode == nil {
		return ""
	}
	cfg, err := config(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbPixelsMax {
		return ""
	}
	img, err := decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, shrink(img, thumbSide), &jpeg.Options{Quality: 80}); err != nil {
		return ""
	}
	return dataURL("image/jpeg", out.Bytes())
}

// shrink returns img scaled to fit a side by side square, on white. It
// picks the nearest source pixel for each preview pixel, which is crude
// but quick, and a preview this small hides the difference. An image
// already small enough keeps its size.
func shrink(img image.Image, side int) *image.RGBA {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	if w > side || h > side {
		if w >= h {
			w, h = side, max(1, h*side/src.Dx())
		} else {
			w, h = max(1, w*side/src.Dy()), side
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	// draw.Draw with draw.Src fills out with white first.
	draw.Draw(out, out.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	scaled := image.NewNRGBA(out.Bounds())
	for y := range h {
		for x := range w {
			sx := src.Min.X + x*src.Dx()/w
			sy := src.Min.Y + y*src.Dy()/h
			scaled.Set(x, y, img.At(sx, sy))
		}
	}
	// draw.Over lays the scaled image on the white, so a transparent
	// part shows white, not black.
	draw.Draw(out, out.Bounds(), scaled, image.Point{}, draw.Over)
	return out
}

// dataURL writes data as a base64 data: URL of type kind.
func dataURL(kind string, data []byte) string {
	return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)
}
