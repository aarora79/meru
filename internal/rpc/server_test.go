// This file tests the server and client together over a real Unix socket in
// a temporary directory: round trips, errors, hang-ups and startup checks.

package rpc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// socketPath returns a socket path in a new temporary directory. It avoids
// t.TempDir because macOS caps socket paths at 104 bytes, and t.TempDir
// paths include the test's name.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// quietLog returns a logger that discards everything.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startServer listens on a fresh socket and serves h until the test ends. It
// returns the socket path.
func startServer(t *testing.T, h Handler) string {
	t.Helper()
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := Listen(ctx, path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// A channel passes values between goroutines. Here it only signals
	// "Serve returned", by being closed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := Serve(ctx, ln, h, quietLog()); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return path
}

// collect runs one request and returns every event, or the first error.
func collect(ctx context.Context, path string, req Request) ([]Event, error) {
	var evs []Event
	for ev, err := range Do(ctx, path, req) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func TestRoundTrip(t *testing.T) {
	echo := func(ctx context.Context, req Request, emit func(Event) error) error {
		if err := emit(Event{Type: EventSession, Session: "s1"}); err != nil {
			return err
		}
		for _, w := range strings.Fields(req.Text) {
			if err := emit(Event{Type: EventToken, Text: w}); err != nil {
				return err
			}
		}
		return nil
	}
	fail := func(ctx context.Context, req Request, emit func(Event) error) error {
		return errors.New("model fell over")
	}

	tests := []struct {
		name    string
		handler Handler
		req     Request
		want    []Event
	}{
		{"ping", echo, Request{Op: OpPing}, []Event{{Type: EventDone}}},
		{"ask", echo, Request{Op: OpAsk, Text: "a b"}, []Event{
			{Type: EventSession, Session: "s1"},
			{Type: EventToken, Text: "a"},
			{Type: EventToken, Text: "b"},
			{Type: EventDone},
		}},
		{"handler error", fail, Request{Op: OpAsk, Text: "x"}, []Event{
			{Type: EventError, Error: "model fell over"},
		}},
		{"unknown op", echo, Request{Op: "dance"}, []Event{
			{Type: EventError, Error: `unknown op "dance"`},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, tt.handler)
			got, err := collect(context.Background(), path, tt.req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("events = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestBadRequestGetsErrorEvent(t *testing.T) {
	path := startServer(t, func(context.Context, Request, func(Event) error) error {
		t.Error("handler ran for a bad request")
		return nil
	})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(line, `"type":"error"`) || !strings.Contains(line, "bad request") {
		t.Errorf("reply = %s, want an error event", line)
	}
}

func TestClientDisconnectCancelsHandler(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan error, 1)
	path := startServer(t, func(ctx context.Context, req Request, emit func(Event) error) error {
		if err := emit(Event{Type: EventToken, Text: "first"}); err != nil {
			return err
		}
		close(started)
		// select waits for whichever case is ready first.
		select {
		case <-ctx.Done():
			cancelled <- ctx.Err()
			return ctx.Err()
		case <-time.After(5 * time.Second):
			cancelled <- nil
			return nil
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for ev, err := range Do(ctx, path, Request{Op: OpAsk, Text: "q"}) {
		if err != nil {
			break
		}
		if ev.Type == EventToken {
			<-started
			cancel() // hang up, as Ctrl-C in meru would
		}
	}

	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("handler ctx error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler wasn't cancelled after the client hung up")
	}
}

func TestShutdownCancelsHandlers(t *testing.T) {
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := Listen(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	h := func(ctx context.Context, req Request, emit func(Event) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, h, quietLog()) }()

	go func() {
		for range Do(context.Background(), path, Request{Op: OpAsk, Text: "q"}) {
		}
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve didn't return after shutdown")
	}
}

func TestListenRefusesRunningServer(t *testing.T) {
	path := startServer(t, func(context.Context, Request, func(Event) error) error { return nil })
	_, err := Listen(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Errorf("second Listen = %v, want 'already listening'", err)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	path := socketPath(t)
	// Make a socket file with nobody behind it. A Go UnixListener deletes its
	// file on Close, so turn that off to leave the file behind like a crash.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	ln2, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	ln2.Close()
}

func TestListenRefusesRegularFile(t *testing.T) {
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(context.Background(), path); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	if b, _ := os.ReadFile(path); string(b) != "keep me" {
		t.Error("Listen changed the regular file")
	}
}

func TestSocketMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	path := socketPath(t)
	ln, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
}
