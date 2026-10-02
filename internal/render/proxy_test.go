// This file tests the proxy without a browser: an http.Client that uses
// it as its proxy plays Chrome, and httptest servers play the sites.

package render

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testProxy starts a proxy whose dial lets through only allow, and an
// http.Client that sends everything through it.
func testProxy(t *testing.T, allow ...string) (*proxy, *http.Client) {
	t.Helper()
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		for _, a := range allow {
			if a == addr {
				return d.DialContext(ctx, network, addr)
			}
		}
		return nil, fmt.Errorf("%s is on this machine", addr)
	}
	px, err := startProxy(dial, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(px.close)
	pu, _ := url.Parse("http://" + px.addr())
	client := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(pu),
		// The test server's certificate is its own; the tunnel carries TLS
		// end to end, so the client checks it, not the proxy.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- test against httptest's own certificate
	}}
	return px, client
}

func TestProxyDisarmed(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "page") }))
	defer site.Close()
	px, client := testProxy(t, site.Listener.Addr().String())
	resp, err := client.Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disarmed: %s, want 403", resp.Status)
	}
	if req, ref := px.counts(); req != 0 || ref != 1 {
		t.Errorf("counts = %d, %d; want 0 allowed, 1 refused", req, ref)
	}
}

func TestProxyArmed(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("Proxy-Authorization reached the site")
		}
		_, _ = io.WriteString(w, "plain page")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secure page") }))
	defer secure.Close()
	px, client := testProxy(t, plain.Listener.Addr().String(), secure.Listener.Addr().String())
	px.arm()

	req, _ := http.NewRequest(http.MethodGet, plain.URL, nil)
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	for _, tc := range []struct {
		req  *http.Request
		want string
	}{
		{req, "plain page"},
		{mustReq(t, http.MethodGet, secure.URL), "secure page"},
	} {
		resp, err := client.Do(tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.req.URL, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != tc.want {
			t.Errorf("%s: %q", tc.req.URL, body)
		}
	}
	if q := px.quietFor(); q > secondish {
		t.Errorf("quietFor = %v right after traffic", q)
	}
	if n, _ := px.counts(); n != 2 {
		t.Errorf("allowed = %d, want 2", n)
	}

	// A method the proxy doesn't pass on.
	resp, err := client.Do(mustReq(t, http.MethodDelete, plain.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: %s, want 405", resp.Status)
	}
}

func TestProxyRefusesPrivate(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the proxy reached a server it should have refused")
	}))
	defer inside.Close()
	px, client := testProxy(t) // allows nothing, as the public check refuses loopback
	px.arm()
	for _, u := range []string{inside.URL, strings.Replace(inside.URL, "http://", "https://", 1)} {
		resp, err := client.Get(u)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Errorf("%s: %s, want 502", u, resp.Status)
			}
		}
	}
	if _, ref := px.counts(); ref != 2 {
		t.Errorf("refused = %d, want 2", ref)
	}
}

func TestDropHop(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "X-Secret, keep-alive")
	h.Set("X-Secret", "1")
	h.Set("Proxy-Authorization", "Basic x")
	h.Set("Keep-Alive", "timeout=5")
	h.Set("Accept", "text/html")
	dropHop(h)
	if len(h) != 1 || h.Get("Accept") != "text/html" {
		t.Errorf("after dropHop: %v", h)
	}
}

// secondish bounds "just now" for quietFor.
const secondish = 500_000_000

// mustReq builds a request or fails the test.
func mustReq(t *testing.T, method, u string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
