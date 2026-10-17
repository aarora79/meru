// This file tests view_image in a turn: the real built-in tools answer
// through dispatch, a model with vision gets the picture on its tool
// message, a model without vision isn't offered the tool and its answer
// gets the text-only notice, and a turn opens at most maxViewed pictures.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// viewTurn builds an agent over a folder holding n small PNGs, ticket-0.png
// and up, with the real built-in tools behind dispatch and eng as the
// model. vision says whether the main model can look at pictures. It
// returns the agent and the pictures' paths and bytes.
func viewTurn(t *testing.T, eng *fakeEngine, vision bool, n int) (*Agent, []string, [][]byte) {
	t.Helper()
	cfg := testConfig(t)
	folder, _ := filepath.EvalSymlinks(t.TempDir())
	var paths []string
	var pics [][]byte
	for i := range n {
		var b bytes.Buffer
		// Each picture gets its own width, so no two share their bytes.
		if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, i+1, 1))); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(folder, fmt.Sprintf("ticket-%d.png", i))
		if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		paths, pics = append(paths, p), append(pics, b.Bytes())
	}
	cfg.Index.Folders = []string{folder}
	ix, err := index.New(cfg.Index, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil)
	disp := dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{})
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}, nil, disp, nil, nil, quietLog())
	a.UseImages(tools.Image, func(context.Context, string) (bool, error) { return vision, nil })
	a.UseVisionSets(func(context.Context) []VisionSet {
		return []VisionSet{{Set: "qwen-4bit", Model: "qwen3.6:35b"}}
	})
	return a, paths, pics
}

// viewCall returns a view_image call on path.
func viewCall(path string) engine.ToolCall {
	args, _ := json.Marshal(map[string]string{"path": path})
	return engine.ToolCall{Name: builtin.ViewImage, Arguments: args}
}

// offeredIn returns the names of the tools the model got in round i.
func offeredIn(eng *fakeEngine, i int) []string {
	var names []string
	for _, s := range eng.calls[i].tools {
		names = append(names, s.Name)
	}
	return names
}

// noticeOf returns the text of the turn's notice event, or "".
func noticeOf(evs []rpc.Event) string {
	for _, ev := range evs {
		if ev.Type == rpc.EventNotice {
			return ev.Text
		}
	}
	return ""
}

func TestViewImageOnToolMessage(t *testing.T) {
	eng := &fakeEngine{}
	a, paths, pics := viewTurn(t, eng, true, 1)
	eng.rounds = []fakeRound{
		{calls: []engine.ToolCall{viewCall(paths[0])}},
		{pieces: []string{"The ticket cost $118.00 [1]."}},
	}
	evs, err := run(context.Background(), a, rpc.Request{Text: "what did my train ticket cost?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !slices.Contains(offeredIn(eng, 0), builtin.ViewImage) {
		t.Fatalf("a model with vision got %v, without view_image", offeredIn(eng, 0))
	}
	// The second round reads the picture on view_image's tool message.
	msgs := eng.lastCall().msgs
	last := msgs[len(msgs)-1]
	if last.Role != engine.RoleTool || last.ToolName != builtin.ViewImage ||
		len(last.Images) != 1 || !bytes.Equal(last.Images[0], pics[0]) {
		t.Errorf("last message = role %s, tool %s, %d pictures; want view_image's result with the picture",
			last.Role, last.ToolName, len(last.Images))
	}
	if n := noticeOf(evs); n != "" {
		t.Errorf("a model with vision got the notice %q", n)
	}
}

func TestViewImageHiddenWithoutVision(t *testing.T) {
	tests := []struct {
		name       string
		vision     bool
		answer     string
		wantNotice bool
	}{
		{"a blind model that names a picture", false, "Your ticket is ticket-0.png in the trips folder.", true},
		{"a blind model that names none", false, "You have two train tickets.", false},
		{"a model with vision that names a picture", true, "Your ticket is ticket-0.png.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{}
			a, _, _ := viewTurn(t, eng, tt.vision, 1)
			eng.rounds = []fakeRound{{pieces: []string{tt.answer}}}
			evs, err := run(context.Background(), a, rpc.Request{Text: "find my train ticket"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := slices.Contains(offeredIn(eng, 0), builtin.ViewImage); got != tt.vision {
				t.Errorf("view_image offered %v; want %v", got, tt.vision)
			}
			n := noticeOf(evs)
			if tt.wantNotice != (n != "") {
				t.Fatalf("notice = %q; want one: %v", n, tt.wantNotice)
			}
			if tt.wantNotice && !strings.Contains(n, "reads text only") ||
				tt.wantNotice && !strings.Contains(n, "qwen3.6:35b can: switch to it with `/model qwen-4bit`") {
				t.Errorf("notice = %q; want the text-only line and the suggestion", n)
			}
		})
	}
}

func TestViewImageCap(t *testing.T) {
	eng := &fakeEngine{}
	a, paths, _ := viewTurn(t, eng, true, maxViewed+1)
	var calls []engine.ToolCall
	for _, p := range paths {
		calls = append(calls, viewCall(p))
	}
	eng.rounds = []fakeRound{{calls: calls}, {pieces: []string{"Nine tickets."}}}
	if _, err := run(context.Background(), a, rpc.Request{Text: "add up my tickets"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	msgs := eng.lastCall().msgs
	var shown, refused int
	for _, m := range msgs {
		if m.Role != engine.RoleTool {
			continue
		}
		switch {
		case len(m.Images) == 1:
			shown++
		case strings.Contains(m.Content, fmt.Sprintf("looked at %d pictures", maxViewed)):
			refused++
		}
	}
	if shown != maxViewed || refused != 1 {
		t.Errorf("shown %d, refused %d; want %d shown and the last call refused", shown, refused, maxViewed)
	}
}

func TestVisionSuggestion(t *testing.T) {
	if s := visionSuggestion(nil); !strings.Contains(s, "/model in meru chat") || !strings.Contains(s, "Settings, Models") {
		t.Errorf("no sets: %q; want the way to pick one", s)
	}
	sets := []VisionSet{{Set: "qwen-4bit", Model: "qwen3.6:35b"}, {Set: "gemma-moe", Model: "gemma4:26b-mxfp8"}}
	if s := visionSuggestion(sets); !strings.HasPrefix(s, "qwen3.6:35b can: switch to it with `/model qwen-4bit`") {
		t.Errorf("two sets: %q; want the first", s)
	}
	for _, msg := range []string{noVisionAnswer("tiny", sets), noVisionText("tiny", sets),
		similarAnswer("tiny", fileHits{sources: []rpc.Citation{{N: 1, Path: "~/a.png"}}}, sets)} {
		if !strings.HasSuffix(msg, visionSuggestion(sets)) {
			t.Errorf("%q doesn't end with the suggestion", msg)
		}
	}
}

func TestNamesImages(t *testing.T) {
	tests := []struct {
		text  string
		cited []rpc.Citation
		want  bool
	}{
		{"See ~/trips/amtrak.png.", nil, true},
		{"The scan IMG_2041.JPG shows it.", nil, true},
		{"Two tickets, $118 in all.", nil, false},
		{"See [1].", []rpc.Citation{{N: 1, Kind: "image"}}, true},
		{"See [1].", []rpc.Citation{{N: 1}}, false},
		{"The pngs folder.", nil, false},
	}
	for _, tt := range tests {
		if got := namesImages(tt.text, tt.cited); got != tt.want {
			t.Errorf("namesImages(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
