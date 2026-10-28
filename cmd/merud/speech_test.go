// This file tests the transcribe op's refusals, which need no speech
// server: no recording, a file that isn't WAV, one that runs too long,
// and the connector off. The whole path, with the real whisper-server, is
// connectors.TestIntegrationWhisper and the live check in the pull
// request.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"runtime"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// silentWAV returns a 16 kHz mono 16-bit WAV file of seconds of silence.
func silentWAV(seconds int) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	data := seconds * 16000 * 2
	b.WriteString("RIFF")
	le(uint32(36 + data))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(1))
	le(uint32(16000))
	le(uint32(32000))
	le(uint16(2))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(data))
	b.Write(make([]byte, data))
	return b.Bytes()
}

func TestTranscribeRefusals(t *testing.T) {
	c := testConnectors(t)
	c.configure(config.Config{Profile: "lite"}, &secrets.Secrets{})
	s := &toolService{conns: c, log: slog.New(slog.DiscardHandler)}
	// whisper ships for Apple silicon and Linux on x86-64; elsewhere the
	// connector says it has no build before it says it is off.
	off := "isn't available on this computer yet"
	switch runtime.GOOS + "_" + runtime.GOARCH {
	case "darwin_arm64", "linux_amd64":
		off = "Speech to text is off"
	}
	tests := []struct {
		name  string
		audio *rpc.Audio
		want  string
	}{
		{"no recording", nil, "needs a recording"},
		{"not WAV", &rpc.Audio{WAV: []byte("ID3 an mp3")}, "isn't a WAV file"},
		{"too long", &rpc.Audio{WAV: silentWAV(61)}, "runs 61 seconds; Meru takes 60 at most"},
		{"connector off", &rpc.Audio{WAV: silentWAV(1)}, off},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []rpc.Event
			err := s.handleTranscribe(context.Background(), rpc.Request{Op: rpc.OpTranscribe, Audio: tt.audio},
				func(ev rpc.Event) error { got = append(got, ev); return nil })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("handleTranscribe = %v, want %q", err, tt.want)
			}
			if len(got) != 0 {
				t.Errorf("events = %+v, want none", got)
			}
		})
	}
}
