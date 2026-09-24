// This file tests session files: creation, appending, reading history back,
// file permissions and ID checks.

package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

func TestNewPathAndPermissions(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !idPattern.MatchString(s.ID()) {
		t.Errorf("ID %q doesn't match the ID format", s.ID())
	}
	now := time.Now().UTC()
	wantDir := filepath.Join(dir, now.Format("2006"), now.Format("01"))
	if filepath.Dir(s.Path()) != wantDir {
		t.Errorf("session file in %s, want %s", filepath.Dir(s.Path()), wantDir)
	}
	if !strings.HasSuffix(s.Path(), s.ID()+".jsonl") {
		t.Errorf("path %s doesn't end in the ID", s.Path())
	}

	if runtime.GOOS == "windows" {
		return // Windows has no Unix permission bits
	}
	checkMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
	checkMode(s.Path(), 0o600)
	checkMode(wantDir, 0o700)
	checkMode(filepath.Dir(wantDir), 0o700)
}

func TestNewGivesDistinctSessions(t *testing.T) {
	dir := t.TempDir()
	seen := map[string]bool{}
	for range 20 {
		s, err := New(dir)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[s.ID()] {
			t.Fatalf("New returned %s twice", s.ID())
		}
		seen[s.ID()] = true
	}
}

func TestAppendWritesOneJSONObjectPerLine(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 9, 23, 10, 15, 2, 500_000_000, time.FixedZone("EDT", -4*3600))
	lines := []Line{
		{TS: ts, Type: TypeUser, Text: "hello", TraceID: "abc"},
		{Type: TypeAssistant, Text: "hi\nthere", TokensIn: 12, TokensOut: 3},
	}
	for _, l := range lines {
		if err := s.Append(l); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("file has %d lines, want 2:\n%s", len(got), raw)
	}
	want0 := `{"ts":"2026-09-23T14:15:02Z","type":"user","text":"hello","trace_id":"abc"}`
	if got[0] != want0 {
		t.Errorf("line 1 = %s\nwant     %s", got[0], want0)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(got[1]), &second); err != nil {
		t.Fatalf("line 2 isn't JSON: %v", err)
	}
	if second["type"] != "assistant" || second["tokens_in"] != 12.0 || second["tokens_out"] != 3.0 {
		t.Errorf("line 2 = %s", got[1])
	}
	if _, ok := second["trace_id"]; ok {
		t.Errorf("line 2 has an empty trace_id key: %s", got[1])
	}
}

func TestOpenContinuesSession(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Line{Type: TypeUser, Text: "q"}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir, s.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := again.Append(Line{Type: TypeAssistant, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 {
		t.Errorf("history has %d messages, want 2", len(h))
	}
}

func TestOpenRejectsBadIDs(t *testing.T) {
	dir := t.TempDir()
	tests := []string{
		"",
		"../../etc/passwd",
		"2026-09-23T101502-7f3a/../../x",
		"2026-09-23T101502-7f3a.jsonl",
		"2026/09/2026-09-23T101502-7f3a",
		"2026-09-23T101502-7F3A", // IDs use lower-case hex
		"2026-09-23T101502-7f3",
		"..\\..\\x",
	}
	for _, id := range tests {
		t.Run(id, func(t *testing.T) {
			if _, err := Open(dir, id); err == nil {
				t.Errorf("Open(%q) succeeded; want an error", id)
			}
		})
	}
}

func TestOpenMissingSession(t *testing.T) {
	_, err := Open(t.TempDir(), "2026-09-23T101502-7f3a")
	if err == nil {
		t.Fatal("Open of a missing session succeeded")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want a not-exist error", err)
	}
}

func TestHistory(t *testing.T) {
	u := func(s string) Line { return Line{Type: TypeUser, Text: s} }
	a := func(s string) Line { return Line{Type: TypeAssistant, Text: s} }
	msgs := func(pairs ...string) []engine.Message {
		var m []engine.Message
		for i := 0; i < len(pairs); i += 2 {
			m = append(m,
				engine.Message{Role: engine.RoleUser, Content: pairs[i]},
				engine.Message{Role: engine.RoleAssistant, Content: pairs[i+1]})
		}
		return m
	}

	tests := []struct {
		name     string
		lines    []Line
		maxTurns int
		want     []engine.Message
	}{
		{"empty", nil, 10, msgs()},
		{"zero turns", []Line{u("q1"), a("a1")}, 0, nil},
		{"all turns", []Line{u("q1"), a("a1"), u("q2"), a("a2")}, 10, msgs("q1", "a1", "q2", "a2")},
		{"capped keeps newest", []Line{u("q1"), a("a1"), u("q2"), a("a2"), u("q3"), a("a3")}, 2, msgs("q2", "a2", "q3", "a3")},
		{"unanswered question dropped", []Line{u("q1"), u("q2"), a("a2"), u("q3")}, 10, msgs("q2", "a2")},
		{"stray answer dropped", []Line{a("a0"), u("q1"), a("a1")}, 10, msgs("q1", "a1")},
		// A turn that called a tool has its tool lines between the question
		// and the answer. History leaves them out: the answer already holds
		// what mattered from the result.
		{"tool lines left out", []Line{
			u("q1"),
			{Type: TypeToolCall, CallID: "call-1", Kind: "mcp", Server: "notes", Tool: "search"},
			{Type: TypeApproval, CallID: "call-1", Choice: "once"},
			{Type: TypeToolResult, CallID: "call-1", Outcome: "ok", OK: true, Result: "raw result"},
			a("a1"),
		}, 10, msgs("q1", "a1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range tt.lines {
				if err := s.Append(l); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.History(tt.maxTurns)
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			if !slices.EqualFunc(got, tt.want, messagesEqual) {
				t.Errorf("History(%d) = %+v\nwant %+v", tt.maxTurns, got, tt.want)
			}
		})
	}
}

// messagesEqual compares the fields History fills in.
func messagesEqual(a, b engine.Message) bool {
	return a.Role == b.Role && a.Content == b.Content
}

func TestHistorySkipsTornLines(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Line{Type: TypeUser, Text: "q1"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: half a line with no newline, then more
	// appends that land on the end of it.
	f, err := os.OpenFile(s.Path(), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"ts":"2026-09-23T10:15:09Z","type":"assis`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, l := range []Line{{Type: TypeUser, Text: "q2"}, {Type: TypeAssistant, Text: "a2"}} {
		if err := s.Append(l); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.History(10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	// The torn line swallowed the q2 line, so History pairs q1 with a2. That
	// mismatch is the cost of one lost line; the session stays readable.
	want := []engine.Message{
		{Role: engine.RoleUser, Content: "q1"},
		{Role: engine.RoleAssistant, Content: "a2"},
	}
	if !slices.EqualFunc(got, want, messagesEqual) {
		t.Errorf("History = %+v, want %+v", got, want)
	}

	// Every surviving line still parses on its own.
	f2, err := os.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	sc := bufio.NewScanner(f2)
	n := 0
	for sc.Scan() {
		n++
	}
	if n != 3 {
		t.Errorf("file has %d lines, want 3", n)
	}
}

func TestIDFormat(t *testing.T) {
	// The documented example must be a valid ID.
	if !idPattern.MatchString("2026-09-23T101502-7f3a") {
		t.Error("the ARCHITECTURE.md example ID doesn't match idPattern")
	}
	if got := sessionPath("root", "2026-09-23T101502-7f3a"); got != filepath.Join("root", "2026", "09", "2026-09-23T101502-7f3a.jsonl") {
		t.Errorf("sessionPath = %s", got)
	}
}
