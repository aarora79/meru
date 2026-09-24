// This file tests the meru client against an in-process rpc server: asking,
// pinging, errors, usage and Ctrl-C.

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// startServer serves h on a short socket path until the test ends.
func startServer(t *testing.T, h rpc.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "m.sock")

	ctx, cancel := context.WithCancel(context.Background())
	ln, err := rpc.Listen(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		rpc.Serve(ctx, ln, h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return sock
}

// answer returns a handler that streams the given pieces, after checking the
// question.
func answer(t *testing.T, wantQuestion string, pieces ...string) rpc.Handler {
	return func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		if req.Text != wantQuestion || req.Source != rpc.SourceCLI {
			t.Errorf("request = %+v, want text %q from cli", req, wantQuestion)
		}
		emit(rpc.Event{Type: rpc.EventSession, Session: "s"})
		emit(rpc.Event{Type: rpc.EventRoute, Route: "direct"})
		for _, p := range pieces {
			if err := emit(rpc.Event{Type: rpc.EventToken, Text: p}); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestRun(t *testing.T) {
	failing := func(context.Context, rpc.Request, func(rpc.Event) error, rpc.ApproveFunc) error {
		return errors.New("model not found")
	}
	tests := []struct {
		name       string
		handler    rpc.Handler
		args       []string // after -socket
		wantCode   int
		wantOut    string
		wantErrOut string
	}{
		{"ask", answer(t, "what is up", "not ", "much"), []string{"what is up"}, 0, "not much\n", ""},
		{"ask unquoted", answer(t, "what is up", "x\n"), []string{"what", "is", "up"}, 0, "x\n", ""},
		{"ping", answer(t, ""), []string{"ping"}, 0, "merud is up\n", ""},
		{"error event", failing, []string{"hi"}, 1, "", "meru: model not found"},
		{"chat without a terminal", answer(t, ""), []string{"chat"}, 1, "", "meru: chat:"},
		{"no arguments", answer(t, ""), nil, 1, "", "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := startServer(t, tt.handler)
			var out, errOut bytes.Buffer
			args := append([]string{"-socket", sock}, tt.args...)
			code := run(context.Background(), args, &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErrOut) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErrOut)
			}
		})
	}
}

func TestRunNoDaemon(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"-socket", filepath.Join(t.TempDir(), "none.sock"), "hi"}, &out, &errOut)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "is merud running?") {
		t.Errorf("stderr = %q, want a hint to start merud", errOut.String())
	}
}

func TestRunInterrupted(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		emit(rpc.Event{Type: rpc.EventToken, Text: "partial"})
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// cancelOnWrite plays the user pressing Ctrl-C once the first token shows.
	out := &cancelOnWrite{cancel: cancel}
	code := run(ctx, []string{"-socket", sock, "q"}, out, io.Discard)
	if code != exitInterrupted {
		t.Errorf("exit code = %d, want %d", code, exitInterrupted)
	}
}

// cancelOnWrite is an io.Writer that cancels a context on its first write.
type cancelOnWrite struct {
	cancel context.CancelFunc
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}
