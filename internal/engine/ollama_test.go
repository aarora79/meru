// This file tests OllamaEngine against a fake Ollama built with httptest. No
// test here needs a real model; the integration test in
// ollama_integration_test.go covers the real server.

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOllama is an httptest server that answers each path with a canned
// reply and remembers the last request body it got on each path.
type fakeOllama struct {
	srv *httptest.Server

	mu     sync.Mutex        // guards bodies; handlers run on their own goroutines
	bodies map[string][]byte // last request body per path
}

// route is one canned reply: a status code and a body.
type route struct {
	status int
	body   string
}

// newFakeOllama starts a fake Ollama that serves routes and stops it when
// the test ends. Paths not in routes get a 404 with an Ollama-style error.
func newFakeOllama(t *testing.T, routes map[string]route) *fakeOllama {
	t.Helper()
	f := &fakeOllama{bodies: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies[r.URL.Path] = b
		f.mu.Unlock()
		rt, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"no such path"}`)
			return
		}
		w.WriteHeader(rt.status)
		io.WriteString(w, rt.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// body returns the last request body sent to path, decoded into a map.
func (f *fakeOllama) body(t *testing.T, path string) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(f.bodies[path], &m); err != nil {
		t.Fatalf("request body on %s is not JSON: %v (%q)", path, err, f.bodies[path])
	}
	return m
}

// newTestEngine builds an engine pointed at the fake server.
func newTestEngine(t *testing.T, f *fakeOllama, keepAlive string) *OllamaEngine {
	t.Helper()
	e, err := NewOllama(f.srv.URL, keepAlive, "nomic-embed-text", f.srv.Client(), nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	return e
}

const chatReply = `{"model":"m","message":{"role":"assistant","content":"C"},"done":true,"done_reason":"length",
"logprobs":[{"token":"C","logprob":-0.69,"bytes":[67],"top_logprobs":[
  {"token":"C","logprob":-0.69},{"token":"A","logprob":-1.05},{"token":" D","logprob":-2.16}]}],
"total_duration":2112496458,"load_duration":2059612666,"prompt_eval_count":61,
"prompt_eval_duration":48942000,"eval_count":1,"eval_duration":1000}`

func TestGenerate(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, chatReply}})
	e := newTestEngine(t, f, "-1")
	temp := 0.2

	got, err := e.Generate(context.Background(),
		[]Message{{Role: RoleUser, Content: "hi"}}, nil,
		Options{Model: "m", Temperature: &temp, MaxTokens: 1, LogProbs: true, TopLogProbs: 20})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	want := Completion{
		Text:       "C",
		DoneReason: "length",
		Usage: Usage{
			PromptTokens:       61,
			OutputTokens:       1,
			LoadDuration:       2059612666 * time.Nanosecond,
			PromptEvalDuration: 48942000 * time.Nanosecond,
			EvalDuration:       1000 * time.Nanosecond,
			TotalDuration:      2112496458 * time.Nanosecond,
		},
		LogProbs: []PositionLogProbs{{
			Chosen: TokenLogProb{"C", -0.69},
			Top:    []TokenLogProb{{"C", -0.69}, {"A", -1.05}, {" D", -2.16}},
		}},
	}
	// reflect.DeepEqual compares two values field by field, through slices.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Generate =\n %+v\nwant\n %+v", got, want)
	}

	body := f.body(t, "/api/chat")
	checks := []struct {
		key  string
		want any
	}{
		{"model", "m"},
		{"stream", false},
		{"keep_alive", float64(-1)}, // JSON numbers decode to float64
		{"logprobs", true},
		{"top_logprobs", float64(20)},
		{"think", false},
	}
	for _, c := range checks {
		if body[c.key] != c.want {
			t.Errorf("request %s = %#v, want %#v", c.key, body[c.key], c.want)
		}
	}
	opts, _ := body["options"].(map[string]any)
	if opts["num_predict"] != float64(1) || opts["temperature"] != 0.2 {
		t.Errorf("request options = %v, want num_predict 1 and temperature 0.2", opts)
	}
}

func TestGenerateRequestDefaults(t *testing.T) {
	// With zero Options apart from the model, the optional keys stay out of
	// the request, so Ollama uses the model's own defaults.
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, `{"message":{"content":"ok"},"done":true}`}})
	e := newTestEngine(t, f, "")
	if _, err := e.Generate(context.Background(), nil, nil, Options{Model: "m"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body := f.body(t, "/api/chat")
	for _, key := range []string{"keep_alive", "options", "logprobs", "top_logprobs", "think", "tools"} {
		if v, ok := body[key]; ok {
			t.Errorf("request has %s = %v, want it left out", key, v)
		}
	}
}

func TestGenerateNoThink(t *testing.T) {
	// NoThink sends "think": false and asks for no log probabilities.
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, `{"message":{"content":"ok"},"done":true}`}})
	e := newTestEngine(t, f, "")
	if _, err := e.Generate(context.Background(), nil, nil, Options{Model: "m", NoThink: true}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body := f.body(t, "/api/chat")
	if body["think"] != false {
		t.Errorf("request think = %#v, want false", body["think"])
	}
	if v, ok := body["logprobs"]; ok {
		t.Errorf("request has logprobs = %v, want it left out", v)
	}
}

func TestGenerateToolsAndToolCalls(t *testing.T) {
	reply := `{"message":{"role":"assistant","content":"","tool_calls":[
		{"id":"abc","function":{"index":0,"name":"get_weather","arguments":{"city":"Paris"}}}]},
		"done":true,"done_reason":"stop"}`
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, reply}})
	e := newTestEngine(t, f, "5m")

	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "now"}}},
		{Role: RoleTool, ToolName: "now", Content: "noon"},
	}
	tools := []ToolSpec{{Name: "get_weather", Description: "weather", Parameters: json.RawMessage(`{"type":"object"}`)}}
	got, err := e.Generate(context.Background(), msgs, tools, Options{Model: "m"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "abc" || got.ToolCalls[0].Name != "get_weather" ||
		string(got.ToolCalls[0].Arguments) != `{"city":"Paris"}` {
		t.Errorf("tool calls = %+v", got.ToolCalls)
	}

	body := f.body(t, "/api/chat")
	if body["keep_alive"] != "5m" {
		t.Errorf("keep_alive = %#v, want \"5m\"", body["keep_alive"])
	}
	wantTools := `[{"function":{"description":"weather","name":"get_weather","parameters":{"type":"object"}},"type":"function"}]`
	if b, _ := json.Marshal(body["tools"]); string(b) != wantTools {
		t.Errorf("tools = %s\nwant    %s", b, wantTools)
	}
	wantMsgs := `[{"content":"","role":"assistant","tool_calls":[{"function":{"arguments":{},"name":"now"}}]},` +
		`{"content":"noon","role":"tool","tool_name":"now"}]`
	if b, _ := json.Marshal(body["messages"]); string(b) != wantMsgs {
		t.Errorf("messages = %s\nwant       %s", b, wantMsgs)
	}
}

func TestStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`,
		`{"message":{"role":"assistant","content":"Hi"},"done":false}`,
		``,
		`{"message":{"role":"assistant","content":" there"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop",` +
			`"total_duration":60,"load_duration":1,"prompt_eval_count":12,"prompt_eval_duration":49,"eval_count":3,"eval_duration":8}`,
	}, "\n") + "\n"
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, stream}})
	e := newTestEngine(t, f, "-1")

	seq, err := e.Stream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil,
		Options{Model: "m", MaxTokens: 3})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []Delta
	for d, err := range seq {
		if err != nil {
			t.Fatalf("stream item: %v", err)
		}
		got = append(got, d)
	}
	want := []Delta{
		{Text: "Hi"},
		{Text: " there"},
		{Done: true, DoneReason: "stop", Usage: Usage{
			PromptTokens: 12, OutputTokens: 3, LoadDuration: 1, PromptEvalDuration: 49, EvalDuration: 8, TotalDuration: 60,
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deltas =\n %+v\nwant\n %+v", got, want)
	}

	body := f.body(t, "/api/chat")
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	if opts, _ := body["options"].(map[string]any); opts["num_predict"] != float64(3) {
		t.Errorf("options = %v, want num_predict 3", body["options"])
	}

	// A second range over the same sequence reports an error.
	for _, err := range seq {
		if err == nil {
			t.Errorf("second range: got nil error, want one")
		}
	}
}

func TestStreamErrors(t *testing.T) {
	// Each case is a stream body that goes wrong partway; the last item of
	// the sequence must be an error containing want.
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{"error line", `{"message":{"content":"a"},"done":false}` + "\n" + `{"error":"model crashed"}` + "\n", "model crashed"},
		{"bad json", `{"message":{"content":"a"},"done":false}` + "\n" + `{not json` + "\n", "decode line"},
		{"no done line", `{"message":{"content":"a"},"done":false}` + "\n", "ended before done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeOllama(t, map[string]route{"/api/chat": {200, tt.stream}})
			e := newTestEngine(t, f, "")
			seq, err := e.Stream(context.Background(), nil, nil, Options{Model: "m"})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			var last error
			for _, err := range seq {
				last = err
			}
			if last == nil || !strings.Contains(last.Error(), tt.want) {
				t.Errorf("last error = %v, want one containing %q", last, tt.want)
			}
		})
	}
}

