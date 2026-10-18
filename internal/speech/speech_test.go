// This file tests the speech package: WAV headers of every shape Meru may
// get, the request Transcribe sends to a fake whisper-server, and the
// cleaning of Whisper's markers.

package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wavFile builds a WAV file: chunks before fmt (such as a LIST chunk
// macOS writes), then fmt with format, channels, rate and bits, then a
// data chunk of seconds of silence.
func wavFile(format uint16, channels uint16, rate uint32, bits uint16, seconds float64, before ...[]byte) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(0)) // the size; CheckWAV doesn't need it
	b.WriteString("WAVE")
	for _, c := range before {
		b.Write(c)
	}
	byteRate := rate * uint32(channels) * uint32(bits/8)
	b.WriteString("fmt ")
	le(uint32(16))
	le(format)
	le(channels)
	le(rate)
	le(byteRate)
	le(channels * bits / 8)
	le(bits)
	data := int(float64(byteRate) * seconds)
	b.WriteString("data")
	le(uint32(data))
	b.Write(make([]byte, data))
	return b.Bytes()
}

// chunk builds one RIFF chunk with id and body, padded to an even length.
func chunk(id string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString(id)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(body)))
	b.Write(body)
	if len(body)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

// TestCheckWAV checks the length of each kind of WAV file, and each
// refusal.
func TestCheckWAV(t *testing.T) {
	tests := []struct {
		name    string
		wav     []byte
		seconds float64
		err     error
	}{
		{"16 kHz mono, as Meru.app records", wavFile(1, 1, 16000, 16, 3), 3, nil},
		{"48 kHz stereo float", wavFile(3, 2, 48000, 32, 2.5), 2.5, nil},
		{"a LIST chunk of odd size first", wavFile(1, 1, 16000, 16, 1, chunk("LIST", []byte("abc"))), 1, nil},
		{"60 seconds", wavFile(1, 1, 16000, 16, 60), 60, nil},
		{"61 seconds", wavFile(1, 1, 16000, 16, 61), 61, ErrTooLong},
		{"ADPCM", wavFile(2, 1, 16000, 16, 1), 0, ErrNotWAV},
		{"not RIFF", []byte("ID3 an mp3 file, say"), 0, ErrNotWAV},
		{"empty", nil, 0, ErrNotWAV},
		{"no data chunk", []byte("RIFF\x00\x00\x00\x00WAVE"), 0, ErrNotWAV},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CheckWAV(tt.wav)
			if !errors.Is(err, tt.err) {
				t.Fatalf("CheckWAV = %v, want %v", err, tt.err)
			}
			if tt.err == nil && (got < tt.seconds-0.01 || got > tt.seconds+0.01) {
				t.Errorf("seconds = %v, want %v", got, tt.seconds)
			}
		})
	}
}

// TestTranscribe checks the request whisper-server gets, under the base
// path, with the file and the fields, and the text that comes back.
func TestTranscribe(t *testing.T) {
	wav := wavFile(1, 1, 16000, 16, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/tok/inference" {
			http.NotFound(w, r)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got, _ := io.ReadAll(f)
		if !bytes.Equal(got, wav) || r.FormValue("temperature") != "0.0" || r.FormValue("response_format") != "json" {
			http.Error(w, "wrong form", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"text":" Remind me to call the dentist.\n"}`)
	}))
	defer srv.Close()
	text, err := Transcribe(context.Background(), srv.Client(), srv.URL+"/tok", wav)
	if err != nil || text != "Remind me to call the dentist." {
		t.Errorf("Transcribe = %q, %v", text, err)
	}
	if _, err := Transcribe(context.Background(), srv.Client(), srv.URL+"/wrong", wav); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Transcribe at the wrong path = %v, want the 404", err)
	}
}

// TestTranscribeErrors checks a server error in the JSON and an answer
// that isn't JSON.
func TestTranscribeErrors(t *testing.T) {
	for _, body := range []string{`{"error":"failed to read WAV file"}`, `<html>busy</html>`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		_, err := Transcribe(context.Background(), srv.Client(), srv.URL, wavFile(1, 1, 16000, 16, 1))
		srv.Close()
		if err == nil {
			t.Errorf("Transcribe with %q worked", body)
		}
	}
}

// TestClean checks the markers and the empty answers Whisper gives for
// silence and noise.
func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		{" [BLANK_AUDIO]\n", ""},
		{" .\n", ""},
		{"", ""},
		{" Hello there.\n And again. ", "Hello there. And again."},
		{"[MUSIC] Play the next song", "Play the next song"},
		{"Call at 3:30.", "Call at 3:30."},
	}
	for _, tt := range tests {
		if got := Clean(tt.in); got != tt.want {
			t.Errorf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
