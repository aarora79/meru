// This file tests the text "Edit first" puts in the composer, and the
// server and tool an approval card names for the side panel.

package desktop

import (
	"encoding/json"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestApprovalDraft(t *testing.T) {
	tests := []struct {
		name, tool, kind, args string
		wantServer, wantTool   string
		want                   string
	}{
		{
			"mail", "google.send_gmail_message", "mcp",
			`{"to":["dana@example.com"],"subject":"Lisbon","body":"Check-in is at 15:00.\nSee you there."}`,
			"google", "send_gmail_message",
			"Send this mail instead:\n\nTo: dana@example.com\nSubject: Lisbon\n\nCheck-in is at 15:00.\nSee you there.",
		},
		{
			"a draft with more keys", "google.draft_gmail_message", "mcp",
			`{"to":"sam.okafor@example.com","subject":"Friday","body":"Hi Sam","thread":"t1"}`,
			"google", "draft_gmail_message",
			"Draft this mail instead:\n\nTo: sam.okafor@example.com\nSubject: Friday\n\nHi Sam\n\nOther details: {\n  \"thread\": \"t1\"\n}",
		},
		{
			"not mail", "write_file", "builtin", `{"path":"notes/plan.md"}`,
			"", "write_file",
			"Run write_file with these arguments instead:\n\n{\n  \"path\": \"notes/plan.md\"\n}",
		},
		{"no arguments", "cmd.git-log", "command", ``, "", "git-log", "Run cmd.git-log instead."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := approvalView(rpc.Approval{ID: "1", Name: tt.tool, Kind: tt.kind, Args: json.RawMessage(tt.args)})
			if v.Draft != tt.want {
				t.Errorf("draft =\n%q\nwant\n%q", v.Draft, tt.want)
			}
			if v.Server != tt.wantServer || v.Tool != tt.wantTool {
				t.Errorf("server, tool = %q, %q; want %q, %q", v.Server, v.Tool, tt.wantServer, tt.wantTool)
			}
		})
	}
}
