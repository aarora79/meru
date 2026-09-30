// This file holds the SearXNG check: one request that says whether the
// SearXNG instance web search uses answers, and answers in JSON. `meru
// setup` runs it in its "Web search" step and merud's SearXNG connector
// runs it at start and every minute, so it lives here, where the thin
// client may import it. See ARCHITECTURE.md, "Web search" and "SearXNG and
// Ollama".

package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// SearXNGDocs names the part of the docs that shows how to start SearXNG.
// Error texts end with it, so a user or the model knows where to look.
const SearXNGDocs = `See "Web search" in docs/running.md.`

// SearXNGFormatsHint explains the one setting SearXNG needs for Meru. A
// fresh SearXNG answers only HTML, and refuses format=json with a 403.
const SearXNGFormatsHint = "SearXNG answered with a web page instead of JSON, because JSON is off. " +
	"Add json under search: formats: in SearXNG's settings.yml, then restart SearXNG. For the SearXNG " +
	"Meru runs, the file is ~/.meru/searxng/settings.yml and the restart is docker restart meru-searxng."

// Errors CheckSearXNG wraps, so callers can pick their message with
// errors.Is.
var (
	// ErrSearXNGDown means nothing accepted the connection.
	ErrSearXNGDown = errors.New("SearXNG isn't answering")
	// ErrSearXNGNoJSON means SearXNG answered, but with HTML.
	ErrSearXNGNoJSON = errors.New("SearXNG doesn't answer JSON")
)

// searxngCheckTimeout caps the check. A SearXNG on this machine answers a
// one-word search in about a second; three leaves room for a slow engine.
const searxngCheckTimeout = 3 * time.Second

// CheckSearXNG asks SearXNG at baseURL for an empty search with
// format=json and returns nil when a JSON object comes back. SearXNG
// refuses an empty query with 400 and {"error": "No query"} when JSON is
// on, and with a 403 HTML page when JSON is off, and in neither case asks
// any search engine. So the check tells the two apart and sends nothing
// off this machine, which matters because merud runs it every minute.
//
// It fails with ErrSearXNGDown when nothing listens, with ErrSearXNGNoJSON
// when SearXNG answers HTML, and with a plain error for anything else,
// such as a timeout or a 500. It follows no redirect and uses no proxy:
// the address is on this machine.
func CheckSearXNG(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, searxngCheckTimeout)
	defer cancel()
	u := strings.TrimSuffix(baseURL, "/") + "/search?q=&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("SearXNG URL %q: %w", baseURL, err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// A failed dial means nothing listens. errors.As finds a
		// *net.OpError anywhere in err's chain.
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return fmt.Errorf("%w on %s", ErrSearXNGDown, baseURL)
		}
		return fmt.Errorf("SearXNG on %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	head, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return fmt.Errorf("SearXNG on %s: read the answer: %w", baseURL, err)
	}
	return classify(resp.StatusCode, head, baseURL)
}

// classify reads a SearXNG answer's status and the start of its body. A
// JSON object with status 200 or 400 passes: SearXNG answers JSON; 400 is
// how it refuses the empty query. HTML, whatever the status, means JSON is
// off. Anything else is an error naming the status.
func classify(status int, head []byte, baseURL string) error {
	body := strings.TrimSpace(string(head))
	switch {
	case (status == http.StatusOK || status == http.StatusBadRequest) && strings.HasPrefix(body, "{"):
		return nil
	case strings.HasPrefix(body, "<"):
		return fmt.Errorf("%w on %s", ErrSearXNGNoJSON, baseURL)
	default:
		return fmt.Errorf("SearXNG on %s answered %d", baseURL, status)
	}
}
