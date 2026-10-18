// This file tests `meru transcribe` against an in-process rpc server: the
// file goes as the request's audio, the words come back, a recording with
// no words says so, merud's refusal comes through, and bad usage and a
// file that is too large fail before anything is sent.

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

func TestTranscribeCommand(t *testing.T) {
	wav := []byte("RIFF....WAVE pretend audio")
	file := filepath.Join(t.TempDir(), "note.wav")
	if err := os.WriteFile(file, wav, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		handler rpc.Handler
		want    string
		err     string
	}{
		{"words", func(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
			if req.Op != rpc.OpTranscribe || req.Audio == nil || !bytes.Equal(req.Audio.WAV, wav) {
				t.Errorf("request = %+v, want the file as transcribe's audio", req)
			}
			return emit(rpc.Event{Type: rpc.EventTranscript, Text: "Call the dentist on Tuesday."})
		}, "Call the dentist on Tuesday.\n", ""},
		{"no words", func(_ context.Context, _ rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
			return emit(rpc.Event{Type: rpc.EventTranscript})
		}, "(Meru heard no words.)\n", ""},
		{"connector off", func(context.Context, rpc.Request, func(rpc.Event) error, rpc.ApproveFunc) error {
			return errors.New("Speech to text is off. Turn it on in Settings, Connections.")
		}, "", "Speech to text is off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := startServer(t, tt.handler)
			var out bytes.Buffer
			err := transcribeCmd(context.Background(), sock, []string{file}, &out)
			if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Fatalf("transcribeCmd = %v, want %q", err, tt.err)
			}
			if out.String() != tt.want {
				t.Errorf("output = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

// TestTranscribeCommandRefuses checks bad usage, a missing file and one
// that is too large, none of which reach merud.
func TestTranscribeCommandRefuses(t *testing.T) {
	big := filepath.Join(t.TempDir(), "film.wav")
	if err := os.WriteFile(big, make([]byte, maxRecording+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"a.wav", "b.wav"}, {filepath.Join(t.TempDir(), "none.wav")}, {big}} {
		if err := transcribeCmd(context.Background(), "/nonexistent.sock", args, &bytes.Buffer{}); err == nil {
			t.Errorf("transcribeCmd(%q) worked", args)
		}
	}
}
