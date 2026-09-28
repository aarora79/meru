// This file tests the Ollama step against a fake Ollama: the pull stream
// parser, the pull request, the model list and the version check.

package installer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// pullStream is what Ollama's /api/pull sends for a model with two
// layers, one line per object, cut down from a real pull.
const pullStream = `{"status":"pulling manifest"}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":0}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":500}
{"status":"pulling bbb","digest":"sha256:bbb","total":3000,"completed":1000}
{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":1000}
{"status":"pulling bbb","digest":"sha256:bbb","total":3000,"completed":3000}
{"status":"verifying sha256 digest"}
{"status":"writing manifest"}
{"status":"success"}
`

// TestReadPull checks the progress over all layers after each line, and
// the failures: an error line, a line that isn't JSON, and a stream that
// ends early.
func TestReadPull(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantErr string
		want    []Pull // the progress after each line; nil skips the check
	}{
		{
			name:   "two layers",
			stream: pullStream,
			want: []Pull{
				{"m", "pulling manifest", 0, 0},
				{"m", "pulling aaa", 0, 1000},
				{"m", "pulling aaa", 500, 1000},
				{"m", "pulling bbb", 1500, 4000},
				{"m", "pulling aaa", 2000, 4000},
				{"m", "pulling bbb", 4000, 4000},
				{"m", "verifying sha256 digest", 4000, 4000},
				{"m", "writing manifest", 4000, 4000},
				{"m", "success", 4000, 4000},
			},
		},
		{name: "an error line", stream: "{\"status\":\"pulling manifest\"}\n{\"error\":\"pull model manifest: file does not exist\"}\n", wantErr: "file does not exist"},
		{name: "not JSON", stream: "<html>\n", wantErr: "isn't JSON"},
		{name: "ends before success", stream: "{\"status\":\"pulling manifest\"}\n", wantErr: "before it said success"},
		{name: "blank lines are fine", stream: "\n{\"status\":\"success\"}\n\n", want: []Pull{{"m", "success", 0, 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []Pull
			err := readPull(strings.NewReader(tt.stream), "m", func(p Pull) { got = append(got, p) })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one holding %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d reports, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("report %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// fakeOllama serves /api/version, /api/tags and /api/pull, and records
// the models pulled.
func fakeOllama(t *testing.T, have []string, pulled *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"version":"0.34.0"}`)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		var body struct {
			Models []map[string]string `json:"models"`
		}
		for _, h := range have {
			body.Models = append(body.Models, map[string]string{"name": h})
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Stream {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Model == "no-such-model" {
			io.WriteString(w, "{\"status\":\"pulling manifest\"}\n{\"error\":\"pull model manifest: file does not exist\"}\n")
			return
		}
		*pulled = append(*pulled, req.Model)
		io.WriteString(w, pullStream)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestPullModel pulls one model from the fake Ollama and checks the last
// report, then pulls a model it doesn't know.
func TestPullModel(t *testing.T) {
	var pulled []string
	srv := fakeOllama(t, nil, &pulled)
	var last Pull
	if err := PullModel(context.Background(), srv.Client(), srv.URL, "nomic-embed-text", func(p Pull) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Status != "success" || last.Completed != 4000 || last.Total != 4000 {
		t.Errorf("last report = %+v", last)
	}
	if len(pulled) != 1 || pulled[0] != "nomic-embed-text" {
		t.Errorf("pulled = %v", pulled)
	}
	if last.Text() != "nomic-embed-text: 4 KB of 4 KB" {
		t.Errorf("Text() = %q", last.Text())
	}
	err := PullModel(context.Background(), srv.Client(), srv.URL, "no-such-model", func(Pull) {})
	if err == nil || !strings.Contains(err.Error(), "file does not exist") {
		t.Errorf("pull of an unknown model = %v", err)
	}
}

// TestOllamaModelsAndVersion checks the model list, the tag rule and the
// version check, and that a dead Ollama fails.
func TestOllamaModelsAndVersion(t *testing.T) {
	srv := fakeOllama(t, []string{"nomic-embed-text:latest", "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M"}, new([]string))
	have, err := OllamaModels(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"nomic-embed-text":                      true,
		"nomic-embed-text:latest":               true,
		"hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M": true,
		"hf.co/openbmb/MiniCPM5-2B-GGUF":        false,
		"qwen3.6:35b-a3b-mxfp8":                 false,
	} {
		if HasModel(have, name) != want {
			t.Errorf("HasModel(%q) = %v, want %v", name, !want, want)
		}
	}
	if v, err := OllamaVersion(context.Background(), srv.Client(), srv.URL); err != nil || v != "0.34.0" {
		t.Errorf("OllamaVersion = %q, %v", v, err)
	}
	if _, err := OllamaVersion(context.Background(), deadClient(), "http://127.0.0.1:11434"); err == nil {
		t.Error("OllamaVersion passed with nothing listening")
	}
}

// TestStartOllamaRunning checks that a running Ollama is left alone: no
// program runs.
func TestStartOllamaRunning(t *testing.T) {
	srv := fakeOllama(t, nil, new([]string))
	r := &fakeRunner{}
	v, err := StartOllama(context.Background(), r.run, srv.Client(), t.TempDir(), srv.URL, func(string) {})
	if err != nil || v != "0.34.0" {
		t.Fatalf("StartOllama = %q, %v", v, err)
	}
	if calls := r.called(); len(calls) != 0 {
		t.Errorf("ran %v for an Ollama that runs", calls)
	}
}

// TestInstallOllamaWithBrew checks the Homebrew path's command, and that a
// failing brew stops the step instead of falling back to the download.
func TestInstallOllamaWithBrew(t *testing.T) {
	r := &fakeRunner{}
	msg, err := InstallOllama(context.Background(), r.run, deadClient(), t.TempDir(), t.TempDir(), func(string) {})
	if err != nil || !strings.Contains(msg, "Homebrew") {
		t.Fatalf("InstallOllama = %q, %v", msg, err)
	}
	if got := strings.Join(r.called(), "|"); got != "brew install ollama" {
		t.Errorf("calls = %q", got)
	}

	broken := &fakeRunner{answer: func(string, []string, func(string)) (string, error) { return "", errors.New("brew: no network") }}
	if _, err := InstallOllama(context.Background(), broken.run, deadClient(), t.TempDir(), t.TempDir(), func(string) {}); err == nil {
		t.Error("a failing brew passed")
	}
}

// TestInstallOllamaDownload checks the path for a Mac with no Homebrew:
// the zip comes from the fake server, ditto unpacks it, codesign checks
// it, and a bad signature removes the app.
func TestInstallOllamaDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download/Ollama-darwin.zip" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, "PK fake zip")
	}))
	defer srv.Close()

	for _, badSignature := range []bool{false, true} {
		apps := t.TempDir()
		r := &fakeRunner{answer: func(program string, args []string, _ func(string)) (string, error) {
			switch program {
			case "brew":
				return "", ErrMissing
			case "ditto":
				return "", os.MkdirAll(apps+"/Ollama.app", 0o755)
			case "codesign":
				if badSignature {
					return "invalid signature", errors.New("exit status 1")
				}
			}
			return "", nil
		}}
		say, lines := collect()
		_, err := InstallOllama(context.Background(), r.run, clientTo(srv), t.TempDir(), apps, say)
		if badSignature {
			if err == nil {
				t.Error("a bad signature passed")
			}
			if _, statErr := os.Stat(apps + "/Ollama.app"); statErr == nil {
				t.Error("the app with a bad signature stayed")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		calls := strings.Join(r.called(), "|")
		if !strings.Contains(calls, "ditto -x -k ") || !strings.Contains(calls, "codesign --verify --deep --strict "+apps+"/Ollama.app") {
			t.Errorf("calls = %q", calls)
		}
		if !strings.Contains(strings.Join(*lines, "\n"), OllamaDownloadURL) {
			t.Errorf("the screen never showed the URL: %v", *lines)
		}
	}
}
