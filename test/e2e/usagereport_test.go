//go:build e2e

// End-to-end tests for the anonymous daily usage report: a release-stamped
// merud against a fake collector on loopback. They check the rule that
// matters most: nothing goes until the user chose "on" and a client showed
// the notice on an earlier day, and an upgrade never sends on its own.

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// collector is a fake usage-report collector that keeps every body it gets.
type collector struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

// startCollector starts a fake collector that answers 204, as the real one
// does for a good report.
func startCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, r.Method+" "+r.URL.Path+" "+string(b))
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// got returns a copy of what the collector has received.
func (c *collector) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

// reportHome makes a home whose config sends reports to c with send set to
// send ("" leaves the line out, as an upgraded config does) and debug logs
// on, so the test can wait for the loop's first decision. seen, when true,
// writes a "seen" line dated two days ago, so the one-day wait is over.
func reportHome(t *testing.T, f *fake, c *collector, send string, seen bool) *home {
	t.Helper()
	h := newHome(t)
	table := fmt.Sprintf("[usage_report]\nurl = %q\n", c.srv.URL)
	if send != "" {
		table += fmt.Sprintf("send = %q\n", send)
	}
	h.writeConfig(t, fakeConfig(f.url, "[log]\nlevel = \"debug\"\n\n"+table))
	if seen {
		line := fmt.Sprintf(`{"type":"seen","at":%q,"report":1,"client":"desktop"}`+"\n",
			time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(h.dir, "usage-report.jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// startReporting starts the release-stamped merud for h and waits until the
// report loop has made its first decision, sent or skipped.
func startReporting(t *testing.T, h *home, env ...string) *proc {
	t.Helper()
	env = append([]string{"HOME=" + h.dir, "USERPROFILE=" + h.dir}, env...)
	m := startProc(t, "merud-release", env, "-config", h.config, "-socket", h.socket)
	waitReady(t, h, m, readyTimeout)
	waitFor(t, readyTimeout, "the usage report loop's first decision", func() bool {
		log := h.log()
		return strings.Contains(log, "usage report skipped") || strings.Contains(log, "usage report sent") ||
			strings.Contains(log, "usage report failed")
	})
	return m
}

// reportStatus asks merud for the usage report's status.
func reportStatus(t *testing.T, h *home, kind string) rpc.UsageReportStatus {
	t.Helper()
	for _, ev := range op(t, h.socket, rpc.Request{Op: rpc.OpUsageReport, Kind: kind, Source: rpc.SourceDesktop}) {
		if ev.Type == rpc.EventUsageReport && ev.UsageReport != nil {
			return *ev.UsageReport
		}
	}
	t.Fatalf("usage_report %q: no usage_report event", kind)
	return rpc.UsageReportStatus{}
}

// TestReportUpgradeNeverSends is the promise to everyone who installed
// before the report existed: a config with no send line sends nothing,
// even with the notice seen, and the clients are told to ask.
func TestReportUpgradeNeverSends(t *testing.T) {
	t.Parallel()
	f, c := startFake(t), startCollector(t)
	h := reportHome(t, f, c, "", true)
	startReporting(t, h)
	if got := c.got(); len(got) != 0 {
		t.Fatalf("an upgraded config sent %v", got)
	}
	if st := reportStatus(t, h, ""); st.Choice != "" || st.Banner != rpc.BannerAsk {
		t.Errorf("status = choice %q, banner %q; want not chosen and the ask notice", st.Choice, st.Banner)
	}
}

// TestReportNotSeenNeverSends checks that "on" alone isn't enough: until a
// client shows the notice nothing goes, and the day it shows nothing goes
// either.
func TestReportNotSeenNeverSends(t *testing.T) {
	t.Parallel()
	f, c := startFake(t), startCollector(t)
	h := reportHome(t, f, c, "on", false)
	startReporting(t, h)
	st := reportStatus(t, h, rpc.UsageReportSeen)
	if got := c.got(); len(got) != 0 {
		t.Fatalf("sent before a client showed the notice: %v", got)
	}
	if !strings.Contains(st.Blocked, "tomorrow") {
		t.Errorf("after seen, Blocked = %q, want the first report held to tomorrow", st.Blocked)
	}
}

// TestReportDoNotTrack checks that DO_NOT_TRACK in merud's environment stops
// a report that would otherwise go.
func TestReportDoNotTrack(t *testing.T) {
	t.Parallel()
	f, c := startFake(t), startCollector(t)
	h := reportHome(t, f, c, "on", true)
	startReporting(t, h, "DO_NOT_TRACK=1")
	if got := c.got(); len(got) != 0 {
		t.Fatalf("sent with DO_NOT_TRACK=1: %v", got)
	}
	if st := reportStatus(t, h, ""); !strings.Contains(st.Blocked, "DO_NOT_TRACK") {
		t.Errorf("Blocked = %q, want it to name DO_NOT_TRACK", st.Blocked)
	}
}

// TestReportSendsAfterSeen checks the one path that sends: "on", the notice
// seen on an earlier day. The collector gets exactly the body the record
// holds, and the report holds the seven fields and nothing else.
func TestReportSendsAfterSeen(t *testing.T) {
	t.Parallel()
	// Each day's report waits for a random minute up to two hours after
	// midnight UTC. Before 02:00 UTC the loop may hold it, by design.
	if time.Now().UTC().Hour() < 2 {
		t.Skip("before 02:00 UTC the day's send minute may not have come yet")
	}
	f, c := startFake(t), startCollector(t)
	h := reportHome(t, f, c, "on", true)
	startReporting(t, h)
	waitFor(t, readyTimeout, "one report at the collector", func() bool { return len(c.got()) == 1 })

	got := c.got()[0]
	method, rest, _ := strings.Cut(got, " ")
	path, body, _ := strings.Cut(rest, " ")
	if method != http.MethodPost || path != "/v1/report" {
		t.Errorf("collector got %s %s, want POST /v1/report", method, path)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if len(fields) != 7 || fields["meru"] != strings.TrimPrefix(releaseVersion, "v") || fields["profile"] != "lite" {
		t.Errorf("body = %s, want the seven fields of a %s lite install", body, releaseVersion)
	}
	b, err := os.ReadFile(filepath.Join(h.dir, "usage-report.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var last struct{ Type, Body string }
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Type != "sent" || last.Body != body {
		t.Errorf("record's last line = %+v, want a sent line with the body the collector got", last)
	}
	if st := reportStatus(t, h, ""); !st.Sent || !strings.Contains(st.Blocked, "gone") {
		t.Errorf("status after the send = %+v, want Sent and today's report gone", st)
	}
}

// TestReportSendNow checks the "Send today's now" button: the day the
// notice first shows, a click sends one report at once, and a second click
// the same day sends nothing and says why.
func TestReportSendNow(t *testing.T) {
	t.Parallel()
	f, c := startFake(t), startCollector(t)
	h := reportHome(t, f, c, "on", false)
	startReporting(t, h)
	if st := reportStatus(t, h, rpc.UsageReportSeen); !st.CanSend {
		t.Fatalf("after the notice showed, CanSend = false; status %+v", st)
	}
	if st := reportStatus(t, h, rpc.UsageReportSend); !st.Sent || st.CanSend {
		t.Errorf("after a send: Sent %v, CanSend %v; want true, false", st.Sent, st.CanSend)
	}
	if got := c.got(); len(got) != 1 {
		t.Fatalf("collector got %d reports after one click, want 1", len(got))
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var refusal string
	for ev, err := range rpc.Do(ctx, h.socket, rpc.Request{Op: rpc.OpUsageReport, Kind: rpc.UsageReportSend, Source: rpc.SourceDesktop}, nil) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == rpc.EventError {
			refusal = ev.Error
		}
	}
	if !strings.Contains(refusal, "gone") {
		t.Errorf("a second click answered %q, want a refusal saying today's has gone", refusal)
	}
	if got := c.got(); len(got) != 1 {
		t.Errorf("collector got %d reports after two clicks, want 1", len(got))
	}
}
