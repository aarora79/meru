//go:build e2e && unix

// This file tests a local command end to end: merud reads a [[commands]]
// entry, the fake model calls cmd.say, merud runs echo with no shell, and
// the transcript, `meru log` and `meru tools` show the argv that ran. It
// needs echo on PATH, so the build line keeps it to Unix.

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

func TestLocalCommand(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	// under is the Meru home itself: a folder the test owns.
	h.writeConfig(t, fakeConfig(f.url, fmt.Sprintf(`[router]
temperature = 1.0

[[commands]]
name        = "say"
description = "Says the text back"
argv        = ["echo", "{text}", "--in={dir}"]

  [commands.params.text]
  type = "string"

  [commands.params.dir]
  type  = "path"
  under = %q
`, h.dir)))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	const answer = "It said hello."
	f.enqueue(t, fastModel, toolsRoute())
	f.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "cmd.say",
			Arguments: map[string]any{"text": "hello; rm -rf ~", "dir": "sessions"}}}},
		fakeollama.Reply{Text: answer})

	res := runMeru(t, h, "say hello")
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s\nmerud.log:\n%s", res.code, res.stderr, h.log())
	}
	if res.stdout != answer+"\n" {
		t.Errorf("stdout = %q, want only the answer", res.stdout)
	}

	// The sessions folder, links resolved, as the argv holds it.
	sessions, err := filepath.EvalSymlinks(filepath.Join(h.dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	lines := readTranscript(t, sessionFiles(t, h)[0])
	var call, result transcript.Line
	for _, l := range lines {
		switch l.Type {
		case transcript.TypeToolCall:
			call = l
		case transcript.TypeToolResult:
			result = l
		}
	}
	wantArgv := fmt.Sprintf(`"argv":["echo","hello; rm -rf ~","--in=%s"]`, sessions)
	if call.Kind != "command" || call.Server != "meru" || call.Tool != "cmd.say" || !strings.Contains(string(call.Args), wantArgv) {
		t.Errorf("tool_call line = %+v, want args holding %s", call, wantArgv)
	}
	// echo printed the text as one argument: no shell ran the "rm".
	if result.Outcome != "ok" || !strings.Contains(result.Result, "hello; rm -rf ~ --in="+sessions) ||
		!strings.Contains(result.Result, "exit code: 0") {
		t.Errorf("tool_result line = %+v", result)
	}

	log := runMeru(t, h, "log")
	const want = "command  meru.cmd.say  ok  -"
	if log.code != 0 || !strings.Contains(log.stdout, want) ||
		!strings.Contains(log.stdout, `echo "hello; rm -rf ~" --in=`+sessions) {
		t.Errorf("meru log exited %d:\n%s%s", log.code, log.stdout, log.stderr)
	}
	tools := runMeru(t, h, "tools")
	if tools.code != 0 || !strings.Contains(tools.stdout, "cmd.say") ||
		!strings.Contains(tools.stdout, "runs: echo {text} --in={dir}") {
		t.Errorf("meru tools exited %d:\n%s%s", tools.code, tools.stdout, tools.stderr)
	}
}
