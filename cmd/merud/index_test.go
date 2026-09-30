// This file runs merud over a fake engine with [index] folders set: the
// startup scan runs in the background while questions get answers, a new
// embedding model makes merud embed every file again, and the index ops
// answer `meru index`.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// daemon is one merud started by startDaemon.
type daemon struct {
	sock string
	stop func() // cancels merud and waits for run to return nil
}

// startDaemon runs merud with a config.toml holding cfgBody, in dir, over
// eng, and waits until it answers a ping. The test's cleanup stops it if
// the test didn't.
func startDaemon(t *testing.T, dir, cfgBody string, eng *fakeEngine) daemon {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "d.sock")
	build := func(config.Config, *slog.Logger) (engine.Engine, error) { return eng, nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-config", cfgPath, "-socket", sock}, io.Discard, build) }()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run = %v, want nil after shutdown", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("run didn't return after cancel")
		}
	}
	t.Cleanup(stop)

	// merud answers a ping as soon as its socket opens, before it has
	// checked Ollama; the status op waits until merud is ready.
	waitUntil(t, "merud is ready", func() bool {
		up := false
		for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpIndexStatus}, nil) {
			up = up || (err == nil && ev.Type == rpc.EventStatus)
		}
		return up
	})
	return daemon{sock: sock, stop: stop}
}

