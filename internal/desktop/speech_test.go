// This file tests the microphone's Bridge methods against an in-process
// rpc server: the connector's state for each case the page handles, and
// the recording's trip to the transcribe op and back.

package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestSpeechStatus(t *testing.T) {
	tests := []struct {
		name       string
		connectors []rpc.ConnectorStatus
		want       SpeechState
	}{
		{"ready", []rpc.ConnectorStatus{{ID: "whisper", State: rpc.ConnectorOK, Sentence: "Speech to text is ready."}},
			SpeechState{Ready: true, Sentence: "Speech to text is ready."}},
		{"off", []rpc.ConnectorStatus{{ID: "whisper", State: rpc.ConnectorOff, Sentence: "Speech to text is off. Turn it on in Settings, Connections."}},
			SpeechState{Sentence: "Speech to text is off. Turn it on in Settings, Connections."}},
		{"installing", []rpc.ConnectorStatus{{ID: "whisper", State: rpc.ConnectorStarting, Sentence: "Meru is installing Speech to text 1.9.5."}},
			SpeechState{Sentence: "Meru is installing Speech to text 1.9.5."}},
		{"an older merud", []rpc.ConnectorStatus{{ID: "ollama", State: rpc.ConnectorOK}},
			SpeechState{Sentence: "This merud has no speech to text. Update Meru to use the microphone."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newBridge(startServer(t, func(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
				return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: tt.connectors})
			}))
			got, err := b.SpeechStatus(context.Background())
			if err != nil || got != tt.want {
				t.Errorf("SpeechStatus = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestTranscribe(t *testing.T) {
	wav := []byte("RIFF....WAVE the recording")
	b, _ := newBridge(startServer(t, func(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		if req.Op != rpc.OpTranscribe || req.Audio == nil || !bytes.Equal(req.Audio.WAV, wav) {
			return errors.New("the recording didn't arrive as sent")
		}
		return emit(rpc.Event{Type: rpc.EventTranscript, Text: "Call the dentist on Tuesday."})
	}))
	got, err := b.Transcribe(context.Background(), base64.StdEncoding.EncodeToString(wav))
	if err != nil || got != "Call the dentist on Tuesday." {
		t.Errorf("Transcribe = %q, %v", got, err)
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(make([]byte, maxRecording+1))} {
		if _, err := b.Transcribe(context.Background(), bad); err == nil {
			t.Errorf("Transcribe took %d bytes of %q…", len(bad), bad[:min(len(bad), 12)])
		}
	}
	off, _ := newBridge(startServer(t, func(context.Context, rpc.Request, func(rpc.Event) error, rpc.ApproveFunc) error {
		return errors.New("Speech to text is off. Turn it on in Settings, Connections.")
	}))
	if _, err := off.Transcribe(context.Background(), base64.StdEncoding.EncodeToString(wav)); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Errorf("Transcribe with the connector off = %v", err)
	}
}
