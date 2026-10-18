//go:build integration

// This file holds the test that runs the real whisper connector: it
// downloads whisper-server from Meru's release and ggml-base.en.bin from
// Hugging Face into a temporary Meru home, starts the server through the
// Service supervisor, and has it write down a clip that macOS's say
// spoke. It runs only with the integration build tag, on a Mac with Apple
// silicon:
//
//	go test -tags integration -run TestIntegrationWhisper ./internal/connectors/
//
// CI never runs it, since it needs the internet and about 160 MB.

package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// TestIntegrationWhisper installs the real whisper connector, uses it,
// and checks the transcript and the token path.
func TestIntegrationWhisper(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("whisper-server has a build for Apple silicon only")
	}
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m := ms[slices.IndexFunc(ms, func(m Manifest) bool { return m.ID == "whisper" })]
	s, err := NewService(m, NewInstaller(filepath.Join(t.TempDir(), ".meru"), t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Configure(config.Connector{"enabled": true}, nil)
	deadline := time.Now().Add(10 * time.Minute)
	for st := s.Status(); st.State != StateOK; st = s.Status() {
		if st.State == StateFailed || time.Now().After(deadline) {
			t.Fatalf("install: %s %s", st.State, st.Sentence)
		}
		time.Sleep(time.Second)
	}

	clip := filepath.Join(t.TempDir(), "clip.wav")
	say := exec.Command("/usr/bin/say", "-o", clip, "--data-format=LEI16@16000", "What did I write in my notes about the garden plan last spring?")
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base, done, err := s.Use(ctx)
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	defer done()
	t.Logf("base %s", base)

	text, code := transcribeFile(t, base+"/inference", clip)
	if code != http.StatusOK || !strings.Contains(strings.ToLower(text), "garden plan last spring") {
		t.Errorf("inference = %d %q", code, text)
	}
	// Without the token, every route is closed, /load included.
	root := strings.TrimSuffix(m.Launch.URL, "/")
	if _, code := transcribeFile(t, root+"/inference", clip); code != http.StatusNotFound {
		t.Errorf("inference without the token = %d, want 404", code)
	}
	resp, err := http.Post(root+"/load", "text/plain", nil) // #nosec G107 -- the connector's own loopback URL
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("load without the token = %d, want 404", resp.StatusCode)
		}
	}
}

// transcribeFile posts the WAV file at path to url as whisper-server's
// form wants it, and returns the text and the status code.
func transcribeFile(t *testing.T, url, path string) (string, int) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- the test's own clip
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "clip.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = w.WriteField("temperature", "0.0")
	_ = w.WriteField("response_format", "json")
	_ = w.Close()
	resp, err := http.Post(url, w.FormDataContentType(), &body) // #nosec G107 -- the connector's own loopback URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return strings.TrimSpace(out.Text), resp.StatusCode
}
