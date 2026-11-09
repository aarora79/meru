// This file tests the tool notes History adds after an answer: one line
// per call, the caps, the web tools, and the pairing of results with
// calls in History, on disk and in an incognito session.

package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// called returns a tool call and its result, the two lines dispatch writes.
func called(id, server, tool, args, outcome, result string) []Line {
	return []Line{
		{Type: TypeToolCall, CallID: id, Server: server, Tool: tool, Args: json.RawMessage(args)},
		{Type: TypeToolResult, CallID: id, Outcome: outcome, Result: result},
	}
}

func TestToolNotes(t *testing.T) {
	long := strings.Repeat("word ", 200)
	tests := []struct {
		name  string
		calls []toolCall
		want  string // the note lines, after the header
	}{
		{"a built-in with its arguments and result",
			[]toolCall{{call: Line{Server: "meru", Tool: "analyze_table", Args: json.RawMessage(`{ "path": "~/trip.csv" }`)},
				result: &Line{Outcome: "ok", Result: "Matched 3 of 6 rows in trip (rows 4 to 7).\n| sum(Amount) |\n| --- |\n| 1207.00 |\nCite this table as [11]."}}},
			`- analyze_table {"path":"~/trip.csv"} ok: Matched 3 of 6 rows in trip (rows 4 to 7). | sum(Amount) | | --- | | 1207.00 |`},
		{"an MCP tool by server and name, with an error",
			[]toolCall{{call: Line{Server: "google", Tool: "get_events", Args: json.RawMessage(`{}`)}, result: &Line{Outcome: "error", Result: "calendar not found"}}},
			"- google.get_events error: calendar not found"},
		{"a call that never finished",
			[]toolCall{{call: Line{Server: "meru", Tool: "grep", Args: json.RawMessage(`{"pattern":"stars"}`)}}},
			`- grep {"pattern":"stars"} didn't finish`},
		{"a web tool by name only",
			[]toolCall{{call: Line{Server: "meru", Tool: WebSearchTool, Args: json.RawMessage(`{"query":"go release"}`)}, result: &Line{Outcome: "ok", Result: "[1] Go 1.27 — https://go.dev"}}},
			`- web_search {"query":"go release"} ok (what it found is in the web notes)`},
		{"a torn argument kept as it stands",
			[]toolCall{{call: Line{Server: "meru", Tool: "read_file", Args: json.RawMessage(`{"path":"~/a.md`)}, result: &Line{Outcome: "ok", Result: "text"}}},
			`- read_file {"path":"~/a.md ok: text`},
		{"a long result cut",
			[]toolCall{{call: Line{Server: "meru", Tool: "read_file"}, result: &Line{Outcome: "ok", Result: long}}},
			"- read_file ok: " + strings.TrimSpace(long[:maxNoteTextChars-1]) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolNotes(tt.calls)
			want := "\n\n" + toolNotesHead + "\n" + tt.want
			if got != want {
				t.Errorf("toolNotes =\n%q\nwant\n%q", got, want)
			}
		})
	}
	if got := toolNotes(nil); got != "" {
		t.Errorf("toolNotes of no calls = %q, want empty", got)
	}
}

func TestToolNotesCap(t *testing.T) {
	var calls []toolCall
	for i := range 10 {
		tool := "grep"
		if i == 9 {
			tool = "list_folder"
		}
		calls = append(calls, toolCall{call: Line{Server: "meru", Tool: tool, Args: json.RawMessage(fmt.Sprintf(`{"pattern":"p%d"}`, i))},
			result: &Line{Outcome: "ok", Result: strings.Repeat("x", 100)}})
	}
	got := toolNotes(calls)
	if n := strings.Count(got, "\n- "); n != maxToolNotes+1 {
		t.Errorf("%d note lines, want %d and the count line:\n%s", n, maxToolNotes, got)
	}
	if !strings.HasSuffix(got, "\n- and 4 more: grep ×3, list_folder") {
		t.Errorf("notes end %q, want the count line", got[strings.LastIndex(got, "\n"):])
	}
	if n := utf8.RuneCountInString(got) - utf8.RuneCountInString(toolNotesHead); n > maxToolNoteChars+100 {
		t.Errorf("notes run to %d characters past the header", n)
	}
}

