// This file serves merud's metrics for Prometheus, when [observability]
// prometheus names a loopback address. The handler, which collects the
// metrics and writes the text format, lives in internal/obs; this file
// owns the listener and the server's life. See ARCHITECTURE.md, "Reading
// the metrics".

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aarora79/meru/internal/obs"
)

// metricsTimeout bounds how long a client may take to send a request's
// headers, so a slow or stuck client can't hold a connection open.
const metricsTimeout = 5 * time.Second

// listenMetrics claims addr for /metrics. config has already checked that
// addr is loopback. It fails, naming the key, when another program holds
// the port.
func listenMetrics(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("observability.prometheus %s: %w; pick a free port, or set prometheus = \"\"", addr, err)
	}
	return ln, nil
}

// serveMetrics serves /metrics on ln until ctx ends. It runs in merud's
// errgroup: one function serves, a second waits for ctx and shuts the
// server down, which makes Serve return. Both return nil on a clean stop.
func serveMetrics(ctx context.Context, g *errgroup.Group, ln net.Listener, log *slog.Logger) {
	srv := &http.Server{
		Handler:           obs.MetricsHandler(log),
		ReadHeaderTimeout: metricsTimeout,
	}
	g.Go(func() error {
		// Serve returns http.ErrServerClosed after Shutdown, which means
		// the stop was asked for.
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics server: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		// ctx is over, so give Shutdown a fresh deadline of its own.
		sctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	})
}

// metricsURL is the address a client shows for /metrics, or "" when the
// page is off.
func metricsURL(ln net.Listener) string {
	if ln == nil {
		return ""
	}
	return "http://" + ln.Addr().String() + "/metrics"
}
