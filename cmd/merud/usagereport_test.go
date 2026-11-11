// Tests for the usage_report op: each action replies with the status after
// it, "on" and "off" write config.toml, and the choice closes the notice.

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/usagereport"
)

// zeroQuestions is a usagereport.Questions that counts nothing.
type zeroQuestions struct{}

func (zeroQuestions) QuestionsBetween(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}

func (zeroQuestions) ActiveDaysBetween(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}

// newReportTest returns a reportService over a config.toml with no
// [usage_report] table, as a config from before the report has.
func newReportTest(t *testing.T) *reportService {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("profile = \"lite\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &reportService{
		reporter:   usagereport.New(dir, "v0.7.5", zeroQuestions{}, log),
		configPath: path,
		editConfig: func(f func() error) error { return f() },
		log:        log,
	}
}

// ask runs one usage_report action and returns the status it replied with.
func ask(t *testing.T, s *reportService, kind string) rpc.UsageReportStatus {
	t.Helper()
	var got *rpc.UsageReportStatus
	err := s.handle(context.Background(), rpc.Request{Op: rpc.OpUsageReport, Kind: kind, Source: rpc.SourceDesktop},
		func(e rpc.Event) error {
			if e.Type == rpc.EventUsageReport {
				got = e.UsageReport
			}
			return nil
		})
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	if got == nil {
		t.Fatalf("%s: no usage_report event", kind)
	}
	return *got
}

func TestUsageReportOp(t *testing.T) {
	s := newReportTest(t)

	st := ask(t, s, "")
	if st.Choice != "" || st.Banner != rpc.BannerAsk {
		t.Errorf("an upgraded config: choice %q, banner %q; want not chosen and the ask notice", st.Choice, st.Banner)
	}
	if st = ask(t, s, rpc.UsageReportSeen); st.Banner != rpc.BannerAsk {
		t.Errorf("after seen: banner %q, want it still up until the user chooses", st.Banner)
	}

	st = ask(t, s, rpc.UsageReportOn)
	cfg, err := config.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UsageReport.Send != config.ReportOn || st.Choice != config.ReportOn || st.Banner != "" {
		t.Errorf("after on: config %q, choice %q, banner %q", cfg.UsageReport.Send, st.Choice, st.Banner)
	}

	ask(t, s, rpc.UsageReportOff)
	cfg, _ = config.Load(s.configPath)
	if cfg.UsageReport.Send != config.ReportOff {
		t.Errorf("after off: config %q, want off", cfg.UsageReport.Send)
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(s.configPath), usagereport.FileName))
	if strings.Count(string(b), `"type":"seen"`) != 1 || strings.Count(string(b), `"type":"dismissed"`) != 1 {
		t.Errorf("record holds\n%s\nwant one seen and one dismissed line", b)
	}

	err = s.handle(context.Background(), rpc.Request{Op: rpc.OpUsageReport, Kind: "maybe"}, func(rpc.Event) error { return nil })
	if err == nil {
		t.Error("an unknown kind was taken")
	}
}

// TestAboutMeruNamesReport checks the line about_meru gives the model for
// each choice, so "does Meru send anything about me?" gets the true answer.
func TestAboutMeruNamesReport(t *testing.T) {
	for send, want := range map[string]string{
		"on":  "on: once a day",
		"off": "Meru sends nothing about itself",
		"":    "off until the user chooses",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[usage_report]\nsend = \""+send+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := usageReportSentence(path); !strings.Contains(got, want) {
			t.Errorf("send %q: about_meru says %q, want it to contain %q", send, got, want)
		}
	}
}

// TestOnBeforeNoticeCanSend checks "on" before any client showed the
// banner, as `meru report on` in a terminal does: the choice also marks the
// notice seen, so the report isn't left waiting for a banner that the
// choice itself hid. The first report then waits a day, as usual, and Send
// today's now can go at once.
func TestOnBeforeNoticeCanSend(t *testing.T) {
	s := newReportTest(t)
	st := ask(t, s, rpc.UsageReportOn)
	if strings.Contains(st.Blocked, "Waiting for meru chat") {
		t.Errorf("after on: Blocked = %q; the report would wait for a notice that never shows", st.Blocked)
	}
	if !strings.Contains(st.Blocked, "tomorrow") || !st.CanSend || st.Banner != "" {
		t.Errorf("after on: blocked %q, can send %v, banner %q; want the first-day wait, a live button and no banner",
			st.Blocked, st.CanSend, st.Banner)
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(s.configPath), usagereport.FileName))
	if strings.Count(string(b), `"type":"seen"`) != 1 {
		t.Errorf("record holds\n%s\nwant one seen line", b)
	}

	// "off" alone marks nothing seen: a user who says no gave no consent.
	off := newReportTest(t)
	ask(t, off, rpc.UsageReportOff)
	b, _ = os.ReadFile(filepath.Join(filepath.Dir(off.configPath), usagereport.FileName))
	if strings.Contains(string(b), `"type":"seen"`) {
		t.Errorf("off wrote a seen line:\n%s", b)
	}
}
