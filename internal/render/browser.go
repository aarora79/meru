// This file starts chrome-headless-shell for one page and stops it. The
// flags keep Chrome to the one page: every request goes to the proxy,
// nothing talks to Google in the background, and nothing touches the
// user's own Chrome, profile or keychain.

package render

import (
	"context"
	"fmt"
	"time"

	"github.com/aarora79/meru/internal/connectors"
)

// healthTimeout bounds Chrome's start: from the program starting to its
// first reply over the pipe.
const healthTimeout = 10 * time.Second

// closeTimeout bounds the polite Browser.close before Stop takes over.
const closeTimeout = time.Second

// chromeFlags returns the command-line flags for one render. profile is a
// fresh folder that Chrome keeps its state in for this page alone, and
// proxyAddr is the proxy's "127.0.0.1:<port>".
func chromeFlags(profile, proxyAddr string) []string {
	return []string{
		// Commands and replies go over fd 3 and fd 4, so Chrome opens no
		// debugging port that another program could find.
		"--remote-debugging-pipe",
		"--user-data-dir=" + profile,
		// Every request goes to the proxy, which dials with web_fetch's
		// check. <-loopback removes Chrome's own exception for
		// localhost, so a page asking for 127.0.0.1 goes to the proxy too,
		// where it fails.
		"--proxy-server=http://" + proxyAddr,
		"--proxy-bypass-list=<-loopback>",
		// HTTP/3 (QUIC) and WebRTC use UDP, which an HTTP proxy doesn't
		// carry; these keep both off the network.
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		// Nothing in the background: no updates, sync, Safe Browsing
		// pings, field trials or reports.
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-client-side-phishing-detection",
		"--disable-features=OptimizationHints,MediaRouter,Translate,AutofillServerCommunication",
		"--no-first-run",
		"--no-pings",
		"--metrics-recording-only",
		// No keychain on macOS and no keyring on Linux: Chrome would ask
		// the user's to encrypt cookies, and on a Mac that shows a prompt.
		"--use-mock-keychain",
		"--password-store=basic",
		// Images add time and bytes, and the reader keeps only text.
		"--blink-settings=imagesEnabled=false",
		"--mute-audio",
		"--hide-scrollbars",
		"about:blank",
	}
}

// browser is one running Chrome and the DevTools connection to it.
type browser struct {
	p *connectors.Piped
	c *conn
}

// startBrowser starts chromePath with chromeFlags, an environment that
// holds HOME and nothing else, and checks that it answers. It fails when
// Chrome can't start or doesn't answer within healthTimeout.
func startBrowser(ctx context.Context, chromePath, home, profile, proxyAddr string) (*browser, error) {
	p, err := connectors.StartPiped(connectors.Cmd{
		Path: chromePath,
		Args: chromeFlags(profile, proxyAddr),
		// HOME points into ~/.meru/runtime, as for npm and uv, so any file
		// Chrome writes there stays out of the user's home.
		Env: []string{"HOME=" + home},
	})
	if err != nil {
		return nil, fmt.Errorf("start Chrome: %w", err)
	}
	b := &browser{p: p, c: newConn(p.In, p.Out)}

	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	var version struct {
		Product string `json:"product"`
	}
	if err := b.c.call(hctx, "", "Browser.getVersion", nil, &version); err != nil {
		return nil, fmt.Errorf("start Chrome: %w (%v)", err, b.close())
	}
	// A download would stay on disk; web_fetch's save mode is the one way
	// to keep a file.
	if err := b.c.call(hctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "deny"}, nil); err != nil {
		return nil, fmt.Errorf("start Chrome: %w (%v)", err, b.close())
	}
	return b, nil
}

// close asks Chrome to quit, then stops it through Stop, which kills it
// if it hasn't gone, and waits for the DevTools reader to end. It returns
// nil when Chrome exited cleanly, and otherwise its exit status and the
// last lines of its stderr.
func (b *browser) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	// Browser.close often gets no reply: Chrome exits first. Its error
	// says nothing useful.
	_ = b.c.call(ctx, "", "Browser.close", nil, nil)
	err := b.p.Stop()
	<-b.c.done
	return err
}
