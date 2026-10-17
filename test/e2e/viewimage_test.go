//go:build e2e

// This file checks view_image end to end: with a main model that can look
// at pictures, merud sends the picture on the tool message of the model's
// next round and keeps it out of the transcript; with one that can't, the
// tool isn't offered and the answer ends with a notice that names a model
// set whose model can.

package e2e

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// viewConfig returns a config that indexes dir, with one model set,
// "eyes", whose answer model is eyeModel.
func viewConfig(url, dir, eyeModel string) string {
	return fmt.Sprintf("[ollama]\nbase_url = %q\n\n[models]\nfast = %q\nmain = %q\nembed = \"nomic-embed-text\"\n\n"+
		"[[models.sets]]\nname = \"eyes\"\nmain = %q\n\n[web]\nsearxng_url = \"\"\n\n[index]\nfolders = [%q]\n",
		url, fastModel, mainModel, eyeModel, dir)
}

// ticketDir makes a folder holding one small PNG, ticket.png, and returns
// the folder and the picture's bytes.
func ticketDir(t *testing.T) (string, []byte) {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru-view-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	p := filepath.Join(dir, "ticket.png")
	pngFile(t, p, 90)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return dir, b
}

func TestViewImageE2E(t *testing.T) {
	t.Parallel()
	dir, pic := ticketDir(t)
	f := startFake(t, "-vision", mainModel)
	h := newHome(t)
	h.writeConfig(t, viewConfig(f.url, dir, mainModel))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	const answer = "The ticket cost $118.00."
	f.enqueue(t, fastModel, toolsRoute())
	f.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "view_image",
			Arguments: map[string]any{"path": filepath.Join(dir, "ticket.png")}}}},
		fakeollama.Reply{Text: answer})
	res := runMeru(t, h, "what did my train ticket cost?")
	if res.code != 0 {
		t.Fatalf("meru exited %d:\n%s\nmerud.log:\n%s", res.code, res.stderr, h.log())
	}
	if !strings.Contains(res.stdout, answer) {
		t.Errorf("stdout = %q", res.stdout)
	}

	// The model's last round carries the picture, as base64 in the tool
	// message's images. (The first request is merud's warm-up.)
	encoded := base64.StdEncoding.EncodeToString(pic)
	chats := f.chatRequests(t, mainModel)
	if len(chats) == 0 || !strings.Contains(string(chats[len(chats)-1].Body), `"images":["`+encoded+`"]`) {
		t.Fatalf("got %d chat requests; want the last with the picture on view_image's message", len(chats))
	}
	// The transcript names the picture and never holds it.
	var result transcript.Line
	for _, l := range readTranscript(t, sessionFiles(t, h)[0]) {
		if l.Type == transcript.TypeToolResult {
			result = l
		}
	}
	if result.Outcome != "ok" || !strings.Contains(result.Result, "ticket.png") || strings.Contains(result.Result, encoded) {
		t.Errorf("tool_result line = %+v; want the picture named and not included", result)
	}
}

func TestViewImageNoVisionE2E(t *testing.T) {
	t.Parallel()
	dir, _ := ticketDir(t)
	// Only the "eyes" set's model can look at pictures; the main model
	// can't.
	const eyeModel = "fake-eyes"
	f := startFake(t, "-vision", eyeModel)
	h := newHome(t)
	h.writeConfig(t, viewConfig(f.url, dir, eyeModel))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	f.enqueue(t, fastModel, toolsRoute())
	f.enqueue(t, mainModel, fakeollama.Reply{Text: "Your ticket is ticket.png in the trips folder."})
	res := runMeru(t, h, "find my train ticket")
	if res.code != 0 {
		t.Fatalf("meru exited %d:\n%s\nmerud.log:\n%s", res.code, res.stderr, h.log())
	}
	// The turn's request is the last; the first is merud's warm-up.
	chats := f.chatRequests(t, mainModel)
	if len(chats) == 0 || strings.Contains(string(chats[len(chats)-1].Body), `"name":"view_image"`) {
		t.Errorf("the main model, which can't see, was offered view_image")
	}
	out := res.stdout + res.stderr
	if !strings.Contains(out, "reads text only") || !strings.Contains(out, eyeModel+" can: switch to it with `/model eyes`") {
		t.Errorf("output lacks the notice and its suggestion:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
}
