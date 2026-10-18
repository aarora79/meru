// This file tests Call.Offered, the fence a scoped turn puts on what may
// run: a call to an allowed tool the round didn't offer is denied and
// still recorded, and a nil Offered fences nothing.

package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/transcript"
)

func TestOffered(t *testing.T) {
	tests := []struct {
		name     string
		offered  []string
		hint     string
		want     string // the outcome
		wantRan  bool
		wantText string // a part of what the model reads
	}{
		{"nil fences nothing", nil, "", OutcomeOK, true, "ok from"},
		{"an offered tool runs", []string{"web_search", "read_file"}, "", OutcomeOK, true, "ok from"},
		{"an allowed tool the round didn't offer is denied", []string{"read_file"}, "", OutcomeDenied, false, "isn't offered for this question"},
		{"an empty, non-nil list denies every tool", []string{}, "", OutcomeDenied, false, "isn't offered"},
		{"a hint replaces the refusal", []string{"read_file"}, "web-research is a skill", OutcomeDenied, false, "web-research is a skill"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{kind: KindBuiltin, tools: []string{"web_search", "read_file"}}
			rec := &fakeRecorder{}
			tr := &sink{}
			d := New([]Backend{b}, rec, Options{})
			c := newCall("web_search", tr, nil)
			c.Offered, c.Hint = tt.offered, tt.hint
			res, out := d.Dispatch(context.Background(), c)
			if out.Outcome != tt.want {
				t.Errorf("outcome %q, want %q", out.Outcome, tt.want)
			}
			if ran := b.callCount() == 1; ran != tt.wantRan {
				t.Errorf("tool ran = %v, want %v", ran, tt.wantRan)
			}
			if !strings.Contains(res.Text, tt.wantText) {
				t.Errorf("text %q, want it to hold %q", res.Text, tt.wantText)
			}
			// A fenced call is still on record: its tool_call and
			// tool_result lines, and its row.
			if len(rec.rows) != 1 || rec.rows[0].Outcome != tt.want {
				t.Errorf("rows = %+v, want one with outcome %q", rec.rows, tt.want)
			}
			var types []string
			for _, l := range tr.lines {
				types = append(types, l.Type)
			}
			if len(types) != 2 || types[0] != transcript.TypeToolCall || types[1] != transcript.TypeToolResult {
				t.Errorf("transcript lines = %v, want tool_call then tool_result", types)
			}
		})
	}
}