func TestStreamStopsWhenCallerBreaks(t *testing.T) {
	stream := `{"message":{"content":"a"},"done":false}` + "\n" + `{"message":{"content":"b"},"done":false}` + "\n"
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, stream}})
	e := newTestEngine(t, f, "")
	seq, err := e.Stream(context.Background(), nil, nil, Options{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	n := 0
	for range seq {
		n++
		break
	}
	if n != 1 {
		t.Errorf("got %d items after break, want 1", n)
	}
}

func TestEmbed(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/embed": {200, `{"model":"nomic-embed-text","embeddings":[[0.1,0.2],[0.3,0.4]]}`}})
	e := newTestEngine(t, f, "-1")

	got, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	want := []Vector{{0.1, 0.2}, {0.3, 0.4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Embed = %v, want %v", got, want)
	}
	body := f.body(t, "/api/embed")
	if body["model"] != "nomic-embed-text" || body["keep_alive"] != float64(-1) {
		t.Errorf("request = %v", body)
	}
	if b, _ := json.Marshal(body["input"]); string(b) != `["a","b"]` {
		t.Errorf("input = %s", b)
	}

	// The wrong number of vectors is an error.
	if _, err := e.Embed(context.Background(), []string{"a", "b", "c"}); err == nil {
		t.Errorf("Embed with 3 texts and 2 vectors: got nil error")
	}
	// No texts means no request.
	if got, err := e.Embed(context.Background(), nil); got != nil || err != nil {
		t.Errorf("Embed(nil) = %v, %v; want nil, nil", got, err)
	}
}

func TestInfo(t *testing.T) {
	f := newFakeOllama(t, map[string]route{
		"/api/version": {200, `{"version":"0.34.0"}`},
		"/api/ps":      {200, `{"models":[{"name":"gemma3:1b","size":1},{"name":"nomic-embed-text:latest"}]}`},
	})
	e := newTestEngine(t, f, "")
	got, err := e.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	want := ModelInfo{Runtime: "ollama", RuntimeVersion: "0.34.0", LoadedModels: []string{"gemma3:1b", "nomic-embed-text:latest"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Info = %+v, want %+v", got, want)
	}
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		version string
		wantErr bool
	}{
		{"0.34.0", false},
		{"0.12.11", false},
		{"0.13.0-rc1", false},
		{"v1.0", false},
		{"0.12.10", true},
		{"0.11.99", true},
		{"0.0.0", true},
		{"banana", true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			f := newFakeOllama(t, map[string]route{"/api/version": {200, `{"version":"` + tt.version + `"}`}})
			e := newTestEngine(t, f, "")
			err := e.CheckVersion(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckVersion(%s) error = %v, wantErr %v", tt.version, err, tt.wantErr)
			}
			// A too-old version names both versions, so the user knows
			// what they have and what to install.
			if err != nil && tt.version != "banana" {
				msg := err.Error()
				if !strings.Contains(msg, tt.version) || !strings.Contains(msg, MinOllamaVersion) {
					t.Errorf("error %q should name %s and %s", msg, tt.version, MinOllamaVersion)
				}
			}
		})
	}
}

func TestNon2xxSurfacesOllamaError(t *testing.T) {
	tests := []struct {
		name    string
		rt      route
		wantMsg string
	}{
		{"json error", route{404, `{"error":"model 'nope' not found"}`}, "model 'nope' not found"},
		{"plain text", route{500, "boom"}, "boom"},
		{"empty", route{502, ""}, "(empty body)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeOllama(t, map[string]route{"/api/chat": tt.rt})
			e := newTestEngine(t, f, "")
			_, err := e.Generate(context.Background(), nil, nil, Options{Model: "nope"})
			// errors.As looks through wrapped errors for an *APIError and,
			// when it finds one, stores it in apiErr.
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want an *APIError", err)
			}
			if apiErr.StatusCode != tt.rt.status || apiErr.Message != tt.wantMsg || apiErr.Path != "/api/chat" {
				t.Errorf("APIError = %+v, want status %d and message %q", apiErr, tt.rt.status, tt.wantMsg)
			}
			if _, err := e.Stream(context.Background(), nil, nil, Options{Model: "nope"}); !errors.As(err, &apiErr) {
				t.Errorf("Stream error = %v, want an *APIError", err)
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	// The handler holds the request open until the client goes away, the way
	// a slow model would.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" && strings.Contains(readAll(r), `"stream":true`) {
			// Send one chunk, then hang, so the cancel lands mid-stream.
			io.WriteString(w, `{"message":{"content":"a"},"done":false}`+"\n")
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanup runs last-in, first-out: release the handlers, then close.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	e, err := NewOllama(srv.URL, "", "embed", srv.Client(), nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}

	calls := map[string]func(ctx context.Context) error{
		"generate": func(ctx context.Context) error {
			_, err := e.Generate(ctx, nil, nil, Options{Model: "m"})
			return err
		},
		"embed": func(ctx context.Context) error {
			_, err := e.Embed(ctx, []string{"x"})
			return err
		},
		"info": func(ctx context.Context) error {
			_, err := e.Info(ctx)
			return err
		},
		"stream": func(ctx context.Context) error {
			seq, err := e.Stream(ctx, nil, nil, Options{Model: "m"})
			if err != nil {
				return err
			}
			var last error
			for _, err := range seq {
				last = err
			}
			return last
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := call(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}

// readAll returns the request body as a string.
func readAll(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestNewOllama(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		keepAlive string
		wantErr   bool
	}{
		{"ipv4 loopback", "http://127.0.0.1:11434", "-1", false},
		{"other 127 address", "http://127.8.9.10:11434/", "", false},
		{"ipv6 loopback", "http://[::1]:11434", "5m", false},
		{"localhost", "http://localhost:11434", "300", false},
		{"lan address", "http://192.168.1.10:11434", "", true},
		{"public host name", "https://api.example.com", "", true},
		{"any address", "http://0.0.0.0:11434", "", true},
		{"no scheme", "127.0.0.1:11434", "", true},
		{"bad keep_alive", "http://127.0.0.1:11434", "forever", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewOllama(tt.baseURL, tt.keepAlive, "", nil, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewOllama(%q, %q) error = %v, wantErr %v", tt.baseURL, tt.keepAlive, err, tt.wantErr)
			}
		})
	}
}

func TestGenerateRefusesRedirect(t *testing.T) {
	srv := httptest.NewServer(http.RedirectHandler("http://127.0.0.1:1/elsewhere", http.StatusFound))
	t.Cleanup(srv.Close)
	e, err := NewOllama(srv.URL, "", "", srv.Client(), nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	_, err = e.Generate(context.Background(), nil, nil, Options{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error = %v, want a refused redirect", err)
	}
}

func TestKeepAliveJSON(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"-1", "-1"},
		{"0", "0"},
		{"300", "300"},
		{"5m", `"5m"`},
		{"1h30m", `"1h30m"`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := keepAliveJSON(tt.in)
			if err != nil || string(got) != tt.want {
				t.Errorf("keepAliveJSON(%q) = %s, %v; want %s", tt.in, got, err, tt.want)
			}
		})
	}
}
