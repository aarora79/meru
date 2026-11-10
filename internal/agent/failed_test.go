// This file tests the notice under an answer that went without a tool call
// that failed.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

func TestFailedCallsNotice(t *testing.T) {
	tests := []struct {
		name   string
		tried  []triedCall
		answer string
		want   string // "" for no notice
	}{
		{"a failed call never redone", []triedCall{{"read_file", "ok"}, {"tracker.list_issues", dispatch.OutcomeError}},
			"No new issues today.", "tracker.list_issues failed"},
		{"redone and it worked", []triedCall{{"tracker.list_issues", dispatch.OutcomeError}, {"tracker.list_projects", "ok"}, {"tracker.list_issues", "ok"}},
			"You have 3 open issues.", ""},
		{"the answer says so", []triedCall{{"tracker.list_issues", dispatch.OutcomeError}},
			"I couldn't read your issues, so I can't say.", ""},
		{"a timeout counts", []triedCall{{"notes.search", dispatch.OutcomeTimeout}}, "Nothing found.", "notes.search failed"},
		{"the user said no doesn't count", []triedCall{{"mail.send", dispatch.OutcomeDeclined}}, "OK, not sent.", ""},
		{"two tools", []triedCall{{"a.one", dispatch.OutcomeError}, {"b.two", dispatch.OutcomeError}, {"a.one", dispatch.OutcomeError}},
			"Here is what I found.", "These tool calls failed, so this answer goes without what they would have read: a.one, b.two."},
		{"every call worked", []triedCall{{"read_file", "ok"}}, "Here it is.", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := failedCallsNotice(tt.tried, tt.answer)
			if tt.want == "" && got != "" || tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("failedCallsNotice = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFailedCallNoticeInATurn runs the real case: a lookup fails for a
// missing argument, the model answers anyway, and the notice says so.
func TestFailedCallNoticeInATurn(t *testing.T) {
	tools := &fakeTools{specs: []engine.ToolSpec{spec("tracker.list_issues")},
		results: map[string]fakeResult{"tracker.list_issues": {
			text:    "The tool call failed: missing properties: [\"project_id\"]",
			outcome: dispatch.OutcomeError,
		}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("tracker.list_issues", `{"label":"bug"}`)}},
		{pieces: []string{"No new bugs today."}},
	}}
	evs, err := run(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "how many bugs came in today"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
	if i < 0 || !strings.Contains(evs[i].Text, "tracker.list_issues failed") {
		t.Errorf("events %v: want the failed-call notice", types(evs))
	}
}
