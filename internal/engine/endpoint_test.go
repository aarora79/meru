// This file tests what the endpoint engines share: the server-sent event
// reader, the pairing of tool calls with their results, and the fake
// endpoint the OpenAI and Anthropic tests use.

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeEndpoint is an httptest server that answers every request with one
// canned status and body, and remembers the last request's path, headers
// and body.
type fakeEndpoint struct {
	srv *httptest.Server

	mu     sync.Mutex // guards the fields below; handlers run on their own goroutines
	path   string
	header http.Header
	body   []byte
}

// newFakeEndpoint starts a fake that answers with status and body, and
// stops it when the test ends.
func newFakeEndpoint(t *testing.T, status int, body string) *fakeEndpoint {
	t.Helper()
	f := &fakeEndpoint{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.path, f.header, f.body = r.URL.Path, r.Header.Clone(), b
		f.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// request returns the last request's path, headers and decoded body.
func (f *fakeEndpoint) request(t *testing.T) (string, http.Header, map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(f.body, &m); err != nil {
		t.Fatalf("request body isn't JSON: %v: %s", err, f.body)
	}
	return f.path, f.header, m
}

// collect reads a whole stream: the text, the last Delta, and the error
// that ended it, if any.
func collect(t *testing.T, seq iter.Seq2[Delta, error]) (string, Delta, error) {
	t.Helper()
	var text strings.Builder
	var last Delta
	for d, err := range seq {
		if err != nil {
			return text.String(), last, err
		}
		text.WriteString(d.Text)
		last = d
	}
	return text.String(), last, nil
}

func TestReadSSE(t *testing.T) {
	in := ": a comment\n" +
		"event: one\ndata: {\"a\":1}\n\n" +
		"data: first line\ndata: second line\n\n" +
		"id: 7\nretry: 100\n\n" + // no data: skipped
		"data:no space\n" +
		"\n" +
		"data: [DONE]" // no blank line before the end
	var got []sseEvent
	for ev, err := range readSSE(strings.NewReader(in)) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	want := []sseEvent{
		{Type: "one", Data: []byte(`{"a":1}`)},
		{Data: []byte("first line\nsecond line")},
		{Data: []byte("no space")},
		{Data: []byte("[DONE]")},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Type != want[i].Type || string(got[i].Data) != string(want[i].Data) {
			t.Errorf("event %d = %q %q, want %q %q", i, got[i].Type, got[i].Data, want[i].Type, want[i].Data)
		}
	}
}

func TestReadSSEStopsWhenTheLoopBreaks(t *testing.T) {
	n := 0
	for range readSSE(strings.NewReader("data: 1\n\ndata: 2\n\ndata: 3\n\n")) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("read %d events after a break, want 1", n)
	}
}

// errReader fails after its text, as a dropped connection does.
type errReader struct{ r io.Reader }

func (e errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestReadSSEPassesAReadError(t *testing.T) {
	var gotErr error
	for _, err := range readSSE(errReader{strings.NewReader("data: 1\n\n")}) {
		gotErr = err
	}
	if !errors.Is(gotErr, io.ErrUnexpectedEOF) {
		t.Errorf("last item's error = %v, want io.ErrUnexpectedEOF", gotErr)
	}
}

func TestToolCallIDs(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a1", Name: "grep"}, {Name: "read_file"}}},
		{Role: RoleTool, ToolName: "grep", Content: "r1"},
		{Role: RoleTool, ToolName: "read_file", Content: "r2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "datetime"}}},
		{Role: RoleTool, ToolName: "datetime", Content: "r3"},
	}
	out, results := toolCallIDs(msgs)
	if got := out[1].ToolCalls[1].ID; got != "call_1_1" {
		t.Errorf("a call with no ID got %q, want call_1_1", got)
	}
	want := map[int]string{2: "a1", 3: "call_1_1", 5: "call_4_0"}
	for i, id := range want {
		if results[i] != id {
			t.Errorf("result %d answers %q, want %q", i, results[i], id)
		}
	}
	if msgs[1].ToolCalls[1].ID != "" {
		t.Error("toolCallIDs changed the caller's messages")
	}
}

func TestEndpointMustBeOnThisMachine(t *testing.T) {
	cfg := EndpointConfig{Name: "lab", URL: "http://10.0.0.5:8000/v1", Model: "m"}
	if _, err := NewOpenAI(cfg, nil, nil); err == nil {
		t.Error("NewOpenAI took a URL off this machine")
	}
	if _, err := NewAnthropic(cfg, nil, nil); err == nil {
		t.Error("NewAnthropic took a URL off this machine")
	}
	cfg.URL, cfg.Model = "http://127.0.0.1:1/v1", ""
	if _, err := NewOpenAI(cfg, nil, nil); err == nil {
		t.Error("NewOpenAI took an endpoint with no model")
	}
}

func TestEndpointRefusesARedirect(t *testing.T) {
	away := newFakeEndpoint(t, http.StatusOK, `{"choices":[{"message":{"content":"from elsewhere"}}]}`)
	srv := httptest.NewServer(http.RedirectHandler(away.srv.URL+"/chat/completions", http.StatusTemporaryRedirect))
	t.Cleanup(srv.Close)
	e, err := NewOpenAI(EndpointConfig{Name: "lab", URL: srv.URL, Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Generate(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Options{}); err == nil {
		t.Fatal("Generate followed a redirect")
	}
	away.mu.Lock()
	defer away.mu.Unlock()
	if away.body != nil {
		t.Error("the prompt reached the server the redirect named")
	}
}

func TestEndpointErrorSaysWhatToDo(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"error":{"message":"invalid x-api-key"}}`, "check the api_key entry for lab in secrets.toml"},
		{429, `{"error":{"message":"slow down"}}`, "try again in a minute"},
		{500, `<html>oops</html>`, "<html>oops</html>"},
	}
	for _, c := range cases {
		f := newFakeEndpoint(t, c.status, c.body)
		e, err := NewOpenAI(EndpointConfig{Name: "lab", URL: f.srv.URL, Key: "sk-test-secret-value", Model: "m"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.Generate(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Options{})
		var ee *EndpointError
		if !errors.As(err, &ee) || ee.StatusCode != c.status {
			t.Fatalf("status %d: err = %v, want an *EndpointError", c.status, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %d: %q doesn't say %q", c.status, err, c.want)
		}
		if strings.Contains(err.Error(), "sk-test-secret-value") {
			t.Errorf("status %d: the error holds the key: %q", c.status, err)
		}
	}
}

// newHoldingServer starts a server that writes first, flushes it, and
// holds the connection open until release closes or the client goes.
func newHoldingServer(t *testing.T, release <-chan struct{}, first string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, first)
		w.(http.Flusher).Flush()
		// select waits for whichever channel is ready first.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
