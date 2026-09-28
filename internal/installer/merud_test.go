// This file tests the Start Meru step: merud's launchd job, and Ping and
// FollowScan against a fake merud that answers on a real socket.

package installer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// TestMerudPlistIsDeployCopy fails when the embedded plist and the one in
// deploy/launchd differ: the installer must write the job the docs show.
func TestMerudPlistIsDeployCopy(t *testing.T) {
	deploy := readFile(t, filepath.Join("..", "..", "deploy", "launchd", "com.meru.merud.plist"))
	if deploy != merudPlistTemplate {
		t.Error("internal/installer/launchd/com.meru.merud.plist differs from deploy/launchd/; copy deploy's over it")
	}
}

// TestMerudPlist checks the paths the installer fills in.
func TestMerudPlist(t *testing.T) {
	plist := MerudPlist(Paths{Home: "/Users/dana"})
	for _, want := range []string{
		"<string>com.meru.merud</string>",
		"<string>/Users/dana/.local/bin/merud</string>",
		"<string>/Users/dana/.meru/merud.out.log</string>",
		"<string>/Users/dana/.meru/merud.err.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(plist, "__HOME__") {
		t.Errorf("plist still holds a placeholder:\n%s", plist)
	}
}

// fakeMerud serves the socket at a short path with h, and returns the
// path. A Unix socket's path must stay under about 100 bytes, which
// t.TempDir can exceed on macOS.
func fakeMerud(t *testing.T, h rpc.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := rpc.Listen(ctx, path)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rpc.Serve(ctx, ln, h, slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() { cancel(); <-done })
	return path
}

// TestPingAndFollowScan runs Ping, then follows a scan that takes three
// status calls to finish.
func TestPingAndFollowScan(t *testing.T) {
	var calls atomic.Int32
	socket := fakeMerud(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		switch req.Op {
		case rpc.OpPing:
			return nil
		case rpc.OpIndexStatus:
			n := int(calls.Add(1))
			st := rpc.IndexStatus{Folders: []string{"~/Documents"}, Documents: n * 10, Scanning: n < 3}
			if n >= 3 {
				st.LastScan = &rpc.IndexReport{Skipped: 4, Failed: 1}
			}
			return emit(rpc.Event{Type: rpc.EventStatus, Status: &st})
		}
		return nil
	})
	if err := Ping(context.Background(), socket); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	say, lines := collect()
	msg, err := FollowScan(context.Background(), socket, time.Millisecond, say)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "merud scanned your folders: 30 files in the index, 4 skipped, 1 it couldn't read." {
		t.Errorf("result = %q", msg)
	}
	if len(*lines) != 3 || !strings.Contains((*lines)[0], "10 files") {
		t.Errorf("news = %q", *lines)
	}
}

// TestFollowScanNoFolders checks the answer when no folder is listed.
func TestFollowScanNoFolders(t *testing.T) {
	socket := fakeMerud(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		return emit(rpc.Event{Type: rpc.EventStatus, Status: &rpc.IndexStatus{}})
	})
	msg, err := FollowScan(context.Background(), socket, time.Millisecond, func(string) {})
	if err != nil || !strings.Contains(msg, "No folders") {
		t.Errorf("FollowScan = %q, %v", msg, err)
	}
}

// TestPingNoMerud checks that Ping fails when nothing listens.
func TestPingNoMerud(t *testing.T) {
	if err := Ping(context.Background(), filepath.Join(t.TempDir(), "none.sock")); err == nil {
		t.Error("Ping passed with no merud")
	}
}

// TestStartMerudNeedsProgram checks that the step refuses before it writes
// a launchd job when merud isn't installed.
func TestStartMerudNeedsProgram(t *testing.T) {
	p := tempHome(t)
	r := &fakeRunner{}
	if _, err := StartMerud(context.Background(), r.run, p, func(string) {}); err == nil {
		t.Fatal("StartMerud passed with no merud")
	}
	if calls := r.called(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
	if _, err := os.Stat(p.LaunchAgents()); err == nil {
		t.Error("wrote a launchd job with no merud")
	}
}
