// This file tests the datetime tool.

package builtin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
)

func TestDateTime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 14, 5, 0, 0, ny)
	tests := []struct {
		name    string
		args    string
		want    []string
		wantErr string
	}{
		{"no args", ``, []string{"Now: Thursday, 24 September 2026, 14:05 EDT (UTC-04:00); ISO 2026-09-24T14:05:00-04:00."}, ""},
		{"empty object", `{}`, []string{"Now: Thursday, 24 September 2026, 14:05"}, ""},
		{"another zone", `{"timezone":"Asia/Kolkata"}`, []string{"In Asia/Kolkata: Thursday, 24 September 2026, 23:35 IST (UTC+05:30)"}, ""},
		{"a date ahead", `{"date":"2026-12-25"}`, []string{"2026-12-25 is a Friday, 92 days from today."}, ""},
		{"today", `{"date":"2026-09-24"}`, []string{"2026-09-24 is a Thursday, today."}, ""},
		{"tomorrow", `{"date":"2026-09-25"}`, []string{"tomorrow"}, ""},
		{"a date past", `{"date":"2026-09-01"}`, []string{"2026-09-01 is a Tuesday, 23 days ago."}, ""},
		{"across daylight saving", `{"date":"2026-11-02"}`, []string{"39 days from today"}, ""},
		{"bad zone", `{"timezone":"Mars/Olympus"}`, nil, "isn't a time zone name"},
		{"bad date", `{"date":"25/12/2026"}`, nil, "isn't YYYY-MM-DD"},
		{"not json", `{`, nil, "arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dateTime(now, json.RawMessage(tt.args))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("result lacks %q:\n%s", w, got)
				}
			}
		})
	}
}

// TestDateTimeTool runs datetime through the backend: it never asks, and
// Call reads the Tools clock.
func TestDateTimeTool(t *testing.T) {
	tools := New("", config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	tools.now = func() time.Time { return time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC) }
	if c := tools.Confirm(DateTime); c != dispatch.ConfirmNever {
		t.Errorf("Confirm(datetime) = %v, want never", c)
	}
	res, err := tools.Call(t.Context(), DateTime, json.RawMessage(`{}`))
	if err != nil || res.IsError || !strings.Contains(res.Text, "Thursday, 24 September 2026, 09:00 UTC") {
		t.Errorf("Call = %+v, %v", res, err)
	}
}
