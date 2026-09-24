// This file tests the fake the way Meru's code will use it: over HTTP, with
// the JSON shapes Ollama documents. The tests sit in package
// fakeollama_test, outside the package, so they can only use what a real
// caller can.

package fakeollama_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// chunk is the subset of a chat or generate response object the tests read.
type chunk struct {
	Message struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	EvalDuration    int64  `json:"eval_duration"`
	LoadDuration    int64  `json:"load_duration"`
	Error           string `json:"error"`
	LogProbs        []struct {
		Token       string  `json:"token"`
		LogProb     float64 `json:"logprob"`
		TopLogProbs []struct {
			Token   string  `json:"token"`
			LogProb float64 `json:"logprob"`
		} `json:"top_logprobs"`
	} `json:"logprobs"`
}

// text returns the answer text in c, whether it came from chat or generate.
func (c chunk) text() string { return c.Message.Content + c.Response }

// post sends body as JSON to url and returns the response. The caller
// closes the body.
func post(t *testing.T, ctx context.Context, url string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// readChunks reads an NDJSON body into chunks, one per line.
func readChunks(t *testing.T, r io.Reader) []chunk {
	t.Helper()
	var out []chunk
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var c chunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("decode line %q: %v", sc.Text(), err)
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return out
}

// TestChatAndGenerate runs one scripted call per row and checks the chunks
// that come back.
func TestChatAndGenerate(t *testing.T) {
	user := []map[string]string{{"role": "user", "content": "three words here"}}
	tests := []struct {
		name  string
		path  string
		body  map[string]any
		reply *fakeollama.Reply // nil means no scripted reply
		// check inspects the decoded chunks.
		check func(t *testing.T, chunks []chunk)
	}{
		{
			name:  "chat streams one word per chunk then a done line",
			path:  "/api/chat",
			body:  map[string]any{"model": "m", "messages": user},
			reply: &fakeollama.Reply{Text: "hi there you"},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks) != 4 {
					t.Fatalf("got %d chunks, want 3 words + done", len(chunks))
				}
				var text strings.Builder
				for _, c := range chunks[:3] {
					if c.Done {
						t.Errorf("chunk %+v has done set early", c)
					}
					text.WriteString(c.text())
				}
				if text.String() != "hi there you" {
					t.Errorf("text = %q", text.String())
				}
				last := chunks[3]
				if !last.Done || last.DoneReason != "stop" || last.PromptEvalCount != 3 || last.EvalCount != 3 {
					t.Errorf("last chunk = %+v, want done, stop, 3 prompt and 3 output tokens", last)
				}
				if last.EvalDuration != int64(30*time.Millisecond) {
					t.Errorf("eval_duration = %d, want 30ms", last.EvalDuration)
				}
			},
		},
		{
			name: "chat without stream sends one object",
			path: "/api/chat",
			body: map[string]any{"model": "m", "messages": user, "stream": false},
			reply: &fakeollama.Reply{
				Text: "one two", DoneReason: "length", PromptTokens: 40, OutputTokens: 7,
				LoadDuration: 2 * time.Second,
			},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks) != 1 {
					t.Fatalf("got %d objects, want 1", len(chunks))
				}
				c := chunks[0]
				if c.text() != "one two" || !c.Done || c.DoneReason != "length" ||
					c.PromptEvalCount != 40 || c.EvalCount != 7 || c.LoadDuration != int64(2*time.Second) {
					t.Errorf("object = %+v", c)
				}
			},
		},
		{
			name: "default reply when nothing is queued",
			path: "/api/chat",
			body: map[string]any{"model": "m", "messages": user, "stream": false},
			check: func(t *testing.T, chunks []chunk) {
				if got := chunks[0].text(); got != "Hello from fake Ollama." {
					t.Errorf("text = %q", got)
				}
			},
		},
		{
			name:  "made-up log probabilities with top alternatives",
			path:  "/api/chat",
			body:  map[string]any{"model": "m", "messages": user, "stream": false, "logprobs": true, "top_logprobs": 3},
			reply: &fakeollama.Reply{Text: "a b"},
			check: func(t *testing.T, chunks []chunk) {
				lps := chunks[0].LogProbs
				if len(lps) != 2 {
					t.Fatalf("got %d logprobs, want 2", len(lps))
				}
				if lps[0].Token != "a " || len(lps[0].TopLogProbs) != 3 || lps[0].TopLogProbs[0].Token != "a " {
					t.Errorf("logprobs[0] = %+v", lps[0])
				}
				if lps[0].LogProb >= 0 {
					t.Errorf("logprob %v should be negative", lps[0].LogProb)
				}
			},
		},
		{
			name: "scripted log probabilities stream one per chunk",
			path: "/api/chat",
			body: map[string]any{"model": "m", "messages": user, "logprobs": true, "top_logprobs": 2},
			reply: &fakeollama.Reply{LogProbs: []fakeollama.LogProb{{
				Token: "chat", LogProb: math.Log(0.7),
				Top: []fakeollama.TokenLogProb{{Token: "chat", LogProb: math.Log(0.7)}, {Token: "search", LogProb: math.Log(0.2)}, {Token: "tools", LogProb: math.Log(0.1)}},
			}}},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks) != 2 {
					t.Fatalf("got %d chunks, want 1 token + done", len(chunks))
				}
				lp := chunks[0].LogProbs
				if chunks[0].text() != "chat" || len(lp) != 1 || len(lp[0].TopLogProbs) != 2 || lp[0].TopLogProbs[1].Token != "search" {
					t.Errorf("chunk = %+v, want token chat with 2 alternatives (capped by top_logprobs)", chunks[0])
				}
			},
		},
		{
			name:  "no log probabilities unless asked",
			path:  "/api/chat",
			body:  map[string]any{"model": "m", "messages": user, "stream": false},
			reply: &fakeollama.Reply{LogProbs: []fakeollama.LogProb{{Token: "x", LogProb: -1}}},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks[0].LogProbs) != 0 {
					t.Errorf("got logprobs %+v without asking", chunks[0].LogProbs)
				}
			},
		},
		{
			name: "tool calls arrive before the done line",
			path: "/api/chat",
			body: map[string]any{"model": "m", "messages": user},
			reply: &fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{
				{Name: "search", Arguments: map[string]any{"q": "meru"}},
			}},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks) != 2 {
					t.Fatalf("got %d chunks, want tool call + done", len(chunks))
				}
				calls := chunks[0].Message.ToolCalls
				if len(calls) != 1 || calls[0].Function.Name != "search" || calls[0].Function.Arguments["q"] != "meru" {
					t.Errorf("tool calls = %+v", calls)
				}
			},
		},
		{
			name:  "generate streams into response with log probabilities",
			path:  "/api/generate",
			body:  map[string]any{"model": "m", "prompt": "route this", "logprobs": true},
			reply: &fakeollama.Reply{Chunks: []string{"sea", "rch"}},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks) != 3 || chunks[0].Response != "sea" || chunks[1].Response != "rch" {
					t.Fatalf("chunks = %+v", chunks)
				}
				if len(chunks[0].LogProbs) != 1 || chunks[0].LogProbs[0].Token != "sea" {
					t.Errorf("logprobs = %+v", chunks[0].LogProbs)
				}
				if last := chunks[2]; !last.Done || last.PromptEvalCount != 2 {
					t.Errorf("last = %+v, want done with 2 prompt tokens", last)
				}
			},
		},
		{
			name:  "stream error part way",
			path:  "/api/chat",
			body:  map[string]any{"model": "m", "messages": user},
			reply: &fakeollama.Reply{Text: "a b c", StreamError: "model crashed", FailAfter: 2},
			check: func(t *testing.T, chunks []chunk) {
				if len(chunks) != 3 || chunks[1].text() != "b " || chunks[2].Error != "model crashed" {
					t.Errorf("chunks = %+v, want two words then an error line", chunks)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeollama.Start(t, fakeollama.Config{})
			if tt.reply != nil {
				srv.Enqueue("", *tt.reply)
			}
			resp := post(t, t.Context(), srv.URL+tt.path, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			tt.check(t, readChunks(t, resp.Body))

			reqs := srv.Requests(tt.path)
			if len(reqs) != 1 || reqs[0].Model != "m" {
				t.Errorf("recorded %+v, want one request for model m", reqs)
			}
		})
	}
}

