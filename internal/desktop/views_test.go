// This file tests the views the page draws: each tool call's friendly
// label, who a turn contacted, and the approval card's layout.

package desktop

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestStepLabel(t *testing.T) {
	tests := []struct {
		kind, name, args string
		want             string
	}{
		{"mcp", "google.search_gmail_messages", `{"query":"Lisbon"}`, "Searched mail"},
		{"mcp", "google.send_gmail_message", `{"to":"dana@example.com"}`, "Sent mail"},
		{"mcp", "google.draft_gmail_message", ``, "Drafted mail"},
		{"mcp", "google.get_gmail_message_content", ``, "Read mail"},
		{"mcp", "google.get_events", ``, "Checked calendar"},
		{"mcp", "google.create_event", ``, "Changed calendar"},
		{"mcp", "obsidian.obsidian_search_vault", ``, "Searched notes"},
		{"mcp", "obsidian.obsidian_read_note", `{"path":"Travel/lisbon.md"}`, "Read lisbon.md"},
		{"mcp", "obsidian.obsidian_read_note", ``, "Read a note"},
		{"mcp", "google.list_drive_items", ``, "Used google list drive items"},
		{"builtin", "read_file", `{"path":"~/Notes/lisbon.md"}`, "Read lisbon.md"},
		{"builtin", "read_file", `{"path":"C:\\Notes\\folio.pdf"}`, "Read folio.pdf"},
		{"builtin", "read_file", `not json`, "Read a file"},
		{"builtin", "list_folder", `{"path":"~/Notes/"}`, "Listed Notes"},
		{"builtin", "search_files", ``, "Searched your files"},
		{"builtin", "grep", ``, "Searched your files"},
		{"builtin", "web_search", `{"query":"Lisbon trams"}`, "Searched the web"},
		{"builtin", "web_fetch", `{"url":"https://example.com/trams"}`, "Read example.com"},
		{"builtin", "web_fetch", ``, "Read a web page"},
		{"builtin", "remember", ``, "Saved a memory"},
		{"builtin", "write_file", `{"path":"plan.md"}`, "Wrote plan.md"},
		{"builtin", "configure", ``, "Changed settings"},
		{"builtin", "new_tool", ``, "Used new tool"},
		{"a2a", "a2a.travel.book_hotel", ``, "Asked travel"},
		{"command", "cmd.backup", ``, "Ran backup"},
	}
	for _, tt := range tests {
		t.Run(tt.name+" "+tt.args, func(t *testing.T) {
			s := stepOf("1", tt.kind, tt.name, json.RawMessage(tt.args))
			if s.Label != tt.want {
				t.Errorf("label = %q, want %q", s.Label, tt.want)
			}
		})
	}
}

func TestSplitName(t *testing.T) {
	tests := []struct {
		kind, name, server, tool string
	}{
		{"mcp", "google.search_gmail_messages", "google", "search_gmail_messages"},
		{"mcp", "plain", "", "plain"},
		{"a2a", "a2a.travel.book", "travel", "book"},
		{"a2a", "a2a.travel", "travel", ""},
		{"command", "cmd.backup", "", "backup"},
		{"builtin", "read_file", "", "read_file"},
	}
	for _, tt := range tests {
		s, tool := splitName(tt.kind, tt.name)
		if s != tt.server || tool != tt.tool {
			t.Errorf("splitName(%s, %s) = %q, %q; want %q, %q", tt.kind, tt.name, s, tool, tt.server, tt.tool)
		}
	}
}

func TestContacted(t *testing.T) {
	steps := []Step{
		stepOf("1", "mcp", "google.search_gmail_messages", nil),
		stepOf("2", "builtin", "read_file", json.RawMessage(`{"path":"a.md"}`)),
		stepOf("3", "mcp", "google.get_events", nil),
		stepOf("4", "builtin", "web_search", nil),
		stepOf("5", "builtin", "web_fetch", json.RawMessage(`{"url":"https://example.com/x"}`)),
		stepOf("6", "a2a", "a2a.travel.book", nil),
		stepOf("7", "mcp", "obsidian.obsidian_read_note", nil),
		stepOf("8", "command", "cmd.backup", nil),
	}
	steps[6].Outcome = "declined" // the user said no, so obsidian never ran
	want := []string{"google", "web search", "example.com", "travel"}
	if got := contacted(steps); !reflect.DeepEqual(got, want) {
		t.Errorf("contacted = %v, want %v", got, want)
	}
	if got := contacted(nil); got != nil {
		t.Errorf("contacted(nil) = %v, want nil", got)
	}
}

func TestApprovalView(t *testing.T) {
	choices := []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}
	tests := []struct {
		name       string
		args       string
		wantFields []Field
		wantJSON   string
	}{
		{
			"mail with other keys",
			`{"To":["dana@example.com","sam@example.com"],"subject":"Lisbon","body":"Check-in is at 15:00.","thread":"t1"}`,
			[]Field{{"To", "dana@example.com, sam@example.com"}, {"Subject", "Lisbon"}, {"Body", "Check-in is at 15:00."}},
			"{\n  \"thread\": \"t1\"\n}",
		},
		{
			"not mail",
			`{"path":"~/meru-output/plan.md","text":"<b>hi</b>"}`,
			nil,
			"{\n  \"path\": \"~/meru-output/plan.md\",\n  \"text\": \"<b>hi</b>\"\n}",
		},
		{"body alone is not mail", `{"body":"x"}`, nil, "{\n  \"body\": \"x\"\n}"},
		{"not an object", `["a","b"]`, nil, "[\n  \"a\",\n  \"b\"\n]"},
		{"no arguments", ``, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := approvalView(rpc.Approval{ID: "3", Name: "google.send_gmail_message", Kind: "mcp",
				Args: json.RawMessage(tt.args), Choices: choices})
			if !reflect.DeepEqual(v.Fields, tt.wantFields) {
				t.Errorf("fields = %+v, want %+v", v.Fields, tt.wantFields)
			}
			if v.JSON != tt.wantJSON {
				t.Errorf("JSON = %q, want %q", v.JSON, tt.wantJSON)
			}
			if v.ID != "3" || v.Label != "Sent mail" {
				t.Errorf("card = %+v, want ID 3 and the step label", v)
			}
			wantChoices := []ChoiceView{
				{rpc.ChoiceOnce, "Allow once"}, {rpc.ChoiceSession, "Allow for this chat"}, {rpc.ChoiceDeny, "Don't allow"},
			}
			if !reflect.DeepEqual(v.Choices, wantChoices) {
				t.Errorf("choices = %+v, want %+v", v.Choices, wantChoices)
			}
		})
	}
}