func TestHistoryToolNotes(t *testing.T) {
	var lines []Line
	lines = append(lines, Line{Type: TypeUser, Text: "q1"})
	lines = append(lines, called("c1", "meru", "grep", `{"pattern":"one"}`, "ok", "r1")...)
	lines = append(lines, Line{Type: TypeAssistant, Text: "a1"}, Line{Type: TypeUser, Text: "q2"})
	// Two calls of one round whose results come back in the other order,
	// and a web-first call merud made itself.
	lines = append(lines,
		Line{Type: TypeToolCall, CallID: "w", Server: "meru", Tool: WebSearchTool, Caller: "meru"},
		Line{Type: TypeToolResult, CallID: "w", Outcome: "ok", Result: "web"},
		Line{Type: TypeToolCall, CallID: "c2", Server: "meru", Tool: "read_file", Args: json.RawMessage(`{"path":"a"}`)},
		Line{Type: TypeToolCall, CallID: "c3", Server: "meru", Tool: "read_file", Args: json.RawMessage(`{"path":"b"}`)},
		Line{Type: TypeToolResult, CallID: "c3", Outcome: "ok", Result: "text of b"},
		Line{Type: TypeToolResult, CallID: "c2", Outcome: "ok", Result: "text of a"},
		Line{Type: TypeAssistant, Text: "a2"},
		Line{Type: TypeUser, Text: "q3"})
	lines = append(lines, called("c4", "meru", "analyze_table", `{"path":"t.csv"}`, "ok", "| 5 |")...)
	lines = append(lines, Line{Type: TypeAssistant, Text: "a3"})

	for _, incognito := range []bool{false, true} {
		t.Run(fmt.Sprintf("incognito=%v", incognito), func(t *testing.T) {
			s := sessionWith(t, incognito, lines)
			got, err := s.History(10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 6 {
				t.Fatalf("History = %d messages, want 6", len(got))
			}
			if got[1].Content != "a1" {
				t.Errorf("the oldest answer = %q; only the last two turns carry notes", got[1].Content)
			}
			want2 := "a2\n\n" + toolNotesHead + "\n- read_file {\"path\":\"a\"} ok: text of a\n- read_file {\"path\":\"b\"} ok: text of b"
			if got[3].Content != want2 {
				t.Errorf("second answer =\n%q\nwant\n%q", got[3].Content, want2)
			}
			want3 := "a3\n\n" + toolNotesHead + "\n- analyze_table {\"path\":\"t.csv\"} ok: | 5 |"
			if got[5].Content != want3 {
				t.Errorf("last answer =\n%q\nwant\n%q", got[5].Content, want3)
			}
		})
	}
}

// sessionWith returns a session holding lines: a file under t.TempDir, or
// an incognito session in memory.
func sessionWith(t *testing.T, incognito bool, lines []Line) *Session {
	t.Helper()
	s := NewIncognito()
	if !incognito {
		var err error
		if s, err = New(t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range lines {
		if err := s.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestResultExcerptKeepsSavedPath(t *testing.T) {
	result := "Attachment downloaded successfully!\nMessage ID: 1a1bbafda9ba28d1\nFilename: IMG_0584.jpeg\n" +
		"Download URL: http://localhost:8000/attachments/" + strings.Repeat("x", 400) + "\n\n" +
		"Saved at ~/meru-output/attachments/IMG_0584_b14ae49e.jpeg."
	got := resultExcerpt(result)
	if !strings.HasPrefix(got, "Saved at ~/meru-output/attachments/IMG_0584_b14ae49e.jpeg.") {
		t.Errorf("excerpt = %q, want it to start with the saved path", got)
	}
}
