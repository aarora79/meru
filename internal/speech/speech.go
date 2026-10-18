// This file holds the three steps of turning a recording into text:
// CheckWAV, which reads the WAV header for the length; Transcribe, which
// posts the file to whisper-server's /inference; and Clean, which drops
// the markers Whisper writes for silence.

package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// MaxSeconds is the longest recording Meru takes: Meru.app stops
// recording at 60 seconds, and 60 seconds of 16 kHz mono 16-bit audio is
// 1.9 MB, which fits the socket's 4 MiB line once base64 grows it to 2.6.
const MaxSeconds = 60

// slack is how far past MaxSeconds a file may run, for a recorder that
// stops a few samples late.
const slack = 0.5

// ErrNotWAV means the bytes aren't a WAV file Meru can read: no RIFF and
// WAVE header, no fmt or data chunk, or a format that isn't plain PCM or
// float.
var ErrNotWAV = errors.New("the recording isn't a WAV file")

// ErrTooLong means the recording runs longer than MaxSeconds.
var ErrTooLong = fmt.Errorf("the recording is longer than %d seconds", MaxSeconds)

// CheckWAV reads the header of wav and returns how many seconds of sound
// it holds. It fails with ErrNotWAV for anything that isn't a PCM or
// float WAV file, and with ErrTooLong past MaxSeconds.
//
// A WAV file is a RIFF file: "RIFF", a size, "WAVE", then chunks, each a
// four-letter ID, a little-endian 32-bit size and its bytes. The "fmt "
// chunk says how many bytes a second takes; the "data" chunk holds the
// sound. Its size over the bytes per second is the length.
func CheckWAV(wav []byte) (float64, error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, ErrNotWAV
	}
	var byteRate uint32
	var dataSize uint32
	gotFmt, gotData := false, false
	for at := 12; at+8 <= len(wav); {
		id := string(wav[at : at+4])
		// binary.LittleEndian reads the four size bytes, lowest first, as
		// RIFF writes them.
		size := binary.LittleEndian.Uint32(wav[at+4 : at+8])
		body := at + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(wav) {
				return 0, ErrNotWAV
			}
			// 1 is plain PCM, 3 is float, and 0xFFFE is "extensible",
			// which whisper-server reads as well.
			format := binary.LittleEndian.Uint16(wav[body : body+2])
			if format != 1 && format != 3 && format != 0xFFFE {
				return 0, fmt.Errorf("%w: format %d isn't PCM", ErrNotWAV, format)
			}
			byteRate = binary.LittleEndian.Uint32(wav[body+8 : body+12])
			gotFmt = true
		case "data":
			dataSize = size
			gotData = true
		}
		if gotFmt && gotData {
			break
		}
		// A chunk of odd size has one byte of padding after it.
		next := body + int(size) + int(size%2)
		if next <= at {
			return 0, ErrNotWAV
		}
		at = next
	}
	if !gotFmt || !gotData || byteRate == 0 {
		return 0, ErrNotWAV
	}
	seconds := float64(dataSize) / float64(byteRate)
	if seconds > MaxSeconds+slack {
		return seconds, ErrTooLong
	}
	return seconds, nil
}

// Transcribe posts wav to whisper-server's /inference under base, the URL
// the connector's Service gave with its token path, and returns the text
// Clean leaves. temperature 0 makes Whisper pick its likeliest words, so
// the same recording gives the same text. client must refuse redirects
// off this machine; merud passes one that does. It fails when the server
// answers with anything but 200 and a JSON text.
func Transcribe(ctx context.Context, client *http.Client, base string, wav []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "recording.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	for k, v := range map[string]string{"temperature": "0.0", "response_format": "json"} {
		if err := w.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("speech to text: %w", err)
	}
	// defer closes the body when Transcribe returns.
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("speech to text: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("speech to text: the server answered %s: %s", resp.Status, firstLine(data))
	}
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("speech to text: the server didn't answer JSON: %s", firstLine(data))
	}
	if out.Error != "" {
		return "", fmt.Errorf("speech to text: %s", out.Error)
	}
	return Clean(out.Text), nil
}

// marker matches what Whisper writes in square brackets in place of
// words, such as "[BLANK_AUDIO]" for silence.
var marker = regexp.MustCompile(`\[[^\]]*\]`)

// Clean returns text with Whisper's markers taken out, its lines joined
// and its spaces trimmed, or "" when nothing is left that holds a letter
// or a digit: on silence base.en writes "[BLANK_AUDIO]" and
// large-v3-turbo writes ".".
func Clean(text string) string {
	text = marker.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(text), " ")
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return text
}

// firstLine returns the first line of b, at most 200 bytes, for an error.
func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
