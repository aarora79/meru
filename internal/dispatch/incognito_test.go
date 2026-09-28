// This file tests a call in an incognito chat: it runs and gets its
// tool_calls row, which keeps no arguments and no result.

package dispatch

import (
	"context"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestIncognitoCall(t *testing.T) {
	tests := []struct {
		name      string
		incognito bool
		wantArgs  bool
	}{
		{"an ordinary chat keeps the arguments and result", false, true},
		{"an incognito chat keeps neither", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{kind: KindMCP, tools: []string{"google.search_mail"},
				confirm: map[string]Confirm{"google.search_mail": ConfirmAsk}}
			rec := &fakeRecorder{}
			d := New([]Backend{b}, rec, Options{})
			c := newCall("google.search_mail", &sink{}, &approver{choice: rpc.ChoiceOnce})
			c.Session, c.Incognito = "incognito-0123abcd", tt.incognito
			res, out := d.Dispatch(context.Background(), c)
			if out.Outcome != OutcomeOK || res.Text != "ok from google.search_mail" {
				t.Fatalf("outcome %q, text %q; want the call to run", out.Outcome, res.Text)
			}
			if len(rec.rows) != 1 {
				t.Fatalf("rows = %+v, want one", rec.rows)
			}
			row := rec.rows[0]
			if got := row.Args != nil && row.Result != ""; got != tt.wantArgs {
				t.Errorf("row keeps args %s and result %q", row.Args, row.Result)
			}
			if row.Tool != "search_mail" || row.Server != "google" || row.Outcome != OutcomeOK ||
				row.Approval != "once" || row.Session != "incognito-0123abcd" || row.Time.IsZero() {
				t.Errorf("row = %+v", row)
			}
		})
	}
}