// waitUntil polls cond every 20 ms for up to 10 seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// call sends req and returns every event of the reply.
func call(t *testing.T, sock string, req rpc.Request) []rpc.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var evs []rpc.Event
	for ev, err := range rpc.Do(ctx, sock, req, nil) {
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// status asks merud for the index status.
func status(t *testing.T, sock string) rpc.IndexStatus {
	t.Helper()
	for _, ev := range call(t, sock, rpc.Request{Op: rpc.OpIndexStatus}) {
		if ev.Type == rpc.EventStatus && ev.Status != nil {
			return *ev.Status
		}
	}
	t.Fatal("no status event")
	return rpc.IndexStatus{}
}

// indexConfig is a config.toml that indexes folder. The watcher is off, so
// only the scans these tests start change the index; test/e2e covers it.
func indexConfig(folder string) string {
	return fmt.Sprintf("[index]\nfolders = [%q]\nwatch = false\n", folder)
}

// notesDir makes a notes folder under dir with files, symlinks resolved so
// paths match what the indexer stores.
func notesDir(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	notes := filepath.Join(dir, "notes")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(notes, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	real, err := filepath.EvalSymlinks(notes)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestQuestionsDuringStartupScan(t *testing.T) {
	dir := shortDir(t)
	notes := notesDir(t, dir, map[string]string{"slow.md": "# Slow\n\n" + holdMarker + " garden plan"})
	eng := &fakeEngine{version: "0.13.0", hold: make(chan struct{})}
	d := startDaemon(t, dir, indexConfig(notes), eng)

	// The scan starts beside the server and then waits inside Embed, so
	// it can't finish yet.
	waitUntil(t, "the scan starts", func() bool { return status(t, d.sock).Scanning })
	if st := status(t, d.sock); st.Documents != 0 {
		t.Errorf("status during the scan = %+v; want nothing indexed yet", st)
	}
	// A question still gets its answer. The fake router is unsure, so the
	// turn takes the fallback route and searches the empty index.
	var answer strings.Builder
	for _, ev := range call(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "when do I sow the tomatoes?"}) {
		if ev.Type == rpc.EventToken {
			answer.WriteString(ev.Text)
		}
		if ev.Type == rpc.EventError {
			t.Fatalf("ask during the scan: %s", ev.Error)
		}
	}
	if answer.String() != "pong" {
		t.Errorf("answer = %q, want pong", answer.String())
	}

	close(eng.hold)
	waitUntil(t, "the scan ends", func() bool {
		st := status(t, d.sock)
		return !st.Scanning && st.LastScan != nil
	})
	st := status(t, d.sock)
	if st.Documents != 1 || st.Chunks == 0 || st.Vectors != st.Chunks || st.LastScan.Indexed != 1 || st.LastError != "" {
		t.Errorf("status after the scan = %+v, last scan %+v", st, st.LastScan)
	}
	if len(st.Folders) != 1 || st.Folders[0] != notes {
		t.Errorf("folders = %v, want [%s]", st.Folders, notes)
	}

	// Now search finds the note, and the turn lists it as a source.
	var sources []rpc.Citation
	for _, ev := range call(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "garden plan"}) {
		if ev.Type == rpc.EventSources {
			sources = ev.Sources
		}
	}
	if len(sources) != 1 || !strings.HasSuffix(sources[0].Path, "slow.md") || sources[0].N != 1 {
		t.Errorf("sources = %+v, want slow.md as [1]", sources)
	}
}

func TestReembedAfterEmbedModelChange(t *testing.T) {
	dir := shortDir(t)
	notes := notesDir(t, dir, map[string]string{"a.md": "# A\n\nalpha", "b.md": "# B\n\nbeta", "empty.md": ""})

	d := startDaemon(t, dir, indexConfig(notes), &fakeEngine{version: "0.13.0", dims: 2})
	waitUntil(t, "the first scan ends", func() bool { return status(t, d.sock).LastScan != nil })
	first := status(t, d.sock)
	if first.Documents != 3 || first.Chunks != 2 || first.Vectors != 2 {
		t.Fatalf("first status = %+v; want 3 files (one empty), 2 chunks, 2 vectors", first)
	}
	d.stop()

	// The same files, but the embedding model now gives 3 numbers per
	// vector: the store drops the old vectors, and the startup scan must
	// embed every file again even though none changed.
	d = startDaemon(t, dir, indexConfig(notes), &fakeEngine{version: "0.13.0", dims: 3})
	waitUntil(t, "the second scan ends", func() bool { return status(t, d.sock).LastScan != nil })
	st := status(t, d.sock)
	if st.Vectors != st.Chunks || st.Chunks != 2 || st.LastScan.Indexed != 3 || st.LastScan.Unchanged != 0 {
		t.Errorf("status after the model change = %+v, last scan %+v; want every file embedded again", st, st.LastScan)
	}
}

func TestIndexOps(t *testing.T) {
	dir := shortDir(t)
	notes := notesDir(t, dir, map[string]string{"a.md": "# A\n\nalpha"})
	d := startDaemon(t, dir, indexConfig(notes), &fakeEngine{version: "0.13.0"})
	waitUntil(t, "the first scan ends", func() bool { return status(t, d.sock).LastScan != nil })

	if err := os.WriteFile(filepath.Join(notes, "b.md"), []byte("# B\n\nbeta"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		path      string
		wantError string
		wantIndex int // files indexed, when there is no error
	}{
		{"rescan all", "", "", 1},
		{"one folder", notes, "", 0},
		{"outside", filepath.Join(dir, "elsewhere"), "isn't inside any [index] folder; add it to folders under [index] in " + filepath.Join(dir, "config.toml") + " and restart merud", 0},
		{"relative", "notes", "need an absolute path", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := call(t, d.sock, rpc.Request{Op: rpc.OpIndex, Path: tt.path})
			last := evs[len(evs)-1]
			if tt.wantError != "" {
				if last.Type != rpc.EventError || !strings.Contains(last.Error, tt.wantError) {
					t.Errorf("last event = %+v, want an error containing %q", last, tt.wantError)
				}
				return
			}
			var rep *rpc.IndexReport
			var progress []string
			for _, ev := range evs {
				switch ev.Type {
				case rpc.EventReport:
					rep = ev.Report
				case rpc.EventProgress:
					progress = append(progress, ev.Text)
				}
			}
			if last.Type != rpc.EventDone || rep == nil || rep.Indexed != tt.wantIndex || len(progress) == 0 {
				t.Errorf("events = %+v; want progress, a report with %d indexed, and done", evs, tt.wantIndex)
			}
		})
	}
	if st := status(t, d.sock); st.Documents != 2 {
		t.Errorf("documents = %d, want 2", st.Documents)
	}
}

func TestIndexOpsWithoutFolders(t *testing.T) {
	dir := shortDir(t)
	d := startDaemon(t, dir, "", &fakeEngine{version: "0.13.0"})
	evs := call(t, d.sock, rpc.Request{Op: rpc.OpIndex})
	if last := evs[len(evs)-1]; last.Type != rpc.EventError || !strings.Contains(last.Error, "no folders to index") {
		t.Errorf("index without folders = %+v, want an error naming the fix", last)
	}
	st := status(t, d.sock)
	if len(st.Folders) != 0 || st.Documents != 0 || st.Scanning || st.LastScan != nil {
		t.Errorf("status = %+v, want an empty index with no scan", st)
	}
}
