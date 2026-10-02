// This file holds the Renderer: install the browser once, then for each
// page start a proxy and a fresh Chrome, load the page, wait for its text
// to settle, read its HTML, and stop both.

package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/obs"
)

// ErrClosed means Close has run: merud is stopping.
var ErrClosed = errors.New("the page reader is shut down")

// Timing of one render.
const (
	// pollEvery is how often waitForText reads the page's text length.
	pollEvery = 250 * time.Millisecond
	// settleFor is how long the text length must hold still, once the
	// page has loaded, before the reader takes it.
	settleFor = 750 * time.Millisecond
	// quietFor is how long no bytes may move through the proxy before the
	// reader takes the page. A page such as a Workday job posting shows a
	// skeleton, holds still, then fetches its text: the text alone looks
	// settled while that fetch is still on the wire.
	quietFor = 500 * time.Millisecond
	// settleCap ends the wait this long after navigation even if the text
	// keeps changing, as on a page with a ticking clock.
	settleCap = 8 * time.Second
	// maxHTML refuses a rendered page larger than 5 MiB, as web_fetch
	// refuses a fetched one.
	maxHTML = 5 << 20
)

// InstallLine is the progress line a render sends while it installs the
// browser. Both clients show it under the running tool.
const InstallLine = "Installing Meru's page reader (about 95 MB, once)"

// Page is what Render returns: the URL the page ended at after any
// redirects its scripts made, its title, its HTML once the text settled,
// and what the proxy did for it.
type Page struct {
	URL, Title, HTML string
	// Requests and Refused count the connections the proxy opened for
	// the page and the ones it refused.
	Requests, Refused int
	// Installed is true when this call installed the browser.
	Installed bool
}

// Renderer installs the pinned browser on first use and renders one page
// at a time, each in its own Chrome. It is a struct, not an interface:
// there is one implementation, and builtin declares the one-method
// interface it needs. Build one with New.
type Renderer struct {
	inst *connectors.Installer
	dial DialFunc
	log  *slog.Logger

	// ctx ends when Close runs. Render watches it beside its caller's
	// ctx, so Close never waits behind a render that is installing or
	// loading.
	ctx    context.Context
	cancel context.CancelFunc

	// one holds a token while a render runs: a channel with room for one
	// value works as a lock that a select can give up on when ctx ends.
	one chan struct{}

	// mu guards closed and the Add on wg, so no render starts after Close
	// begins to wait.
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// New returns a Renderer that installs the browser through inst and
// connects the page's requests through dial, which merud gives as
// web_fetch's public dialer.
func New(inst *connectors.Installer, dial DialFunc, log *slog.Logger) *Renderer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Renderer{inst: inst, dial: dial, log: log, ctx: ctx, cancel: cancel, one: make(chan struct{}, 1)}
}

// Ready reports whether the browser is installed, so web_fetch can give a
// first render the time an install takes.
func (r *Renderer) Ready() bool {
	_, ok := r.inst.RuntimeDir(connectors.RuntimeChrome)
	return ok
}

// Close cancels every render in flight, waits for them to stop, and makes
// later calls fail with ErrClosed. merud calls it on shutdown. It always
// returns nil; the error is there so Renderer fits io.Closer.
func (r *Renderer) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
	return nil
}

// Render installs the browser if needed, starts a proxy and Chrome, loads
// rawURL, returns its HTML once the visible text stops changing, and stops
// Chrome and the proxy before it returns. progress, when not nil, gets
// InstallLine while the browser installs. ctx bounds the whole call. It
// fails when the install fails, Chrome won't start, the page doesn't load
// in time, ctx ends, Close runs, or the platform has no pin (Windows).
func (r *Renderer) Render(ctx context.Context, rawURL string, progress func(string)) (page Page, err error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Page{}, ErrClosed
	}
	r.wg.Add(1)
	r.mu.Unlock()
	defer r.wg.Done()

	// The render ends when the caller's ctx ends or when Close runs.
	// context.AfterFunc calls cancel once r.ctx ends; stop undoes that
	// when Render returns first.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()

	start := time.Now()
	host := hostOf(rawURL)
	ctx, span := obs.Tracer().Start(ctx, "meru.web.render")
	defer func() {
		outcome := outcomeOf(err)
		obs.RecordRender(ctx, outcome, time.Since(start))
		span.SetAttributes(
			attribute.String("url.domain", host),
			attribute.Bool("meru.render.installed", page.Installed),
			attribute.Int("meru.render.requests", page.Requests),
			attribute.Int("meru.render.refused", page.Refused),
			attribute.Int("meru.render.chars", len(page.HTML)),
			attribute.String("meru.outcome", outcome),
		)
		if err != nil {
			obs.EndSpanErr(ctx, span, err)
			r.log.Warn("render failed", "host", host, "ms", time.Since(start).Milliseconds(), "err", err)
		} else {
			r.log.Info("page rendered", "host", host, "ms", time.Since(start).Milliseconds(),
				"chars", len(page.HTML), "requests", page.Requests, "refused", page.Refused)
		}
		span.End()
	}()

	// Wait for any other render to finish; give up when ctx ends.
	select {
	case r.one <- struct{}{}:
		defer func() { <-r.one }()
	case <-ctx.Done():
		return Page{}, cancelled(ctx, r.ctx)
	}

	dir, installed, err := r.install(ctx, progress)
	if err != nil {
		return Page{}, err
	}
	page, err = r.load(ctx, filepath.Join(dir, connectors.Runtimes()[connectors.RuntimeChrome].Program), rawURL, span)
	page.Installed = installed
	if err != nil && ctx.Err() != nil {
		err = cancelled(ctx, r.ctx)
	}
	return page, err
}

