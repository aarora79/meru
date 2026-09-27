// This file tests `meru run --json` against an in-process rpc server: the
// events as JSON lines, the approval that is written and denied, a failed
// turn, bad usage, and a question that only starts with "run".

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// jsonLines decodes each line of out as one rpc.Event.
func jsonLines(t *testing.T, out string) []rpc.Event {
	t.Helper()
	var evs []rpc.Event
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var ev rpc.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %q isn't one JSON event: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// types lists the type of each event, in order.
func types(evs []rpc.Event) []string {
	var ts []string
	for _, ev := range evs {
		ts = append(ts, string(ev.Type))
	}
	return ts
}

func TestRunJSON(t *testing.T) {
	// asks plays a turn with one tool call that asks first. It records the
	// choice the client sent back, and ends with a done event carrying the
	// turn's stats.
	var choice rpc.Choice
	asks := func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
		if req.Op != rpc.OpAsk || req.Text != "digest my notes" || req.Source != rpc.SourceCLI {
			t.Errorf("request = %+v, want an ask of %q from cli", req, "digest my notes")
		}
		emit(rpc.Event{Type: rpc.EventSession, Session: "s1"})
		emit(rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: "c1", Name: "write_file"}})
		c, err := approve(ctx, rpc.Approval{ID: "a1", Name: "write_file", Kind: "builtin",
			Args: json.RawMessage(`{"path":"a<b>.md"}`), Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}})
		if err != nil {
			return err
		}
		choice = c
		emit(rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: "c1", Name: "write_file", Outcome: "declined"}})
		emit(rpc.Event{Type: rpc.EventToken, Text: "Done <3"})
		return emit(rpc.Event{Type: rpc.EventDone, TTFTMillis: 900, TTLTMillis: 2400, DurationMillis: 2450,
			TokensIn: 2000, TokensOut: 8, EvalMillis: 1482, TPOTMillis: 185.25})
	}

	sock := startServer(t, asks)
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"-socket", sock, "run", "--json", "digest", "my", "notes"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if choice != rpc.ChoiceDeny {
		t.Errorf("choice sent back = %q, want deny", choice)
	}
	evs := jsonLines(t, out.String())
	want := []string{"session", "tool_call", "approval", "tool_result", "token", "done"}
	if got := types(evs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	// The arguments cross the socket as JSON, which may escape "<", so
	// compare what they decode to.
	var args struct{ Path string }
	if a := evs[2].Approval; a == nil || a.Name != "write_file" || json.Unmarshal(a.Args, &args) != nil || args.Path != "a<b>.md" {
		t.Errorf("approval line = %+v, want write_file with its arguments", evs[2].Approval)
	}
	done := evs[5]
	if done.TTFTMillis != 900 || done.TTLTMillis != 2400 || done.TokensIn != 2000 || done.TokensOut != 8 || done.TPOTMillis != 185.25 {
		t.Errorf("done line = %+v, want the turn's stats", done)
	}
	// The encoder leaves "<" as it is, so a person can read the line.
	if !strings.Contains(out.String(), `"text":"Done <3"`) {
		t.Errorf("stdout = %q, want the token text unescaped", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
}

func TestRunJSONFails(t *testing.T) {
	failing := func(context.Context, rpc.Request, func(rpc.Event) error, rpc.ApproveFunc) error {
		return errors.New("model not found")
	}
	tests := []struct {
		name       string
		handler    rpc.Handler
		args       []string // after -socket
		wantCode   int
		wantTypes  string // the event types on stdout, joined by commas
		wantErrOut string
	}{
		{"error event", failing, []string{"run", "--json", "hi"}, exitError, "error", "meru: model not found"},
		{"no question", answer(t, ""), []string{"run", "--json"}, exitError, "", `usage: meru run --json "question"`},
		{"unknown flag", answer(t, ""), []string{"run", "--json", "--yaml", "hi"}, exitError, "", "run: flag provided but not defined"},
		// Without --json, "run" is the first word of a question.
		{"a question about running", answer(t, "run the tests", "ok\n"), []string{"run", "the", "tests"}, exitOK, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := startServer(t, tt.handler)
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if tt.wantTypes != "" {
				if got := strings.Join(types(jsonLines(t, out.String())), ","); got != tt.wantTypes {
					t.Errorf("event types = %s, want %s", got, tt.wantTypes)
				}
			}
			if !strings.Contains(errOut.String(), tt.wantErrOut) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErrOut)
			}
		})
	}
}
