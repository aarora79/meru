// This file defines the Fake itself: its settings, the scripted replies a
// test queues up, the record of requests it received, and ServeHTTP, which
// sends each request to the right endpoint. The endpoints live in api.go.

package fakeollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Config sets up a Fake. The zero value works: it accepts any model name and
// reports version 0.12.11, the first Ollama release with log probabilities.
type Config struct {
	// Version is what /api/version reports. Empty means "0.12.11".
	Version string `json:"version,omitempty"`
	// Models lists the model names the fake accepts. A request for any other
	// model gets a 404, as Ollama sends for a model that isn't pulled. Empty
	// accepts every name.
	Models []string `json:"models,omitempty"`
	// Loaded lists the models /api/ps reports before any request arrives.
	// Every model a request uses joins the list, as it would in Ollama.
	Loaded []string `json:"loaded,omitempty"`
	// EmbedDims is the length of each vector /api/embed returns, unless the
	// request asks for a size. Zero means 8.
	EmbedDims int `json:"embed_dims,omitempty"`
	// Latency is a pause before every response, to test timeouts and the
	// time-to-first-token metric.
	Latency time.Duration `json:"latency,omitempty"`
	// DefaultText is the answer to a chat or generate call when no scripted
	// reply is queued. Empty means "Hello from fake Ollama."
	DefaultText string `json:"default_text,omitempty"`
}

