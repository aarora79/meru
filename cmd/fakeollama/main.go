// This file holds main: flag parsing, the loopback check, the optional
// script file, and a clean shutdown on a signal.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// main parses flags, runs the server and exits non-zero on any error.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeollama:", err)
		os.Exit(1)
	}
}

// script is the shape of the -script file: replies to queue at start, by
// model name, where "" means any model.
type script struct {
	Replies map[string][]fakeollama.Reply `json:"replies"`
}

// run does the work of main and returns the first error. Keeping it separate
// from main lets deferred calls run before the process exits.
func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "loopback host:port to listen on; port 0 picks a free one")
	scriptPath := flag.String("script", "", "JSON file of replies to queue at start: {\"replies\": {\"model\": [Reply, ...]}}")
	models := flag.String("models", "", "comma-separated model names to accept; empty accepts any")
	loaded := flag.String("loaded", "", "comma-separated model names /api/ps reports at start")
	version := flag.String("version", "", "version /api/version reports (default 0.12.11)")
	latency := flag.Duration("latency", 0, "pause before every response")
	vision := flag.String("vision", "", "comma-separated model names /api/show lists with the vision capability")
	flag.Parse()

	if err := checkLoopback(*addr); err != nil {
		return err
	}

	fake := fakeollama.New(fakeollama.Config{
		Version: *version,
		Models:  splitList(*models),
		Loaded:  splitList(*loaded),
		Latency: *latency,
		// Each model -vision names can look at images; every other model
		// gets the fake's default, a text model.
		Capabilities: visionModels(splitList(*vision)),
	})
	if *scriptPath != "" {
		if err := loadScript(fake, *scriptPath); err != nil {
			return err
		}
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}
	srv := &http.Server{Handler: fake, ReadHeaderTimeout: 10 * time.Second}

	// signal.NotifyContext returns a context that ends when the process gets
	// SIGINT (Ctrl-C) or SIGTERM. stop releases the signal hook.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Serve blocks, so it runs in its own goroutine (a function running at
	// the same time as this one). The buffered channel lets the goroutine
	// hand back its error without waiting for anyone to read it.
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Printf("listening on http://%s\n", ln.Addr())

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	// After Shutdown, Serve returns http.ErrServerClosed, which is the
	// normal way for it to end.
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// checkLoopback refuses any listen address that isn't loopback. Even a test
// tool follows the rule that Meru's processes listen only on this machine.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse -addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("-addr %q isn't loopback; use 127.0.0.1 or [::1]", addr)
}

// loadScript reads the -script file and queues its replies on fake.
func loadScript(fake *fakeollama.Fake, path string) error {
	// The path comes from the person running this test tool, who can read
	// the file anyway, so gosec's file-inclusion warning (G304) doesn't apply.
	data, err := os.ReadFile(path) // #nosec G304 -- path is the -script flag of a local test tool
	if err != nil {
		return fmt.Errorf("read script: %w", err)
	}
	var s script
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("parse script %s: %w", path, err)
	}
	for model, replies := range s.Replies {
		fake.Enqueue(model, replies...)
	}
	return nil
}

// splitList turns "a, b" into ["a", "b"], and "" into nil.
func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// visionModels returns the /api/show capabilities for each model in
// names: a model with tools that can also look at images. nil for none.
func visionModels(names []string) map[string][]string {
	if len(names) == 0 {
		return nil
	}
	caps := map[string][]string{}
	for _, n := range names {
		caps[n] = []string{"completion", "vision", "tools"}
	}
	return caps
}
