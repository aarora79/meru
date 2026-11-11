// This file tests `meru report` against an in-process rpc server that plays
// merud, and the text it prints for each state of the count.

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// TestReportCommand checks each form of `meru report`: the kind it sends,
// that it never sends "seen", what it prints, and the line for a merud
// older than the op.
func TestReportCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		refuse   string // the error the fake merud sends; "" answers
		wantKind string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"status", []string{"report"}, "", rpc.UsageReportStatusKind, 0, "Anonymous daily count: Not chosen", ""},
		{"on", []string{"report", "on"}, "", rpc.UsageReportOn, 0, "Anonymous daily count: On", ""},
		{"off", []string{"report", "off"}, "", rpc.UsageReportOff, 0, "Anonymous daily count: Off", ""},
		{"older merud", []string{"report"}, `unknown op "usage_report"`, rpc.UsageReportStatusKind, 1, "",
			"meru: this merud is older than meru report; update merud and restart it"},
		{"refused", []string{"report", "on"}, "config.toml has a problem", rpc.UsageReportOn, 1, "",
			"meru: config.toml has a problem"},
		{"send", []string{"report", "send"}, "", rpc.UsageReportSend, 0, "Sent today's report.", ""},
		{"send refused", []string{"report", "send"}, "not sent: Today's has gone.", rpc.UsageReportSend, 1, "",
			"meru: not sent: Today's has gone."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []rpc.Request
			sock := startServer(t, func(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
				got = append(got, req)
				if tt.refuse != "" {
					return emit(rpc.Event{Type: rpc.EventError, Error: tt.refuse})
				}
				// The fake sets the choice the way merud would.
				st := rpc.UsageReportStatus{Version: 1}
				if req.Kind == rpc.UsageReportOn || req.Kind == rpc.UsageReportOff {
					st.Choice = req.Kind
				}
				return emit(rpc.Event{Type: rpc.EventUsageReport, UsageReport: &st})
			})
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if len(got) != 1 || got[0].Op != rpc.OpUsageReport || got[0].Kind != tt.wantKind || got[0].Source != rpc.SourceCLI {
				t.Errorf("requests = %+v, want one usage_report of kind %q from the CLI", got, tt.wantKind)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}

// TestReportArgsAreQuestions checks that words after "report" other than
// on, off and send make a question, not the command.
func TestReportArgsAreQuestions(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"on"}, true},
		{[]string{"off"}, true},
		{[]string{"send"}, true},
		{[]string{"seen"}, false},
		{[]string{"on", "the", "weather"}, false},
	} {
		if got := isReportArgs(tt.args); got != tt.want {
			t.Errorf("isReportArgs(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// TestReportText checks the printed status in a fixed time zone: the
// choice, the reason nothing goes, the next time, a changed collector,
// the next report, and the recent lines, cut to reportRecent.
func TestReportText(t *testing.T) {
	zone := time.FixedZone("test", 2*60*60) // two hours east of UTC
	body := `{"v":1,"meru":"0.7.3","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"0"}`
	recent := []rpc.UsageReportEntry{
		{Type: "sent", At: "2026-11-11T00:52:00Z", Body: body},
		{Type: "failed", At: "2026-11-10T00:40:00Z", Body: body, Error: "no network"},
		{Type: "seen", At: "2026-11-09T08:00:00Z", Client: "desktop"},
	}
	for range reportRecent {
		recent = append(recent, rpc.UsageReportEntry{Type: "dismissed", At: "2026-11-01T08:00:00Z"})
	}
	tests := []struct {
		name string
		s    rpc.UsageReportStatus
		want string
	}{
		{"not chosen", rpc.UsageReportStatus{Next: body},
			"Anonymous daily count: Not chosen\nRun meru report on or meru report off to choose.\n\nThe next report:\n" + body + "\n"},
		{"blocked", rpc.UsageReportStatus{Choice: config.ReportOff, Blocked: "DO_NOT_TRACK is set."},
			"Anonymous daily count: Off\nDO_NOT_TRACK is set.\n"},
		{"on", rpc.UsageReportStatus{Choice: config.ReportOn, NextAt: "2026-11-12T00:47:00Z", URL: "https://example.org", Next: body, Recent: recent},
			"Anonymous daily count: On\nNext one after 2026-11-12 02:47.\nSent to: https://example.org\n\nThe next report:\n" + body + "\n\nRecent:\n" +
				"  2026-11-11 02:52  sent  " + body + "\n" +
				"  2026-11-10 02:40  failed  no network\n" +
				"  2026-11-09 10:00  seen  by desktop\n" +
				"  2026-11-01 10:00  dismissed\n" +
				"  2026-11-01 10:00  dismissed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reportText(tt.s, zone); got != tt.want {
				t.Errorf("reportText =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestExampleVersion checks that only a release version shows as itself.
func TestExampleVersion(t *testing.T) {
	for v, want := range map[string]string{
		"v0.8.1":                               "0.8.1",
		"v0.7.3-acme":                          "dev",
		"(devel) 5325b3e, modified":            "dev",
		"v0.4.2-0.20260927021103-5325b3ef94cb": "dev",
	} {
		if got := exampleVersion(v); got != want {
			t.Errorf("exampleVersion(%q) = %q, want %q", v, got, want)
		}
	}
}
