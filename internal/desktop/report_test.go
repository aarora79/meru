// This file tests the Bridge's usage-report calls against a fake merud,
// and against an older merud that doesn't know the op.

package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// TestUsageReportBridge checks that each call sends usage_report with its
// kind and the desktop source, that the view copies merud's status with
// an empty list where merud sent none, and that a bad kind or choice
// never reaches merud.
func TestUsageReportBridge(t *testing.T) {
	var got []rpc.Request
	b, _ := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		got = append(got, req)
		st := &rpc.UsageReportStatus{Choice: "on", Banner: rpc.BannerNew, Next: `{"v":1}`, Version: 1}
		if req.Kind == rpc.UsageReportOff {
			st = &rpc.UsageReportStatus{Choice: "off", Blocked: "Off.", Version: 1,
				Recent: []rpc.UsageReportEntry{{Type: "dismissed", At: "2026-11-11T09:00:00Z", Client: "desktop"}}}
		}
		return emit(rpc.Event{Type: rpc.EventUsageReport, UsageReport: st})
	}))
	defer b.ServiceShutdown()
	ctx := context.Background()

	v, err := b.UsageReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Available || v.Choice != "on" || v.Banner != rpc.BannerNew || v.Next != `{"v":1}` || v.Recent == nil {
		t.Errorf("status view = %+v", v)
	}
	if _, err := b.UsageReportMark(ctx, rpc.UsageReportSeen); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UsageReportMark(ctx, rpc.UsageReportDismiss); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UsageReportSet(ctx, rpc.UsageReportOn); err != nil {
		t.Fatal(err)
	}
	v, err = b.UsageReportSet(ctx, rpc.UsageReportOff)
	if err != nil {
		t.Fatal(err)
	}
	if v.Choice != "off" || v.Banner != "" || v.Blocked != "Off." || len(v.Recent) != 1 {
		t.Errorf("view after off = %+v", v)
	}

	for _, bad := range []func() error{
		func() error { _, err := b.UsageReportMark(ctx, rpc.UsageReportOn); return err },
		func() error { _, err := b.UsageReportMark(ctx, ""); return err },
		func() error { _, err := b.UsageReportSet(ctx, rpc.UsageReportSeen); return err },
		func() error { _, err := b.UsageReportSet(ctx, "maybe"); return err },
	} {
		if bad() == nil {
			t.Error("a bad kind or choice was accepted")
		}
	}

	want := []string{rpc.UsageReportStatusKind, rpc.UsageReportSeen, rpc.UsageReportDismiss, rpc.UsageReportOn, rpc.UsageReportOff}
	if len(got) != len(want) {
		t.Fatalf("requests = %+v, want %d", got, len(want))
	}
	for i, kind := range want {
		if got[i].Op != rpc.OpUsageReport || got[i].Kind != kind || got[i].Source != rpc.SourceDesktop {
			t.Errorf("request %d = %+v, want usage_report %q from the desktop", i, got[i], kind)
		}
	}
}

// TestUsageReportOldMerud checks that a merud from before the op, which
// answers `unknown op "usage_report"`, gives a view with Available false
// and no error, so the page hides the banner and the section; and that a
// merud that answers with some other event does the same. Another error
// still reaches the page.
func TestUsageReportOldMerud(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		reply   func(emit func(rpc.Event) error) error
		wantErr bool
	}{
		{"unknown op", func(emit func(rpc.Event) error) error {
			return emit(rpc.Event{Type: rpc.EventError, Error: `unknown op "usage_report"`})
		}, false},
		{"another event", func(emit func(rpc.Event) error) error {
			return emit(rpc.Event{Type: rpc.EventUsage})
		}, false},
		{"another error", func(emit func(rpc.Event) error) error {
			return emit(rpc.Event{Type: rpc.EventError, Error: "config.toml is locked"})
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
				return c.reply(emit)
			}))
			defer b.ServiceShutdown()
			v, err := b.UsageReport(ctx)
			if c.wantErr {
				if err == nil {
					t.Error("the error didn't reach the page")
				}
				return
			}
			if err != nil || v.Available || v.Recent == nil {
				t.Errorf("view = %+v, %v; want unavailable, no error, an empty list", v, err)
			}
		})
	}
}

// TestReportBannerNeverTakesFocus checks the banner's wiring: a region
// above the chat in index.html, drawn by report.js, which never moves
// focus, so a user typing in the composer when it appears loses no keys,
// and which tells merud "seen" once per run. The page has no JavaScript
// test runner, so this reads the source.
func TestReportBannerNeverTakesFocus(t *testing.T) {
	files := ownFiles(t)
	html := files["web/index.html"]
	banner := `<section class="report-banner" id="report-banner" role="region" aria-label="Anonymous daily count" hidden>`
	if !strings.Contains(html, banner) {
		t.Fatal("index.html has no hidden banner region")
	}
	if strings.Index(html, banner) > strings.Index(html, `id="messages"`) {
		t.Error("the banner must sit above the conversation")
	}
	src := files["web/js/report.js"]
	if strings.Contains(src, ".focus(") || strings.Contains(src, "autofocus") {
		t.Error("report.js moves focus; the banner must never take it")
	}
	if !strings.Contains(src, "!report.seenSent) {") || strings.Count(src, `usageReportMark("seen")`) != 1 {
		t.Error(`report.js should send "seen" once, behind the seenSent flag`)
	}
	if !strings.Contains(files["web/js/app.js"], "  loadReport();") {
		t.Error("app.js should ask for the banner on each status poll")
	}
}
