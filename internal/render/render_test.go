// This file tests the Renderer's parts that need no browser: Close
// against a render that waits, the outcome names, and the flags that keep
// Chrome's traffic on the proxy.

package render

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/connectors"
)

func TestCloseDuringRender(t *testing.T) {
	r := New(connectors.NewInstaller(t.TempDir(), t.TempDir()), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.one <- struct{}{} // another render holds the token, so this one waits
	done := make(chan error, 1)
	go func() {
		_, err := r.Render(context.Background(), "https://example.com/", nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	closed := make(chan struct{})
	go func() { _ = r.Close(); close(closed) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Render = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close didn't end the waiting render")
	}
	<-closed
	if _, err := r.Render(context.Background(), "https://example.com/", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("Render after Close = %v", err)
	}
}

func TestOutcomeOf(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "cancelled"},
		{ErrClosed, "cancelled"},
		{errors.New("Chrome couldn't load"), "error"},
	}
	for _, tt := range tests {
		if got := outcomeOf(tt.err); got != tt.want {
			t.Errorf("outcomeOf(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// TestChromeFlags checks the flags the security review asked for: the
// pipe, the proxy with no loopback bypass, and no UDP around it.
func TestChromeFlags(t *testing.T) {
	flags := chromeFlags("/tmp/p", "127.0.0.1:9999")
	for _, want := range []string{
		"--remote-debugging-pipe",
		"--proxy-server=http://127.0.0.1:9999",
		"--proxy-bypass-list=<-loopback>",
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-background-networking",
		"--use-mock-keychain",
	} {
		if !slices.Contains(flags, want) {
			t.Errorf("flags lack %s", want)
		}
	}
	for _, f := range flags {
		if f == "--no-sandbox" || f == "--remote-debugging-port" {
			t.Errorf("flags include %s", f)
		}
	}
}
