//go:build e2e

// This file tests the Usage page's data end to end: the dashboard a real
// merud sends after a few questions, and the /metrics page it serves when
// [observability] prometheus is set.

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// usageDetail asks merud for the dashboard over the socket, as both
// clients do, and returns it.
func usageDetail(t *testing.T, socket string) rpc.UsageDetailReply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	req := rpc.Request{Op: rpc.OpUsage, Kind: rpc.UsageDetail}
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			t.Fatalf("usage detail: %v", err)
		}
		if ev.Type == rpc.EventUsageDetail && ev.UsageDetail != nil {
			return *ev.UsageDetail
		}
	}
	t.Fatal("merud sent no usage_detail event")
	return rpc.UsageDetailReply{}
}

// TestUsageDetailE2E asks three questions, one of which calls a tool, and
// checks that the dashboard counts three questions today and the tool's ok
// call, with no /metrics address since the key is off.
func TestUsageDetailE2E(t *testing.T) {
	t.Parallel()
	s := startToolStack(t)
	s.fake.enqueue(t, fastModel, toolsRoute())
	s.fake.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "notes.search", Arguments: map[string]any{"query": "garden"}}}},
		fakeollama.Reply{Text: "Sow tomatoes on 12 April."})
	if res := runMeru(t, s.home, "when do I sow the tomatoes?"); res.code != 0 {
		t.Fatalf("meru exited %d:\n%s", res.code, res.stderr)
	}
	for _, q := range []string{"What is 2 + 2?", "What is the capital of France?"} {
		s.fake.enqueue(t, fastModel, directRoute())
		s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "An answer."})
		if res := runMeru(t, s.home, q); res.code != 0 {
			t.Fatalf("meru exited %d:\n%s", res.code, res.stderr)
		}
	}

	d := usageDetail(t, s.home.socket)
	if len(d.Days) != rpc.UsageDays {
		t.Fatalf("%d days, want %d", len(d.Days), rpc.UsageDays)
	}
	if today := d.Days[len(d.Days)-1]; today.Questions != 3 {
		t.Errorf("today = %+v, want 3 questions", today)
	}
	if len(d.Slowest) != 3 {
		t.Errorf("slowest = %+v, want the 3 questions", d.Slowest)
	}
	if len(d.Tools) != 1 || d.Tools[0].Tool != "search" || d.Tools[0].Outcomes["ok"] != 1 {
		t.Errorf("tools = %+v, want one ok call to search", d.Tools)
	}
	if d.Metrics != "" {
		t.Errorf("Metrics = %q with the key off", d.Metrics)
	}
}

// freePort returns a loopback address no program listens on right now.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestMetricsE2E starts merud with [observability] prometheus set, asks
// one question, and reads /metrics: the turn is counted, target_info names
// merud, and the dashboard carries the address.
func TestMetricsE2E(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	addr := freePort(t)
	h.writeConfig(t, fakeConfig(f.url, fmt.Sprintf("[router]\ntemperature = 1.0\n\n[observability]\nprometheus = %q\n", addr)))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	f.enqueue(t, fastModel, directRoute())
	f.enqueue(t, mainModel, fakeollama.Reply{Text: "Paris."})
	if res := runMeru(t, h, "What is the capital of France?"); res.code != 0 {
		t.Fatalf("meru exited %d:\n%s", res.code, res.stderr)
	}

	url := "http://" + addr + "/metrics"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v\nmerud.log:\n%s", url, err, h.log())
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d:\n%s", resp.StatusCode, body)
	}
	page := string(body)
	for _, want := range []string{`target_info{`, `service_name="merud"`, "# TYPE meru_turn_duration_seconds histogram", "meru_turn_duration_seconds_count{"} {
		if !strings.Contains(page, want) {
			t.Errorf("/metrics lacks %q:\n%s", want, page)
		}
	}

	if d := usageDetail(t, h.socket); d.Metrics != url {
		t.Errorf("Metrics = %q, want %q", d.Metrics, url)
	}
}
