// This file tests the Sources list under an answer and `meru index` against
// an in-process rpc server that plays merud.

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// withSources returns a handler that sends srcs and then streams answer.
func withSources(answer string, srcs []rpc.Citation) rpc.Handler {
	return func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		emit(rpc.Event{Type: rpc.EventSession, Session: "s"})
		emit(rpc.Event{Type: rpc.EventRoute, Route: "search"})
		if srcs != nil {
			emit(rpc.Event{Type: rpc.EventSources, Sources: srcs})
		}
		return emit(rpc.Event{Type: rpc.EventToken, Text: answer})
	}
}

func TestAskSources(t *testing.T) {
	srcs := []rpc.Citation{
		{N: 1, Path: "~/notes/garden.md", Heading: "Budget", StartLine: 1, EndLine: 4, Score: 0.03},
		{N: 2, Path: "~/notes/trip.md", StartLine: 2, EndLine: 2, Score: 0.01},
	}
	tests := []struct {
		name    string
		answer  string
		srcs    []rpc.Citation
		wantOut string
	}{
		{"cited", "It is 4,200 dollars [1].", srcs,
			"It is 4,200 dollars [1].\n\nSources:\n[1] ~/notes/garden.md, \"Budget\", lines 1–4\n"},
		{"none cited lists all", "It is 4,200 dollars.", srcs,
			"It is 4,200 dollars.\n\nSources:\n[1] ~/notes/garden.md, \"Budget\", lines 1–4\n[2] ~/notes/trip.md, line 2\n"},
		{"no sources", "Paris.", nil, "Paris.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := startServer(t, withSources(tt.answer, tt.srcs))
			var out, errOut bytes.Buffer
			if code := run(context.Background(), []string{"-socket", sock, "what is the budget?"}, &out, &errOut); code != 0 {
				t.Fatalf("exit code %d, stderr %q", code, errOut.String())
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q\nwant %q", out.String(), tt.wantOut)
			}
		})
	}
}

// fakeIndexer answers the index ops the way merud does, and remembers the
// last request.
type fakeIndexer struct {
	req rpc.Request
}

func (f *fakeIndexer) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	f.req = req
	switch req.Op {
	case rpc.OpIndexStatus:
		return emit(rpc.Event{Type: rpc.EventStatus, Status: &rpc.IndexStatus{
			Folders: []string{"~/notes"}, Documents: 3, Chunks: 7, Vectors: 5, DBBytes: 8_808_038, Scanning: true,
			LastScan:   &rpc.IndexReport{Seen: 3, Indexed: 3, Chunks: 7, Skipped: 1, DurationMillis: 1200},
			LastScanAt: "2026-09-23T10:15:00Z",
		}})
	case rpc.OpIndex:
		if strings.HasSuffix(req.Path, "outside") {
			return errors.New(req.Path + " isn't inside any [index] folder; add it to folders under [index] in /h/config.toml and restart merud")
		}
		emit(rpc.Event{Type: rpc.EventProgress, Text: "scanning 1 folder"})
		return emit(rpc.Event{Type: rpc.EventReport, Report: &rpc.IndexReport{Seen: 3, Indexed: 1, Unchanged: 2, Chunks: 2, DurationMillis: 40}})
	}
	return errors.New("unexpected op")
}

func TestIndexCommand(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantPath   string
		wantOut    string
		wantErrOut string
	}{
		{"rescan", []string{"index"}, 0, "", "1 file indexed (2 chunks), 2 unchanged, 0 removed, 0 failed, 0 skipped, in 40ms\n", "scanning 1 folder"},
		{"one folder", []string{"index", "/home/u/notes"}, 0, "/home/u/notes", "1 file indexed", ""},
		{"relative folder", []string{"index", "notes"}, 0, filepath.Join(cwd, "notes"), "1 file indexed", ""},
		{"outside", []string{"index", "/tmp/outside"}, 1, "/tmp/outside", "", "restart merud"},
		{"status", []string{"index", "-status"}, 0, "", "Folders:    ~/notes\n" +
			"Index:      3 files, 7 chunks, 5 vectors\n" +
			"            2 chunks still need a vector; keyword search covers them until then\n" +
			"On disk:    8.4 MB (meru.db and its -wal and -shm files)\n" +
			"Scanning:   yes\n" +
			"Last scan:  2026-09-23T10:15:00Z, 3 files indexed (7 chunks), 0 unchanged, 0 removed, 0 failed, 1 skipped, in 1.2s\n", ""},
		{"status with a folder", []string{"index", "-status", "x"}, 1, "", "", "usage"},
		{"two folders", []string{"index", "a", "b"}, 1, "", "", "one folder at a time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeIndexer{}
			sock := startServer(t, f.handle)
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if f.req.Path != tt.wantPath {
				t.Errorf("request path = %q, want %q", f.req.Path, tt.wantPath)
			}
			if !strings.HasPrefix(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to start with %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErrOut) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErrOut)
			}
		})
	}
}