// TestErrors checks every way the fake can fail a request.
func TestErrors(t *testing.T) {
	chat := map[string]any{"model": "m", "messages": []any{}}
	tests := []struct {
		name       string
		setup      func(s *fakeollama.Server)
		method     string
		path       string
		body       any
		wantStatus int
		wantError  string
	}{
		{"scripted HTTP error", func(s *fakeollama.Server) {
			s.Enqueue("m", fakeollama.Reply{Status: 503, Error: "busy"})
		}, http.MethodPost, "/api/chat", chat, 503, "busy"},
		{"injected error on embed", func(s *fakeollama.Server) {
			s.FailNext("/api/embed", 500, "out of memory")
		}, http.MethodPost, "/api/embed", map[string]any{"model": "e", "input": "x"}, 500, "out of memory"},
		{"injected error on version", func(s *fakeollama.Server) {
			s.FailNext("/api/version", 502, "bad gateway")
		}, http.MethodGet, "/api/version", nil, 502, "bad gateway"},
		{"stream error without streaming is a 500", func(s *fakeollama.Server) {
			s.Enqueue("", fakeollama.Reply{StreamError: "boom"})
		}, http.MethodPost, "/api/generate", map[string]any{"model": "m", "stream": false}, 500, "boom"},
		{"unknown model", nil, http.MethodPost, "/api/chat", map[string]any{"model": "nope"}, 404, `model "nope" not found, try pulling it first`},
		{"missing model", nil, http.MethodPost, "/api/chat", map[string]any{}, 400, "model is required"},
		{"wrong method", nil, http.MethodGet, "/api/chat", nil, 405, "method not allowed"},
		{"unknown path", nil, http.MethodGet, "/api/tags", nil, 404, "404 page not found"},
		{"bad embed input", nil, http.MethodPost, "/api/embed", map[string]any{"model": "e", "input": 7}, 400, "input must be a string or a list of strings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeollama.Start(t, fakeollama.Config{Models: []string{"m", "e"}})
			if tt.setup != nil {
				tt.setup(srv)
			}
			var body io.Reader
			if tt.body != nil {
				data, _ := json.Marshal(tt.body)
				body = bytes.NewReader(data)
			}
			req, err := http.NewRequestWithContext(t.Context(), tt.method, srv.URL+tt.path, body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var got struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if resp.StatusCode != tt.wantStatus || got.Error != tt.wantError {
				t.Errorf("got %d %q, want %d %q", resp.StatusCode, got.Error, tt.wantStatus, tt.wantError)
			}
		})
	}
}

