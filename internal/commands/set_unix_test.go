//go:build unix

// This file tests a command call from end to end through a real
// Dispatcher: the program runs, the model reads its output, and the
// tool_calls row and the transcript hold the argv that ran.

package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestDispatchRecordsArgv(t *testing.T) {
	testHome(t)
	s, err := New([]config.Command{{
		Name:   "say",
		Argv:   []string{talk(t), "talk", "{text}", "4"},
		Params: map[string]config.CommandParam{"text": {Type: TypeString}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	d := dispatch.New([]dispatch.Backend{s}, rec, dispatch.Options{})
	var lines []transcript.Line
	res, out := d.Dispatch(context.Background(), dispatch.Call{
		ID: "call-1", Name: "cmd.say", Args: json.RawMessage(`{"text": "hi there"}`),
		Session: "s1", Source: rpc.SourceCLI,
		Append: func(l transcript.Line) error { lines = append(lines, l); return nil },
	})
	// A non-zero exit is a result: the call itself went fine.
	if out.Outcome != dispatch.OutcomeOK || res.IsError {
		t.Errorf("outcome = %q, result = %+v", out.Outcome, res)
	}
	if !strings.Contains(res.Text, "exit code: 4") || !strings.Contains(res.Text, "out: hi there") ||
		!strings.Contains(res.Text, "stderr:\nerr: hi there") {
		t.Errorf("result = %q", res.Text)
	}
	want, _ := json.Marshal(audit{Argv: []string{talk(t), "talk", "hi there", "4"}, Params: map[string]string{"text": "hi there"}})
	if len(rec.rows) != 1 || string(rec.rows[0].Args) != string(want) {
		t.Fatalf("rows = %+v, want args %s", rec.rows, want)
	}
	if len(lines) != 2 || string(lines[0].Args) != string(want) || !strings.Contains(lines[1].Result, "exit code: 4") {
		t.Errorf("transcript = %+v", lines)
	}
}
