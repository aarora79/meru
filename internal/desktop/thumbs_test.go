// This file tests image attachments in the Bridge: the previews it makes
// from the copies in the uploads folder, the image chips, and a question
// that carries images in the request's Images, not in "Read this file"
// lines. It also checks Try again, which sends the images once more, and
// that a reopened session shows its images again.

package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// pngOf encodes a w by h image as a PNG. noisy fills it with changing
// colors, which PNG can't pack small, to push the file past rawThumbMax.
func pngOf(t *testing.T, w, h int, noisy bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{G: 160, A: 255}
			if noisy {
				c = color.RGBA{R: uint8(x * 7 % 256), G: uint8(y * 13 % 256), B: uint8((x * y) % 256), A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// writeBytes writes b to p, failing the test on an error.
func writeBytes(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThumbnail(t *testing.T) {
	home := t.TempDir()
	out := filepath.Join(home, "meru-output")
	uploads := filepath.Join(out, "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	small := pngOf(t, 4, 3, false)
	big := pngOf(t, 800, 400, true)
	if len(big) <= rawThumbMax {
		t.Fatalf("the big PNG is %d bytes; it must pass %d", len(big), rawThumbMax)
	}
	writeBytes(t, filepath.Join(uploads, "garden.png"), small)
	writeBytes(t, filepath.Join(uploads, "bed.png"), big)
	writeBytes(t, filepath.Join(uploads, "notes.png"), []byte("not an image"))
	writeBytes(t, filepath.Join(out, "outside.png"), small)
	if err := os.Symlink(filepath.Join(out, "outside.png"), filepath.Join(uploads, "link.png")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	b := New(Options{Socket: "/nonexistent", Home: home, OutputDir: out})

	if got, want := b.thumbnail(filepath.Join(uploads, "garden.png")), dataURL("image/png", small); got != want {
		t.Errorf("small PNG preview = %.40q, want the file as it stands", got)
	}

	// A large PNG comes back as a JPEG, 160 pixels on its long side.
	got := b.thumbnail(filepath.Join(uploads, "bed.png"))
	raw, ok := strings.CutPrefix(got, "data:image/jpeg;base64,")
	if !ok {
		t.Fatalf("big PNG preview = %.40q, want a JPEG data: URL", got)
	}
	jb, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(jb))
	if err != nil || cfg.Width != thumbSide || cfg.Height != thumbSide/2 {
		t.Errorf("preview is %dx%d, %v; want %dx%d", cfg.Width, cfg.Height, err, thumbSide, thumbSide/2)
	}

	for _, name := range []string{
		filepath.Join(uploads, "notes.png"),   // not an image
		filepath.Join(uploads, "link.png"),    // a symbolic link
		filepath.Join(out, "outside.png"),     // outside uploads
		filepath.Join(uploads, "missing.png"), // gone
	} {
		if got := b.thumbnail(name); got != "" {
			t.Errorf("thumbnail(%s) = %.40q, want none", name, got)
		}
	}
	if got := New(Options{Socket: "/nonexistent"}).thumbnail(filepath.Join(uploads, "garden.png")); got != "" {
		t.Error("a Bridge with no output folder made a preview")
	}
}

func TestShrinkKeepsShape(t *testing.T) {
	tests := []struct{ w, h, wantW, wantH int }{
		{800, 400, 160, 80},
		{300, 900, 53, 160},
		{100, 50, 100, 50}, // small enough already
	}
	for _, tt := range tests {
		got := shrink(image.NewGray(image.Rect(0, 0, tt.w, tt.h)), thumbSide).Bounds()
		if got.Dx() != tt.wantW || got.Dy() != tt.wantH {
			t.Errorf("shrink %dx%d = %dx%d, want %dx%d", tt.w, tt.h, got.Dx(), got.Dy(), tt.wantW, tt.wantH)
		}
	}
}

// imageMerud plays merud for the image tests: attach_file copies the file
// into uploads and calls a .png an image, ask records the request and
// answers, and session_turns sends back one turn with an image.
type imageMerud struct {
	uploads string
	mu      sync.Mutex // guards asked
	asked   []rpc.Request
}

// handler is the rpc.Handler for m.
func (m *imageMerud) handler(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
	switch req.Op {
	case rpc.OpAttachFile:
		raw, err := os.ReadFile(req.Path)
		if err != nil {
			return err
		}
		saved := filepath.Join(m.uploads, filepath.Base(req.Path))
		if err := os.WriteFile(saved, raw, 0o600); err != nil {
			return err
		}
		kind := rpc.AttachFile
		if strings.HasSuffix(saved, ".png") {
			kind = rpc.AttachImage
		}
		return emit(rpc.Event{Type: rpc.EventSaved, Text: saved, Kind: kind})
	case rpc.OpAsk:
		m.mu.Lock()
		m.asked = append(m.asked, req)
		m.mu.Unlock()
		return emit(rpc.Event{Type: rpc.EventToken, Text: "Basil."})
	case rpc.OpSessionTurns:
		return emit(rpc.Event{Type: rpc.EventTurns, Turns: []rpc.TurnInfo{{
			Question: "which plant is this?", Images: []string{filepath.Join(m.uploads, "garden-bed.png")}, Answer: "Basil.",
		}}})
	}
	return errors.New("unexpected op " + string(req.Op))
}

func TestAttachImages(t *testing.T) {
	home := t.TempDir()
	away := filepath.Join(home, "Downloads")
	out := filepath.Join(home, "meru-output")
	uploads := filepath.Join(out, "uploads")
	for _, d := range []string{away, uploads} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	img := pngOf(t, 4, 3, false)
	writeBytes(t, filepath.Join(away, "garden-bed.png"), img)
	writeBytes(t, filepath.Join(away, "seeds.md"), []byte("Beans in June.\n"))

	m := &imageMerud{uploads: uploads}
	r := newRecorder()
	picked := []string{filepath.Join(away, "garden-bed.png"), filepath.Join(away, "seeds.md")}
	b := New(Options{Socket: startServer(t, m.handler), Home: home, OutputDir: out, Emit: r.emit,
		PickFiles: func() ([]string, error) { return picked, nil }})
	ctx := context.Background()

	if err := b.AttachFile(ctx); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "attachments", isAttachments(r, 1))
	chips := lastAttachments(r).Attachments
	if len(chips) != 2 || chips[0].Kind != rpc.AttachImage || chips[1].Kind != rpc.AttachFile {
		t.Fatalf("chips = %+v, want an image and a file", chips)
	}
	if chips[0].Thumb != dataURL("image/png", img) || chips[1].Thumb != "" {
		t.Errorf("previews = %.40q and %.40q, want the PNG and none", chips[0].Thumb, chips[1].Thumb)
	}

	// The file gets its line; the image goes in Images, by full path.
	if err := b.Send("", "Which plant is this?", "", false); err != nil {
		t.Fatal(err)
	}
	start := r.waitFor(t, "start", func(u Update) bool { return u.Kind == KindStart })
	if want := "Which plant is this?\n\nRead this file: ~/meru-output/uploads/seeds.md"; start.Question != want {
		t.Errorf("question = %q, want %q", start.Question, want)
	}
	if len(start.Images) != 1 || start.Images[0].Thumb == "" {
		t.Errorf("start images = %+v, want the image with its preview", start.Images)
	}
	r.waitFor(t, "end", isEnd(1))
	m.mu.Lock()
	asked := m.asked
	m.mu.Unlock()
	want := filepath.Join(uploads, "garden-bed.png")
	if len(asked) != 1 || asked[0].Images == nil || len(asked[0].Images.Paths) != 1 || asked[0].Images.Paths[0] != want {
		t.Fatalf("asked = %+v, want one question carrying %s", asked, want)
	}

	// Try again sends the same image, by the path the page got.
	if err := b.Retry("", "Which plant is this?", "", []string{"~/meru-output/uploads/garden-bed.png"}, false); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "end", isEnd(2))
	m.mu.Lock()
	asked = m.asked
	m.mu.Unlock()
	if len(asked) != 2 || asked[1].Images == nil || len(asked[1].Images.Paths) != 1 || asked[1].Images.Paths[0] != want {
		t.Errorf("retry asked = %+v, want the question carrying %s again", asked, want)
	}
	if err := b.Retry("", "Which plant is this?", "", []string{"~/Downloads/garden-bed.png"}, false); err == nil {
		t.Error("Retry took an image outside the uploads folder")
	}

	// A reopened session shows the image again, with its preview.
	turns, err := b.SessionTurns(ctx, "2026-09-26T101502-7f3a")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || len(turns[0].Images) != 1 || turns[0].Images[0].Name != "garden-bed.png" || turns[0].Images[0].Thumb == "" {
		t.Errorf("turns = %+v, want the image with its preview", turns)
	}
}
