// This file tests Set as dispatch sees it: the tool specs, the confirm
// rule, the argv AuditArgs records, the `meru tools` listing, and a
// confirming command under a scheduled job, through a real Dispatcher.

package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// testSet builds a Set of two commands over a test home: git-log reads and
// runs freely, git-push asks first.
func testSet(t *testing.T) (*Set, string) {
	t.Helper()
	home := testHome(t)
	s, err := New([]config.Command{
		gitLog(),
		{
			Name:    "git-push",
			Argv:    []string{"git", "-C", "{repo}", "push"},
			Confirm: true,
			Params:  map[string]config.CommandParam{"repo": {Type: TypePath, Under: "~/repos"}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, home
}

func TestSetTools(t *testing.T) {
	s, _ := testSet(t)
	specs := s.Tools()
	if len(specs) != 2 || specs[0].Name != "cmd.git-log" || specs[1].Name != "cmd.git-push" {
		t.Fatalf("Tools = %+v", specs)
	}
	if d := specs[0].Description; !strings.HasPrefix(d, "Recent commits.") ||
		!strings.Contains(d, "It runs: git -C {repo} log --oneline -n {count}.") {
		t.Errorf("description = %q", d)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
		Extra      bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(specs[0].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["count"]["type"] != "integer" || schema.Properties["count"]["maximum"] != float64(50) ||
		schema.Properties["repo"]["type"] != "string" || len(schema.Required) != 2 || schema.Extra {
		t.Errorf("schema = %s", specs[0].Parameters)
	}

	if s.Kind() != dispatch.KindCommand {
		t.Errorf("Kind = %q", s.Kind())
	}
	if s.Confirm("cmd.git-log") != dispatch.ConfirmNever || s.Confirm("cmd.git-push") != dispatch.ConfirmAsk {
		t.Error("Confirm doesn't follow confirm = true")
	}
	if server, tool := s.Locate("cmd.git-log"); server != "meru" || tool != "cmd.git-log" {
		t.Errorf("Locate = %q, %q", server, tool)
	}
}

func TestSetStatus(t *testing.T) {
	s, _ := testSet(t)
	st := s.Status()
	if len(st) != 1 || st[0].Name != "commands" || st[0].Kind != "command" || !st[0].Connected || st[0].Offered != 2 {
		t.Fatalf("Status = %+v", st)
	}
	push := st[0].Tools[1]
	if push.Name != "cmd.git-push" || !push.Confirm || rpc.ArgvLine(push.Argv) != "git -C {repo} push" {
		t.Errorf("git-push = %+v", push)
	}
	empty, _ := New(nil, nil)
	if empty.Status() != nil || len(empty.Tools()) != 0 {
		t.Error("an empty set shows a source")
	}
}

func TestAuditArgs(t *testing.T) {
	s, home := testSet(t)
	got := s.AuditArgs("cmd.git-log", json.RawMessage(`{"repo":"~/repos","count":5}`))
	var a audit
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("AuditArgs = %s: %v", got, err)
	}
	want := []string{"git", "-C", filepath.Join(home, "repos"), "log", "--oneline", "-n", "5"}
	if strings.Join(a.Argv, "|") != strings.Join(want, "|") || a.Params["repo"] != "~/repos" {
		t.Errorf("AuditArgs = %s", got)
	}
	// The argv comes first, so a cut log line still shows the program.
	if !strings.HasPrefix(string(got), `{"argv":["git",`) {
		t.Errorf("AuditArgs = %s, want the argv first", got)
	}
	for _, bad := range []string{`{"repo":"/etc","count":5}`, `{"count":5}`, `nope`} {
		if got := s.AuditArgs("cmd.git-log", json.RawMessage(bad)); got != nil {
			t.Errorf("AuditArgs(%s) = %s, want nil", bad, got)
		}
	}
	if s.AuditArgs("cmd.other", nil) != nil {
		t.Error("AuditArgs answered for a command it doesn't hold")
	}
}

// TestCallRefusesBadArgs checks that a value outside its limits comes back
// as a result the model reads, with no program run.
func TestCallRefusesBadArgs(t *testing.T) {
	s, _ := testSet(t)
	res, err := s.Call(context.Background(), "cmd.git-log", json.RawMessage(`{"repo":"/etc","count":5}`))
	if err != nil || !res.IsError || !strings.Contains(res.Text, "outside") || !strings.Contains(res.Text, "didn't run") {
		t.Errorf("Call = %+v, %v", res, err)
	}
	if _, err := s.Call(context.Background(), "cmd.other", nil); err == nil {
		t.Error("Call ran a command it doesn't hold")
	}
}

// recorder keeps the tool_calls rows dispatch writes.
type recorder struct {
	mu   sync.Mutex // guards rows
	rows []store.ToolCall
}

// InsertToolCall keeps row.
func (r *recorder) InsertToolCall(ctx context.Context, row store.ToolCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, row)
	return nil
}

// TestConfirmingCommandInAJob checks that a scheduled job can't run a
// command with confirm = true: nobody can say yes, so dispatch declines it
// without asking, and still writes the transcript lines and the row, with
// the argv the call would have run.
func TestConfirmingCommandInAJob(t *testing.T) {
	s, home := testSet(t)
	rec := &recorder{}
	d := dispatch.New([]dispatch.Backend{s}, rec, dispatch.Options{})
	var lines []transcript.Line
	asked := false
	res, out := d.Dispatch(context.Background(), dispatch.Call{
		ID: "call-1", Name: "cmd.git-push", Args: json.RawMessage(`{"repo":"~/repos"}`),
		Session: "s1", Source: rpc.SourceJob,
		Append: func(l transcript.Line) error { lines = append(lines, l); return nil },
		Approve: func(context.Context, rpc.Approval) (rpc.Choice, error) {
			asked = true
			return rpc.ChoiceOnce, nil
		},
	})
	if out.Outcome != dispatch.OutcomeDeclined || !res.IsError || asked {
		t.Errorf("outcome = %q, asked = %v, result = %+v; want declined without asking", out.Outcome, asked, res)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rec.rows))
	}
	row := rec.rows[0]
	if row.Kind != "command" || row.Server != "meru" || row.Tool != "cmd.git-push" || row.Outcome != "declined" {
		t.Errorf("row = %+v", row)
	}
	argv, _ := json.Marshal([]string{"git", "-C", filepath.Join(home, "repos"), "push"})
	if !strings.Contains(string(row.Args), `"argv":`+string(argv)) {
		t.Errorf("row args = %s, want the argv", row.Args)
	}
	if len(lines) != 2 || lines[0].Type != transcript.TypeToolCall || string(lines[0].Args) != string(row.Args) {
		t.Errorf("transcript lines = %+v", lines)
	}
}
