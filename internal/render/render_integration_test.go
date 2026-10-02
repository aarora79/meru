//go:build integration

// This file runs the Renderer against the real, pinned
// chrome-headless-shell. The first run downloads it (about 95 MB) into a
// cache folder kept between runs. Run it with:
//
//	go test -race -tags integration -run Render ./internal/render/...

package render

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/index"
)

// testRenderer returns a Renderer whose Meru home is a cache folder shared
// by every run, so the browser downloads once, and whose dialer lets
// through only the addresses in allow. Everything else is refused, as
// web_fetch's check refuses this machine.
func testRenderer(t *testing.T, allow ...string) *Renderer {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	meruDir := filepath.Join(cache, "meru-render-test")
	if err := os.MkdirAll(meruDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		for _, a := range allow {
			if addr == a {
				return d.DialContext(ctx, network, addr)
			}
		}
		return nil, fmt.Errorf("%s isn't allowed in this test", addr)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(connectors.NewInstaller(meruDir, home), dial, log)
}

func TestRenderJavaScriptPage(t *testing.T) {
	// The page's text exists only after its script runs, half a second
	// after load.
	text := strings.Repeat("Principal engineer, Denver. ", 70)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><head><title>Job</title></head><body><div id=x></div>
<script>setTimeout(function(){document.getElementById('x').textContent=%q}, 500)</script></body></html>`, text)
	}))
	defer srv.Close()

	r := testRenderer(t, srv.Listener.Addr().String())
	defer r.Close()
	var lines []string
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	page, err := r.Render(ctx, srv.URL, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.HTML, "Principal engineer, Denver.") {
		t.Fatalf("the rendered HTML lacks the script's text: %.300s", page.HTML)
	}
	if page.Title != "Job" || page.Refused != 0 || page.Requests < 1 {
		t.Errorf("title %q, requests %d, refused %d", page.Title, page.Requests, page.Refused)
	}
	if page.Installed && (len(lines) != 1 || lines[0] != InstallLine) {
		t.Errorf("an install sent progress %q", lines)
	}
	t.Logf("installed=%v requests=%d chars=%d", page.Installed, page.Requests, len(page.HTML))
}

func TestRenderBlocksPrivate(t *testing.T) {
	// A second server on this machine holds a secret; the page's script
	// tries to read it and write it into the page.
	var hits atomic.Int32
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = io.WriteString(w, "TOPSECRET")
	}))
	defer secret.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><div id=x>waiting</div><script>
fetch(%q).then(r=>r.text()).then(t=>{document.getElementById('x').textContent=t})
  .catch(e=>{document.getElementById('x').textContent='blocked'})</script></body></html>`, secret.URL+"/secret")
	}))
	defer page.Close()

	r := testRenderer(t, page.Listener.Addr().String())
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	got, err := r.Render(ctx, page.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.HTML, "TOPSECRET") || hits.Load() != 0 {
		t.Fatalf("the page read the private server: hits %d, html %.200s", hits.Load(), got.HTML)
	}
	if got.Refused < 1 {
		t.Errorf("refused = %d, want at least 1", got.Refused)
	}
	if !strings.Contains(got.HTML, "blocked") {
		t.Errorf("the page's fetch didn't fail: %.200s", got.HTML)
	}
}

func TestRenderLeavesNoChrome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html><body>hello</body></html>")
	}))
	defer srv.Close()
	r := testRenderer(t, srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := r.Render(ctx, srv.URL, nil); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if out, _ := exec.Command("pgrep", "-f", "meru-render-test.*chrome-headless-shell").Output(); len(out) > 0 {
		t.Fatalf("Chrome still runs after the render: %s", out)
	}
}

// TestRenderNoBackgroundTraffic starts Chrome with its proxy disarmed and
// waits: whatever Chrome tries to send on its own must get no connection.
func TestRenderNoBackgroundTraffic(t *testing.T) {
	r := testRenderer(t)
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir, _, err := r.install(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tries atomic.Int32
	px, err := startProxy(func(ctx context.Context, network, addr string) (net.Conn, error) {
		tries.Add(1)
		return nil, fmt.Errorf("no dial in this test")
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer px.close()
	profile := t.TempDir()
	b, err := startBrowser(ctx, filepath.Join(dir, "chrome-headless-shell"), t.TempDir(), profile, px.addr())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second)
	_ = b.close()
	requests, refused := px.counts()
	if requests != 0 || tries.Load() != 0 {
		t.Fatalf("Chrome got %d connections and %d dials with the proxy disarmed", requests, tries.Load())
	}
	t.Logf("refused while disarmed: %d", refused)
}

// TestRenderNoWebRTC loads a page that asks a STUN server on this machine
// for its address over UDP. WebRTC's UDP must not get around the proxy.
func TestRenderNoWebRTC(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	var packets atomic.Int32
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := udp.ReadFrom(buf); err != nil {
				return
			}
			packets.Add(1)
		}
	}()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><div id=x>start</div><script>
const pc = new RTCPeerConnection({iceServers:[{urls:'stun:%s'}]});
pc.createDataChannel('x');
pc.onicecandidate = e => { if (e.candidate) document.getElementById('x').textContent += ' ' + e.candidate.type; };
pc.createOffer().then(o => pc.setLocalDescription(o));
setTimeout(() => { document.getElementById('x').textContent += ' done'; }, 3000);
</script></body></html>`, udp.LocalAddr().String())
	}))
	defer srv.Close()
	r := testRenderer(t, srv.Listener.Addr().String())
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	page, err := r.Render(ctx, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if packets.Load() != 0 {
		t.Fatalf("WebRTC sent %d UDP packets around the proxy", packets.Load())
	}
	t.Logf("candidates seen by the page: %.120s", page.HTML[strings.Index(page.HTML, "start"):])
}

// TestRenderWorkday renders the Workday job page that led to this
// feature. It reaches the public web, so it runs only with
// MERU_RENDER_NET=1, and its dialer allows any address.
func TestRenderWorkday(t *testing.T) {
	if os.Getenv("MERU_RENDER_NET") != "1" {
		t.Skip("set MERU_RENDER_NET=1 to reach the public web")
	}
	r := testRenderer(t)
	var d net.Dialer
	r.dial = d.DialContext
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const u = "https://blueorigin.wd5.myworkdayjobs.com/BlueOrigin/job/Denver-CO/Principal-Software-Engineer--Compute-Architect--Orbital-Data-Centers_R73070"
	start := time.Now()
	page, err := r.Render(ctx, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	// web_fetch reads the rendered HTML through the indexer's reader, so
	// the test checks the text it gets, where markup no longer splits
	// "Full time".
	title, text := index.HTMLText(page.HTML)
	for _, want := range []string{"R73070", "Denver", "Full time", "At Blue Origin"} {
		if !strings.Contains(text, want) {
			t.Errorf("the page's text lacks %q", want)
		}
	}
	t.Logf("%v, %d chars of text, %d requests, %d refused, title %q",
		time.Since(start).Round(time.Millisecond), len(text), page.Requests, page.Refused, title)
}
