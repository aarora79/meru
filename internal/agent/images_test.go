// This file tests a turn whose question carries images, against the fake
// Ollama: a model with vision gets the images on the question's message
// and the route "direct", with no router call; the next turn's history
// holds a note, not the bytes; a model without vision gets no call at
// all, and the user reads why; and a bad request fails before any turn.

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// imageTurn holds what the image tests share: an agent over the fake
// Ollama, the fake itself, the path of one uploaded PNG and its bytes.
type imageTurn struct {
	cfg    config.Config
	agent  *Agent
	srv    *fakeollama.Server
	router *fakeRouter
	path   string
	png    []byte
}

// newImageTurn builds an agent whose main model has vision when vision
// is true, and uploads one small PNG the way the desktop app's attach
// button does.
func newImageTurn(t *testing.T, vision bool) imageTurn {
	t.Helper()
	cfg := testConfig(t)
	base := t.TempDir()
	out := filepath.Join(base, "meru-output")
	ix, err := index.New(cfg.Index, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{}, config.Web{}, nil, out, ix, nil, nil)

	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "garden-bed.png")
	if err := os.WriteFile(src, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	up, err := tools.Upload(src)
	if err != nil {
		t.Fatal(err)
	}

	caps := []string{"completion", "tools"}
	if vision {
		caps = append(caps, engine.Vision)
	}
	srv := fakeollama.Start(t, fakeollama.Config{Capabilities: map[string][]string{cfg.Models.Main: caps}})
	eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, nil, nil, nil, quietLog())
	a.UseImages(tools.Image, func(ctx context.Context, model string) (bool, error) {
		c, err := eng.Capabilities(ctx, model)
		return slices.Contains(c, engine.Vision), err
	})
	return imageTurn{cfg: cfg, agent: a, srv: srv, router: router, path: up.Path, png: b.Bytes()}
}

// chatMessage is the part of a message in an /api/chat body the tests
// read.
type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images"`
}

// chatMessages decodes the messages of the fake's chat request r.
func chatMessages(t *testing.T, r fakeollama.Request) []chatMessage {
	t.Helper()
	var body struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Messages
}

func TestImageTurnWithVision(t *testing.T) {
	it := newImageTurn(t, true)
	it.srv.Enqueue(it.cfg.Models.Main, fakeollama.Reply{Text: "Basil, by the leaves."})

	evs, err := run(context.Background(), it.agent, rpc.Request{
		Text: "which plant is this?", Images: &rpc.Images{Paths: []string{it.path}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var session, route string
	for _, ev := range evs {
		switch ev.Type {
		case rpc.EventSession:
			session = ev.Session
		case rpc.EventRoute:
			route = ev.Route
		}
	}
	if route != "direct" {
		t.Errorf("route = %q, want direct", route)
	}
	if it.router.calls != 0 {
		t.Errorf("the router ran %d times; an image turn skips it", it.router.calls)
	}

	chats := it.srv.Requests("/api/chat")
	if len(chats) != 1 {
		t.Fatalf("got %d chat requests, want 1", len(chats))
	}
	msgs := chatMessages(t, chats[0])
	last := msgs[len(msgs)-1]
	want := base64.StdEncoding.EncodeToString(it.png)
	if last.Role != "user" || len(last.Images) != 1 || last.Images[0] != want {
		t.Errorf("question message = %s with %d images, want the user's question with the PNG", last.Role, len(last.Images))
	}
	for _, m := range msgs[:len(msgs)-1] {
		if len(m.Images) > 0 {
			t.Errorf("a %s message carries images", m.Role)
		}
	}

	// The user line names the image and keeps no bytes.
	lines := readLines(t, it.cfg, session)
	if len(lines) != 2 || !slices.Equal(lines[0].Images, []string{it.path}) {
		t.Fatalf("transcript = %+v, want a user line naming the image", lines)
	}
	raw, _ := json.Marshal(lines[0])
	if strings.Contains(string(raw), want[:16]) {
		t.Error("the transcript holds the image's bytes")
	}

	// The next turn's history says an image was shared, as text.
	it.srv.Enqueue(it.cfg.Models.Main, fakeollama.Reply{Text: "Pinch the tips each week."})
	if _, err := run(context.Background(), it.agent, rpc.Request{Session: session, Text: "how do I keep it from flowering?"}); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	chats = it.srv.Requests("/api/chat")
	var sawNote bool
	for _, m := range chatMessages(t, chats[len(chats)-1]) {
		if len(m.Images) > 0 {
			t.Errorf("the second turn sent images again, on a %s message", m.Role)
		}
		if m.Role == "user" && strings.Contains(m.Content, "[image: "+filepath.Base(it.path)+"]") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Error("the second turn's history has no [image: ...] note")
	}
}

func TestImageTurnWithoutVision(t *testing.T) {
	it := newImageTurn(t, false)
	evs, err := run(context.Background(), it.agent, rpc.Request{
		Text: "which plant is this?", Images: &rpc.Images{Paths: []string{it.path}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var answer strings.Builder
	var session string
	for _, ev := range evs {
		switch ev.Type {
		case rpc.EventSession:
			session = ev.Session
		case rpc.EventToken:
			answer.WriteString(ev.Text)
		}
	}
	if answer.String() != noVisionAnswer(it.cfg.Models.Main) {
		t.Errorf("answer = %q, want the no-vision message", answer.String())
	}
	if n := len(it.srv.Requests("/api/chat")); n != 0 {
		t.Errorf("got %d chat requests; a model without vision gets none", n)
	}
	lines := readLines(t, it.cfg, session)
	last := lines[len(lines)-1]
	if last.Type != transcript.TypeAssistant || last.Outcome != endNoVision {
		t.Errorf("assistant line = %+v, want outcome %s", last, endNoVision)
	}
}

func TestImageRequestRefused(t *testing.T) {
	it := newImageTurn(t, true)
	outside := filepath.Join(t.TempDir(), "garden-bed.png")
	if err := os.WriteFile(outside, it.png, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"outside uploads", []string{outside}, "isn't an image you attached"},
		{"too many", slices.Repeat([]string{it.path}, rpc.MaxImages+1), "images at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := run(context.Background(), it.agent, rpc.Request{Text: "which plant is this?", Images: &rpc.Images{Paths: tt.paths}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Handle = %v, want an error with %q", err, tt.want)
			}
		})
	}
	// Without UseImages, a question with images fails.
	a := New(it.cfg, nil, it.router, nil, nil, nil, nil, quietLog())
	if _, err := run(context.Background(), a, rpc.Request{Text: "which plant?", Images: &rpc.Images{Paths: []string{it.path}}}); err == nil {
		t.Error("an agent without UseImages took an image")
	}
	if n := len(it.srv.Requests("/api/chat")); n != 0 {
		t.Errorf("got %d chat requests for refused questions", n)
	}
}
