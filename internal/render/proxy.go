// This file is the proxy every request of a rendered page goes through.
// Chrome starts with --proxy-server pointing here, so it opens no
// connection to the network itself: it asks the proxy, and the proxy
// dials with web_fetch's check, which refuses this machine, the local
// network and the other addresses that aren't public. The proxy refuses
// everything until the renderer arms it, just before the page loads, so
// the requests Chrome makes as it starts go nowhere.

package render

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DialFunc connects to a network address. merud passes web_fetch's public
// dialer; tests pass one that reaches an httptest server.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// dialTimeout bounds one connection the proxy opens.
const dialTimeout = 10 * time.Second

// hopHeaders are the headers that describe one connection, not the
// request, so a proxy drops them before it passes a request or a response
// on. Proxy-Authorization would also carry credentials meant for this
// proxy, which has none.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// proxy is one HTTP proxy on 127.0.0.1, started for one page and closed
// after it.
type proxy struct {
	ln   net.Listener
	srv  *http.Server
	dial DialFunc
	log  *slog.Logger
	// tr sends plain-HTTP requests on, dialing through dial.
	tr *http.Transport

	// mu guards the fields below it.
	mu       sync.Mutex
	armed    bool
	requests int // connections the proxy opened for the page
	refused  int // connections it refused: not armed, not public, or failed
	// tunnels holds the open CONNECT tunnels, so disarm can close them.
	tunnels map[net.Conn]struct{}

	// copies counts the goroutines copying tunnel bytes; close waits for
	// them, after disarm has closed their connections.
	copies sync.WaitGroup

	// lastByte is when bytes last moved through the proxy, in Unix
	// nanoseconds. The renderer reads it to tell when the page has stopped
	// loading: a page that fetches its text after load keeps bytes moving
	// until the text arrives. atomic.Int64 lets the copy goroutines write
	// it and the renderer read it with no lock.
	lastByte atomic.Int64
}

// startProxy listens on a free loopback port and serves until close. It
// fails when it can't listen.
func startProxy(dial DialFunc, log *slog.Logger) (*proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("page reader proxy: %w", err)
	}
	p := &proxy{ln: ln, dial: dial, log: log, tunnels: map[net.Conn]struct{}{}}
	p.touch()
	p.tr = &http.Transport{
		// Proxy nil: the transport connects straight out through dial,
		// never through a proxy from the environment.
		Proxy:               nil,
		DialContext:         dial,
		TLSHandshakeTimeout: dialTimeout,
		MaxIdleConns:        8,
		IdleConnTimeout:     10 * time.Second,
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: dialTimeout}
	// Serve returns when close shuts the server down; its error then is
	// http.ErrServerClosed, which means nothing went wrong.
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// addr is where the proxy listens, "127.0.0.1:<port>".
func (p *proxy) addr() string { return p.ln.Addr().String() }

// arm lets connections through, each after the dial check.
func (p *proxy) arm() {
	p.mu.Lock()
	p.armed = true
	p.mu.Unlock()
}

// disarm refuses connections again and closes every open tunnel, so a
// page's long-lived connection doesn't outlive its render.
func (p *proxy) disarm() {
	p.mu.Lock()
	p.armed = false
	for c := range p.tunnels {
		_ = c.Close()
	}
	p.mu.Unlock()
}

// quietFor returns how long it has been since bytes last moved through the
// proxy, or since it started when none have.
func (p *proxy) quietFor() time.Duration {
	return time.Since(time.Unix(0, p.lastByte.Load()))
}

// touch records that bytes moved now.
func (p *proxy) touch() { p.lastByte.Store(time.Now().UnixNano()) }

// activity is an io.Writer that passes bytes on to w and records, in its
// proxy, that bytes moved.
type activity struct {
	w io.Writer
	p *proxy
}

// Write passes b on and touches the proxy.
func (a activity) Write(b []byte) (int, error) {
	a.p.touch()
	return a.w.Write(b)
}

// counts returns how many connections the proxy opened and refused.
func (p *proxy) counts() (requests, refused int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests, p.refused
}

// close stops the server and every connection it holds, and frees the
// port.
func (p *proxy) close() {
	p.disarm()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
	_ = p.srv.Close()
	p.tr.CloseIdleConnections()
	p.copies.Wait()
}