// Reply scripts one answer to /api/chat or /api/generate. Queue replies with
// Fake.Enqueue. Durations marshal to JSON as nanoseconds, which is how
// cmd/fakeollama's control endpoint reads them.
type Reply struct {
	// Text is the answer. The fake streams it one word at a time.
	Text string `json:"text,omitempty"`
	// Chunks, when set, replaces Text: the fake streams exactly these pieces,
	// and the answer is their concatenation.
	Chunks []string `json:"chunks,omitempty"`
	// ToolCalls are tool calls the model makes. /api/generate ignores them.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Thinking holds pieces of a thinking model's hidden reasoning. On
	// /api/chat the fake streams them before the text, each in a chunk
	// whose message has an empty content and a "thinking" field, as
	// Ollama does. They count against the request's num_predict like any
	// other output token: see Reply.cut.
	Thinking []string `json:"thinking,omitempty"`
	// DoneReason is why the model stopped. Empty means "stop".
	DoneReason string `json:"done_reason,omitempty"`
	// LogProbs, when set, are the log probabilities to report, one per chunk,
	// in order. When empty and the request asks for log probabilities, the
	// fake makes up one entry per chunk. Either way the fake sends them only
	// when the request sets "logprobs": true, as Ollama does.
	LogProbs []LogProb `json:"logprobs,omitempty"`
	// PromptTokens and OutputTokens override prompt_eval_count and
	// eval_count. Zero means: count the words in the prompt, and count the
	// chunks in the answer.
	PromptTokens int `json:"prompt_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	// LoadDuration is reported as load_duration. Zero means the model was
	// already warm.
	LoadDuration time.Duration `json:"load_duration,omitempty"`

	// Status, when non-zero, makes the fake answer with this HTTP status and
	// Error as the message, instead of a reply.
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	// StreamError, when set on a streaming call, makes the fake send
	// FailAfter chunks and then an {"error": StreamError} line, the way
	// Ollama reports a failure part way through. A non-streaming call gets an
	// HTTP 500 instead.
	StreamError string `json:"stream_error,omitempty"`
	FailAfter   int    `json:"fail_after,omitempty"`

	// Delay is a pause before the first byte; ChunkDelay is a pause before
	// each later chunk.
	Delay      time.Duration `json:"delay,omitempty"`
	ChunkDelay time.Duration `json:"chunk_delay,omitempty"`
}

// ToolCall is one scripted tool call.
type ToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// LogProb is one scripted token: its text, its log probability, and the
// alternatives the model weighed at that position.
type LogProb struct {
	Token   string         `json:"token"`
	LogProb float64        `json:"logprob"`
	Top     []TokenLogProb `json:"top,omitempty"`
}

// TokenLogProb is one alternative token and its log probability.
type TokenLogProb struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
}

// Request is one request the fake received, kept for the test to inspect.
type Request struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Model and Stream are pulled out of Body for convenience. Stream is true
	// when the request streams, including when it left the field out.
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
	// Body is the raw request body. Decode it to check any other field.
	Body json.RawMessage `json:"body,omitempty"`
	// Cancelled is true when the client went away before the reply ended.
	Cancelled bool `json:"cancelled"`
}

// Fake is the fake Ollama. It is an http.Handler, so it can run inside
// httptest.NewServer (see Start) or behind any http.Server (see
// cmd/fakeollama). All methods are safe to call from several goroutines.
type Fake struct {
	cfg Config

	// mu guards every field below it. net/http runs each request in its own
	// goroutine, so the handlers and the test can touch these at once.
	mu       sync.Mutex
	queues   map[string][]Reply   // scripted replies by model; "" means any model
	failures map[string][]errorAt // injected HTTP errors by path
	loaded   []string             // what /api/ps reports, in load order
	requests []Request            // every request, in arrival order
	latency  time.Duration        // copy of cfg.Latency that SetLatency can change
}

// errorAt is one injected HTTP error.
type errorAt struct {
	Status  int
	Message string
}

// New returns a Fake with the given settings. It doesn't listen anywhere;
// hand it to an http.Server, or use Start in tests.
func New(cfg Config) *Fake {
	if cfg.Version == "" {
		cfg.Version = "0.12.11"
	}
	if cfg.EmbedDims <= 0 {
		cfg.EmbedDims = 8
	}
	if cfg.DefaultText == "" {
		cfg.DefaultText = "Hello from fake Ollama."
	}
	return &Fake{
		cfg:      cfg,
		queues:   map[string][]Reply{},
		failures: map[string][]errorAt{},
		loaded:   slices.Clone(cfg.Loaded),
		latency:  cfg.Latency,
	}
}

// Server is a Fake listening on a loopback port. Server embeds *Fake (the
// field has a type but no name), so every Fake method works on a Server too:
// srv.Enqueue(...) calls srv.Fake.Enqueue(...).
type Server struct {
	*Fake
	// URL is the base address to give OllamaEngine, such as
	// http://127.0.0.1:53412.
	URL string
}

// Start runs a Fake on a free loopback port for the length of test t and
// shuts it down when the test ends.
func Start(t testing.TB, cfg Config) *Server {
	t.Helper()
	f := New(cfg)
	// httptest.NewServer listens on 127.0.0.1 with a port the OS picks.
	ts := httptest.NewServer(f)
	// t.Cleanup runs ts.Close after the test and its subtests finish.
	t.Cleanup(ts.Close)
	return &Server{Fake: f, URL: ts.URL}
}

// Enqueue adds scripted replies for model. Each chat or generate call takes
// the next reply queued for its model, then the next one queued for model
// "", and falls back to Config.DefaultText when both queues are empty.
func (f *Fake) Enqueue(model string, replies ...Reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[model] = append(f.queues[model], replies...)
}

// FailNext makes the next request to path (such as "/api/embed") fail with
// the given HTTP status and error message. Calls stack up: two calls fail
// the next two requests.
func (f *Fake) FailNext(path string, status int, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[path] = append(f.failures[path], errorAt{Status: status, Message: message})
}

// SetLatency changes the pause before every response.
func (f *Fake) SetLatency(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latency = d
}

// Requests returns a copy of the requests received so far whose path is
// path, or all of them when path is "".
func (f *Fake) Requests(path string) []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Request
	for _, r := range f.requests {
		if path == "" || r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// ServeHTTP answers one HTTP request. net/http calls it; tests don't.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_fake/") {
		f.serveControl(w, r)
		return
	}

	// Read the whole body once, so it can be both recorded and decoded. The
	// limit stops a broken client from filling memory.
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	id := f.record(r, body)

	f.mu.Lock()
	latency := f.latency
	f.mu.Unlock()
	if !sleep(r.Context(), latency) {
		f.markCancelled(id)
		return
	}

	// The table maps each path to the method it needs and its handler.
	routes := map[string]struct {
		method  string
		handler func(http.ResponseWriter, *http.Request, []byte, int)
	}{
		"/api/version":  {http.MethodGet, f.serveVersion},
		"/api/ps":       {http.MethodGet, f.servePS},
		"/api/chat":     {http.MethodPost, f.serveChat},
		"/api/generate": {http.MethodPost, f.serveGenerate},
		"/api/embed":    {http.MethodPost, f.serveEmbed},
	}
	route, ok := routes[r.URL.Path]
	if !ok {
		writeError(w, http.StatusNotFound, "404 page not found")
		return
	}
	if r.Method != route.method {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if fail, ok := f.takeFailure(r.URL.Path); ok {
		writeError(w, fail.Status, fail.Message)
		return
	}
	route.handler(w, r, body, id)
}

// record stores a Request for r and returns its index in f.requests, so the
// handler can mark it cancelled later.
func (f *Fake) record(r *http.Request, body []byte) int {
	// peek pulls out the two fields every endpoint shares.
	var peek struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek) // a bad body is reported by the handler
	rec := Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Model:  peek.Model,
		Stream: peek.Stream == nil || *peek.Stream,
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Valid(body) {
		rec.Body = json.RawMessage(body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, rec)
	return len(f.requests) - 1
}

// markCancelled flags request id as cut short by the client.
func (f *Fake) markCancelled(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[id].Cancelled = true
}

// takeFailure removes and returns the next injected error for path, if any.
func (f *Fake) takeFailure(path string) (errorAt, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.failures[path]
	if len(q) == 0 {
		return errorAt{}, false
	}
	f.failures[path] = q[1:]
	return q[0], true
}

// nextReply removes and returns the next scripted reply for model, falling
// back to the "any model" queue and then to the default text.
func (f *Fake) nextReply(model string) Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range []string{model, ""} {
		if q := f.queues[key]; len(q) > 0 {
			f.queues[key] = q[1:]
			return q[0]
		}
	}
	return Reply{Text: f.cfg.DefaultText}
}

// accepts reports whether the fake knows model. It returns true for every
// name when Config.Models is empty.
func (f *Fake) accepts(model string) bool {
	return len(f.cfg.Models) == 0 || slices.Contains(f.cfg.Models, model)
}

// touch updates the loaded-model list after a request for model: the model
// stays loaded, unless keepAlive is zero, which tells Ollama to unload it.
func (f *Fake) touch(model string, keepAlive json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = slices.DeleteFunc(f.loaded, func(m string) bool { return m == model })
	if !unloads(keepAlive) {
		f.loaded = append(f.loaded, model)
	}
}

// unloads reports whether a keep_alive value means "unload now": the number 0
// or a duration string of zero such as "0" or "0s".
func unloads(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n == 0
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	if s == "0" {
		return true
	}
	d, err := time.ParseDuration(s)
	return err == nil && d == 0
}

// serveControl answers the /_fake/ endpoints, which let a test in another
// process script the fake that cmd/fakeollama serves:
//
//	POST /_fake/enqueue   {"model": "m", "replies": [Reply, ...]}
//	POST /_fake/fail      {"path": "/api/chat", "status": 500, "error": "boom"}
//	GET  /_fake/requests  returns every Request as a JSON array
func (f *Fake) serveControl(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/_fake/enqueue" && r.Method == http.MethodPost:
		var in struct {
			Model   string  `json:"model"`
			Replies []Reply `json:"replies"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "decode enqueue: "+err.Error())
			return
		}
		f.Enqueue(in.Model, in.Replies...)
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/_fake/fail" && r.Method == http.MethodPost:
		var in struct {
			Path   string `json:"path"`
			Status int    `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "decode fail: "+err.Error())
			return
		}
		f.FailNext(in.Path, in.Status, in.Error)
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/_fake/requests" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, f.Requests(""))
	default:
		writeError(w, http.StatusNotFound, "unknown control endpoint")
	}
}

// sleep waits for d, or until ctx ends. It returns false when ctx ended
// first, which means the client hung up.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	// select waits until one of its cases can go ahead, and runs that one.
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// writeJSON sends v as a JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone; nothing to do then
}

// writeError sends Ollama's error shape, {"error": "..."}, with status.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Error: message})
}
