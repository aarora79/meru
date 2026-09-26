// This file tests images over the socket, against a whole merud: attach_file
// copies an image with no [index] folders and says it is one, and an ask
// that names an image outside the uploads folder, or a link inside it, fails
// before any turn starts. It also checks visionCheck against the fake
// Ollama's /api/show.

package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

func TestAttachImageAndAsk(t *testing.T) {
	dir, home := meruHome(t)
	away := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(away, 0o700); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(away, "garden-bed.png")
	if err := os.WriteFile(src, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	// No [index] folders: an image needs no read_file.
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})
	uploads := filepath.Join(home, "meru-output", "uploads")

	evs, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpAttachFile, Path: src}, rpc.ChoiceDeny)
	if msg != "" {
		t.Fatalf("attach: %s", msg)
	}
	saved := eventOf(t, evs, rpc.EventSaved)
	if saved.Text != filepath.Join(uploads, "garden-bed.png") || saved.Kind != rpc.AttachImage {
		t.Fatalf("saved = %q, kind %q; want the copy in uploads, kind image", saved.Text, saved.Kind)
	}
	if err := os.Symlink(src, filepath.Join(uploads, "link.png")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	tests := []struct {
		name    string
		paths   []string
		refusal string
	}{
		{"outside uploads", []string{src}, "isn't an image you attached"},
		{"a link inside uploads", []string{filepath.Join(uploads, "link.png")}, "symbolic link"},
		{"six images", []string{saved.Text, saved.Text, saved.Text, saved.Text, saved.Text, saved.Text}, "5 images at most"},
		// The fake engine can't say what a model sees, so even a good
		// image is refused rather than sent blind.
		{"a good image, but no vision check", []string{saved.Text}, "can't tell whether its model can look at images"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := rpc.Request{Op: rpc.OpAsk, Text: "which plant is this?", Images: &rpc.Images{Paths: tt.paths}}
			evs, msg := callErr(t, d.sock, req, rpc.ChoiceDeny)
			if !strings.Contains(msg, tt.refusal) {
				t.Errorf("ask: error %q, want %q", msg, tt.refusal)
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventSession {
					t.Error("a refused question started a session")
				}
			}
		})
	}
}

func TestVisionCheck(t *testing.T) {
	if visionCheck(&fakeEngine{}) != nil {
		t.Error("visionCheck gave a check for an engine that can't answer one")
	}
	srv := fakeollama.Start(t, fakeollama.Config{Capabilities: map[string][]string{
		"seer": {"completion", engine.Vision}, "reader": {"completion", "tools"},
	}})
	eng, err := engine.NewOllama(srv.URL, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := visionCheck(eng)
	for model, want := range map[string]bool{"seer": true, "reader": false} {
		got, err := check(context.Background(), model)
		if err != nil || got != want {
			t.Errorf("vision(%s) = %v, %v; want %v", model, got, err, want)
		}
	}
}