// install returns the browser's folder, installing it first when it
// isn't there, and whether it did.
func (r *Renderer) install(ctx context.Context, progress func(string)) (string, bool, error) {
	if dir, ok := r.inst.RuntimeDir(connectors.RuntimeChrome); ok {
		return dir, false, nil
	}
	if progress != nil {
		progress(InstallLine)
	}
	r.log.Info("render: installing the page reader")
	t := time.Now()
	dir, err := r.inst.EnsureRuntime(ctx, connectors.RuntimeChrome, nil)
	if err != nil {
		return "", false, fmt.Errorf("install the page reader: %w", err)
	}
	r.log.Info("render: installed the page reader", "ms", time.Since(t).Milliseconds())
	return dir, true, nil
}

// load runs one page in its own proxy and Chrome. The defers run in the
// reverse of the order they were set: disarm the proxy, stop Chrome, stop
// the proxy, delete the profile.
func (r *Renderer) load(ctx context.Context, chromePath, rawURL string, span trace.Span) (page Page, err error) {
	runtimeDir := filepath.Join(r.inst.MeruDir, "runtime")
	home := filepath.Join(runtimeDir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Page{}, fmt.Errorf("page reader: %w", err)
	}
	// MkdirTemp makes the folder with mode 0700, so only the user can
	// read what Chrome keeps there.
	profile, err := os.MkdirTemp(runtimeDir, "chrome-profile-")
	if err != nil {
		return Page{}, fmt.Errorf("page reader: %w", err)
	}
	defer os.RemoveAll(profile)

	px, err := startProxy(r.dial, r.log)
	if err != nil {
		return Page{}, err
	}
	defer px.close()

	t := time.Now()
	b, err := startBrowser(ctx, chromePath, home, profile, px.addr())
	if err != nil {
		return Page{}, err
	}
	defer func() {
		if cerr := b.close(); cerr != nil {
			r.log.Warn("render: chrome exited", "err", cerr)
		}
	}()
	span.SetAttributes(attribute.Int64("meru.render.start_ms", time.Since(t).Milliseconds()))

	px.arm()
	defer px.disarm()
	defer func() { page.Requests, page.Refused = px.counts() }()

	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := b.c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return Page{}, fmt.Errorf("page reader: %w", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	// flatten puts the page's messages on the same pipe, tagged with the
	// session, so one connection drives both the browser and the page.
	if err := b.c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return Page{}, fmt.Errorf("page reader: %w", err)
	}
	s := attached.SessionID

	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := b.c.call(ctx, s, "Page.navigate", map[string]any{"url": rawURL}, &nav); err != nil {
		return Page{}, fmt.Errorf("page reader: %w", err)
	}
	if nav.ErrorText != "" {
		return Page{}, fmt.Errorf("the page reader couldn't load %s: %s", rawURL, nav.ErrorText)
	}
	if err := waitForText(ctx, b.c, s, px.quietFor); err != nil {
		return Page{}, err
	}

	var parts []string
	if err := evalJSON(ctx, b.c, s, `JSON.stringify([location.href, document.title, document.documentElement.outerHTML])`, &parts); err != nil {
		return Page{}, err
	}
	if len(parts) != 3 {
		return Page{}, errors.New("page reader: the page gave no HTML")
	}
	if len(parts[2]) > maxHTML {
		return Page{}, fmt.Errorf("the rendered page is larger than %d MiB, which is as much as Meru reads", maxHTML>>20)
	}
	return Page{URL: parts[0], Title: parts[1], HTML: parts[2]}, nil
}

// waitForText reads the page's load state and text length every
// pollEvery. It returns once the page has loaded, the length has held
// still for settleFor, and quiet says no bytes have moved through the
// proxy for quietFor; or settleCap after it began, whichever comes first.
// Scripts often fill a page after its load event, which is why it watches
// the text and the traffic, not the event.
func waitForText(ctx context.Context, c *conn, session string, quiet func() time.Duration) error {
	const probe = `JSON.stringify([document.readyState, document.body ? document.body.innerText.length : 0])`
	begin := time.Now()
	last, since := -1, time.Now()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		// A json.RawMessage holds the two values as they came: a string
		// and a number.
		var state []json.RawMessage
		if err := evalJSON(ctx, c, session, probe, &state); err == nil && len(state) == 2 {
			var ready string
			var n int
			_ = json.Unmarshal(state[0], &ready)
			_ = json.Unmarshal(state[1], &n)
			if n != last {
				last, since = n, time.Now()
			}
			if ready == "complete" && time.Since(since) >= settleFor && quiet() >= quietFor {
				return nil
			}
		}
		if time.Since(begin) >= settleCap {
			return nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// evalJSON runs expr, which must return a JSON string, in the page, and
// decodes that string into v. It fails when the script throws or the
// result isn't JSON.
func evalJSON(ctx context.Context, c *conn, session, expr string, v any) error {
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &res); err != nil {
		return fmt.Errorf("page reader: %w", err)
	}
	if res.ExceptionDetails != nil {
		return fmt.Errorf("page reader: the page's script failed: %s", res.ExceptionDetails.Text)
	}
	if err := json.Unmarshal([]byte(res.Result.Value), v); err != nil {
		return fmt.Errorf("page reader: %w", err)
	}
	return nil
}

// cancelled returns the error for a render that stopped early: ErrClosed
// when Close ended it, and the caller's ctx error otherwise.
func cancelled(ctx, closing context.Context) error {
	if closing.Err() != nil {
		return ErrClosed
	}
	return ctx.Err()
}

// outcomeOf names how a render ended, for the metric and the span.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled), errors.Is(err, ErrClosed):
		return "cancelled"
	}
	return "error"
}

// hostOf returns rawURL's host, for the log and the span; "" when it
// doesn't parse.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
