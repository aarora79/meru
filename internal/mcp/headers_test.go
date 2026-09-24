// This file tests the Headers setting: a Streamable HTTP server gets the
// configured headers on every request, and a request to another host gets
// none of them.

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHeadersOnEveryRequest(t *testing.T) {
	srv := newTestServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var mu sync.Mutex // guards seen; the server handles requests on its own goroutines
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	cfg := ServerConfig{
		Name:    "h",
		URL:     ts.URL + "/mcp",
		Allow:   []string{"echo"},
		Headers: map[string]string{"Authorization": "Bearer s3cret"},
	}
	p := openPool(t, cfg, nil, dialTransport)
	if res, err := p.Call(context.Background(), "h.echo", json.RawMessage(`{"text":"hi"}`)); err != nil || res.Text != "hi" {
		t.Fatalf("Call = %+v, %v", res, err)
	}

	mu.Lock()
	defer mu.Unlock()
	// The handshake, the tool listing and the call are separate requests.
	if len(seen) < 3 {
		t.Fatalf("server saw %d requests, want at least 3", len(seen))
	}
	for i, h := range seen {
		if h != "Bearer s3cret" {
			t.Errorf("request %d Authorization = %q, want the configured header", i, h)
		}
	}
}

func TestHeaderTransportOnlyForItsHost(t *testing.T) {
	var got http.Header
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Header
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
	})
	rt := &headerTransport{base: base, host: "127.0.0.1:9000", headers: map[string]string{"X-Key": "k"}}

	tests := []struct {
		url  string
		want string
	}{
		{"http://127.0.0.1:9000/mcp", "k"},
		{"http://other.example.com/mcp", ""},
	}
	for _, tt := range tests {
		req, err := http.NewRequest(http.MethodPost, tt.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if v := got.Get("X-Key"); v != tt.want {
			t.Errorf("%s: X-Key = %q, want %q", tt.url, v, tt.want)
		}
		if req.Header.Get("X-Key") != "" {
			t.Errorf("%s: RoundTrip changed the caller's request", tt.url)
		}
	}
}

// roundTripFunc lets a plain function stand in for an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls f.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