// ServeHTTP handles one request from Chrome: CONNECT opens a tunnel for
// HTTPS and WebSocket, and GET, HEAD and POST with a full http:// URL go
// on as plain HTTP. Anything else, anything from off this machine, and
// anything while the proxy isn't armed gets refused.
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !fromLoopback(r.RemoteAddr) {
		http.Error(w, "Meru's page reader serves only this machine", http.StatusForbidden)
		return
	}
	host := r.Host
	if !p.isArmed() {
		p.refuse(host, "no page is loading")
		http.Error(w, "Meru's page reader isn't loading a page", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodConnect:
		p.tunnel(w, r)
	case (r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodPost) &&
		r.URL.IsAbs() && r.URL.Scheme == "http":
		p.forward(w, r)
	default:
		http.Error(w, "Meru's page reader passes on only CONNECT, GET, HEAD and POST", http.StatusMethodNotAllowed)
	}
}

// isArmed reports whether the proxy lets connections through.
func (p *proxy) isArmed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.armed
}

// refuse counts and logs one refused connection. The host goes to the log
// at debug only: a page's hosts say what the user read.
func (p *proxy) refuse(host, why string) {
	p.mu.Lock()
	p.refused++
	p.mu.Unlock()
	p.log.Debug("render proxy refused", "host", host, "reason", why)
}

// tunnel answers CONNECT host:port: it dials host:port through the check
// and, once connected, copies bytes both ways until either side closes.
// The proxy never sees inside the tunnel, which carries TLS end to end
// between Chrome and the site.
func (p *proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	defer cancel()
	upstream, err := p.dial(ctx, "tcp", r.Host)
	if err != nil {
		p.refuse(r.Host, err.Error())
		http.Error(w, "Meru refused or couldn't reach "+r.Host, http.StatusBadGateway)
		return
	}
	// Hijack takes the raw connection back from the HTTP server, so the
	// proxy can copy bytes on it as it likes.
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "the proxy can't open a tunnel", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if !p.track(client, upstream) {
		// Disarmed while dialing: the render is over.
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		p.untrack(client, upstream)
		p.copies.Done()
		p.copies.Done()
		return
	}
	// Two goroutines copy one direction each. When either side closes,
	// both connections close, which ends the other copy too.
	var once sync.Once
	done := func() { once.Do(func() { p.untrack(client, upstream) }) }
	go func() { defer p.copies.Done(); _, _ = io.Copy(activity{upstream, p}, client); done() }()
	go func() { defer p.copies.Done(); _, _ = io.Copy(activity{client, p}, upstream); done() }()
}

// track adds a tunnel's two connections, counts one request, and adds
// the tunnel's two copy goroutines to p.copies. It reports false when the
// proxy was disarmed in the meantime. The Add happens under mu, which
// disarm also takes, so it always comes before close's Wait, as a
// WaitGroup requires.
func (p *proxy) track(a, b net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.armed {
		return false
	}
	p.tunnels[a] = struct{}{}
	p.tunnels[b] = struct{}{}
	p.requests++
	p.copies.Add(2)
	return true
}

// untrack closes a tunnel's two connections and forgets them.
func (p *proxy) untrack(a, b net.Conn) {
	p.mu.Lock()
	delete(p.tunnels, a)
	delete(p.tunnels, b)
	p.mu.Unlock()
	_ = a.Close()
	_ = b.Close()
}

// forward sends one plain-HTTP request on, with its hop-by-hop headers
// dropped, and copies the answer back.
func (p *proxy) forward(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	// A request a server receives has RequestURI set; one a client sends
	// must not.
	out.RequestURI = ""
	dropHop(out.Header)
	resp, err := p.tr.RoundTrip(out)
	if err != nil {
		p.refuse(r.Host, err.Error())
		http.Error(w, "Meru refused or couldn't reach "+r.Host, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	dropHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(activity{w, p}, resp.Body)
}

// dropHop removes the hop-by-hop headers from h, and any header the
// Connection header names as one.
func dropHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

// fromLoopback reports whether a request's remote address is on this
// machine.
func fromLoopback(remote string) bool {
	ap, err := netip.ParseAddrPort(remote)
	return err == nil && ap.Addr().Unmap().IsLoopback()
}