// TestQueueOrder checks that a reply queued for a model wins over one queued
// for any model, and that each reply is used once.
func TestQueueOrder(t *testing.T) {
	srv := fakeollama.Start(t, fakeollama.Config{DefaultText: "fallback"})
	srv.Enqueue("", fakeollama.Reply{Text: "any"})
	srv.Enqueue("fast", fakeollama.Reply{Text: "fast only"})

	calls := []struct {
		model string
		want  string
	}{
		{"fast", "fast only"},
		{"fast", "any"},
		{"main", "fallback"},
	}
	for _, c := range calls {
		resp := post(t, t.Context(), srv.URL+"/api/generate", map[string]any{"model": c.model, "stream": false})
		chunks := readChunks(t, resp.Body)
		resp.Body.Close()
		if got := chunks[0].text(); got != c.want {
			t.Errorf("model %s: text = %q, want %q", c.model, got, c.want)
		}
	}
}

// TestVersionAndPS checks the two GET endpoints, including a model joining
// /api/ps after use and leaving it when keep_alive is 0.
func TestVersionAndPS(t *testing.T) {
	srv := fakeollama.Start(t, fakeollama.Config{Version: "0.13.0", Loaded: []string{"embed"}})

	var version struct{ Version string }
	getJSON(t, srv.URL+"/api/version", &version)
	if version.Version != "0.13.0" {
		t.Errorf("version = %q", version.Version)
	}

	steps := []struct {
		name string
		body map[string]any
		want []string
	}{
		{"before any call", nil, []string{"embed"}},
		{"after a chat", map[string]any{"model": "main", "stream": false}, []string{"embed", "main"}},
		{"after keep_alive 0", map[string]any{"model": "main", "stream": false, "keep_alive": 0}, []string{"embed"}},
		{"after keep_alive \"0s\"", map[string]any{"model": "embed", "stream": false, "keep_alive": "0s"}, []string{}},
		{"after keep_alive -1", map[string]any{"model": "main", "stream": false, "keep_alive": -1}, []string{"main"}},
	}
	for _, step := range steps {
		if step.body != nil {
			resp := post(t, t.Context(), srv.URL+"/api/chat", step.body)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		var ps struct {
			Models []struct{ Name string } `json:"models"`
		}
		getJSON(t, srv.URL+"/api/ps", &ps)
		got := []string{}
		for _, m := range ps.Models {
			got = append(got, m.Name)
		}
		if strings.Join(got, ",") != strings.Join(step.want, ",") {
			t.Errorf("%s: loaded = %v, want %v", step.name, got, step.want)
		}
	}
}

// TestEmbed checks vector count, length, unit norm and that the same text
// always maps to the same vector.
func TestEmbed(t *testing.T) {
	srv := fakeollama.Start(t, fakeollama.Config{EmbedDims: 16})
	tests := []struct {
		name     string
		body     map[string]any
		wantVecs int
		wantDims int
	}{
		{"one string", map[string]any{"model": "e", "input": "hello"}, 1, 16},
		{"a list", map[string]any{"model": "e", "input": []string{"hello", "world", "hello"}}, 3, 16},
		{"asked dimensions", map[string]any{"model": "e", "input": "hello", "dimensions": 4}, 1, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := post(t, t.Context(), srv.URL+"/api/embed", tt.body)
			defer resp.Body.Close()
			var out struct {
				Embeddings [][]float64 `json:"embeddings"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			if len(out.Embeddings) != tt.wantVecs {
				t.Fatalf("got %d vectors, want %d", len(out.Embeddings), tt.wantVecs)
			}
			for _, v := range out.Embeddings {
				var norm float64
				for _, x := range v {
					norm += x * x
				}
				if len(v) != tt.wantDims || math.Abs(norm-1) > 1e-4 {
					t.Errorf("vector has %d dims and norm² %v, want %d and 1", len(v), norm, tt.wantDims)
				}
			}
			if tt.wantVecs == 3 {
				if !slices.Equal(out.Embeddings[0], out.Embeddings[2]) || slices.Equal(out.Embeddings[0], out.Embeddings[1]) {
					t.Error("same text should give the same vector, different text a different one")
				}
			}
		})
	}
}

// TestCancelAndLatency checks that the fake notices a client hanging up in
// the middle of a slow stream, and that Latency delays the first byte.
func TestCancelAndLatency(t *testing.T) {
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue("", fakeollama.Reply{Text: "a b c d", ChunkDelay: time.Hour})

	ctx, cancel := context.WithCancel(t.Context())
	resp := post(t, ctx, srv.URL+"/api/chat", map[string]any{"model": "m"})
	// Read the first line, then hang up while the fake waits an hour.
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, `"a "`) {
		t.Fatalf("first line = %q, %v", line, err)
	}
	cancel()
	resp.Body.Close()

	// The handler notices on its own goroutine, so poll for a short while.
	deadline := time.Now().Add(5 * time.Second)
	for !srv.Requests("/api/chat")[0].Cancelled {
		if time.Now().After(deadline) {
			t.Fatal("request never marked cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}

	srv.SetLatency(50 * time.Millisecond)
	start := time.Now()
	var v struct{ Version string }
	getJSON(t, srv.URL+"/api/version", &v)
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Errorf("version answered in %v, want at least 50ms", took)
	}
}

// TestControlEndpoints scripts the fake over HTTP, as the end-to-end tests
// do with cmd/fakeollama.
func TestControlEndpoints(t *testing.T) {
	srv := fakeollama.Start(t, fakeollama.Config{})

	resp := post(t, t.Context(), srv.URL+"/_fake/enqueue", map[string]any{
		"model":   "m",
		"replies": []fakeollama.Reply{{Text: "scripted"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("enqueue status = %d", resp.StatusCode)
	}
	resp = post(t, t.Context(), srv.URL+"/_fake/fail", map[string]any{"path": "/api/ps", "status": 500, "error": "down"})
	resp.Body.Close()

	resp = post(t, t.Context(), srv.URL+"/api/chat", map[string]any{"model": "m", "stream": false})
	if got := readChunks(t, resp.Body)[0].text(); got != "scripted" {
		t.Errorf("text = %q, want scripted", got)
	}
	resp.Body.Close()

	psResp, err := http.Get(srv.URL + "/api/ps")
	if err != nil {
		t.Fatal(err)
	}
	psResp.Body.Close()
	if psResp.StatusCode != 500 {
		t.Errorf("ps status = %d, want the injected 500", psResp.StatusCode)
	}

	var reqs []fakeollama.Request
	getJSON(t, srv.URL+"/_fake/requests", &reqs)
	if len(reqs) != 2 || reqs[0].Path != "/api/chat" || reqs[0].Stream || reqs[1].Path != "/api/ps" {
		t.Errorf("requests = %+v, want a non-streaming chat then ps", reqs)
	}
}

// getJSON fetches url and decodes the JSON body into v.
func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s: %v", url, err)
	}
}
