// This file tests the Usage page's two ways in: the usage op's detail kind,
// which sends the dashboard with the /metrics address, and the /metrics
// server, which starts on a loopback port and stops with merud's errgroup.

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

func TestHandleUsageDetail(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "e", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, metrics := range []string{"http://127.0.0.1:9464/metrics", ""} {
		var got []rpc.Event
		emit := func(ev rpc.Event) error {
			got = append(got, ev)
			return nil
		}
		req := rpc.Request{Op: rpc.OpUsage, Kind: rpc.UsageDetail}
		if err := handleUsage(ctx, st, req, metrics, emit); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Type != rpc.EventUsageDetail || got[0].UsageDetail == nil {
			t.Fatalf("events = %+v, want one usage_detail", got)
		}
		d := got[0].UsageDetail
		if d.Metrics != metrics {
			t.Errorf("Metrics = %q, want %q", d.Metrics, metrics)
		}
		if len(d.Days) != rpc.UsageDays {
			t.Errorf("%d days, want %d", len(d.Days), rpc.UsageDays)
		}
	}
}

// TestMetricsServer starts the /metrics server on a free loopback port,
// reads the page, then cancels the context and checks that the errgroup
// returns, which means both of the server's goroutines stopped.
func TestMetricsServer(t *testing.T) {
	ln, err := listenMetrics("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := metricsURL(ln)
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/metrics") {
		t.Errorf("metricsURL = %q", url)
	}

	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	serveMetrics(gctx, g, ln, slog.New(slog.DiscardHandler))

	// obs.Setup never ran in this test, so the handler has no reader and
	// answers 404; a 404 still shows the server is up and routing.
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 with no reader", resp.StatusCode)
	}

	cancel()
	if err := g.Wait(); err != nil {
		t.Errorf("errgroup: %v, want a clean stop", err)
	}
	if _, err := http.Get(url); err == nil {
		t.Error("the server still answers after it stopped")
	}
}

func TestListenMetricsTaken(t *testing.T) {
	ln, err := listenMetrics("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = listenMetrics(ln.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "observability.prometheus") {
		t.Errorf("second listen = %v, want an error naming the key", err)
	}
	if metricsURL(nil) != "" {
		t.Error("metricsURL(nil) is not empty")
	}
}
