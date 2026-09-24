// This file holds the two web tools: web_search, which searches the web
// through the SearXNG instance the user runs on this machine, and
// web_url_read, which fetches one public web page and returns its text.
// web_search comes with any [web] searxng_url; web_url_read only when
// [web] read_pages is true, because it is the one tool that makes merud
// connect off this machine. See ARCHITECTURE.md, "Web search" and "Privacy
// boundary".

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
)

// The web tools' names, as the model sees them.
const (
	WebSearch  = "web_search"
	WebURLRead = "web_url_read"
)

// Limits on the web tools. The timeouts bound how long a turn waits; the
// body caps bound what merud holds in memory for one call.
const (
	// searchTimeout covers one SearXNG search. SearXNG waits for the
	// slowest engine it asks, which can take several seconds.
	searchTimeout = 15 * time.Second
	// searchBodyCap is far more than 20 results' JSON, which runs to about
	// 60 KB.
	searchBodyCap = 2 << 20
	// snippetChars cuts each result's snippet.
	snippetChars = 300
	// pageTimeout covers one page fetch, redirects included.
	pageTimeout = 20 * time.Second
	// pageBodyCap refuses a page larger than 5 MiB, which is past any
	// article and most PDFs worth reading page by page.
	pageBodyCap = 5 << 20
	// maxRedirects is how many redirects web_url_read follows.
	maxRedirects = 5
	// userAgent names Meru to the sites web_url_read reads.
	userAgent = "Meru/0.3 (personal assistant; web_url_read)"
)

// timeRanges lists the time_range values SearXNG accepts.
var timeRanges = []string{"day", "week", "month", "year"}

// webSearchArgs and webURLReadArgs are the JSON objects the model sends.
// The `json:"..."` struct tags name the JSON keys.
type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	TimeRange  string `json:"time_range"`
}

type webURLReadArgs struct {
	URL    string `json:"url"`
	Offset int    `json:"offset"`
}

// searxngAnswer is the part of SearXNG's JSON answer web_search reads.
type searxngAnswer struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		// PublishedDate is a pointer, so it can hold nil: SearXNG sends
		// null for a result it has no date for.
		PublishedDate *string `json:"publishedDate"`
	} `json:"results"`
}

// webClients holds the HTTP clients of the two web tools. New builds them
// from [web]; tests swap allowAddr to reach an httptest server as if it
// were a public address.
type webClients struct {
	searxngURL string // "" leaves web_search out
	maxResults int
	readPages  bool // false leaves web_url_read out

	search *http.Client // to SearXNG only
	page   *http.Client // to public addresses only

	// allowAddr decides, at dial time, whether web_url_read may connect
	// to an address. It is checkPublic in merud.
	allowAddr func(netip.AddrPort) error
}

// newWebClients builds the web tools' clients from cfg.
func newWebClients(cfg config.Web) *webClients {
	w := &webClients{
		searxngURL: strings.TrimSuffix(cfg.SearXNGURL, "/"),
		maxResults: cfg.MaxResults,
		readPages:  cfg.ReadPages,
		// A function literal: allowAddr holds a small function, which
		// tests replace.
		allowAddr: func(ap netip.AddrPort) error { return checkPublic(ap.Addr()) },
	}
	if w.maxResults < 1 {
		w.maxResults = 8
	}
	// SearXNG runs on this machine (config checks searxng_url), so the
	// search client uses no proxy and follows no redirect: a redirect
	// could only lead somewhere config never approved.
	w.search = &http.Client{
		Timeout:   searchTimeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("SearXNG answered with a redirect, which Meru doesn't follow")
		},
	}
	// The page client checks every address it connects to, after DNS,
	// in the dialer's Control function; see publicDialer. It has no
	// cookie jar, so no site can track the user from one call to the next,
	// and no proxy, so the check sees the real destination.
	w.page = &http.Client{
		Timeout: pageTimeout,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: w.publicDialer().DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("a redirect to %s isn't http or https", req.URL.Scheme)
			}
			return nil
		},
	}
	return w
}

// publicDialer returns a dialer that refuses any address allowAddr
// refuses. Control runs after DNS has turned the host name into an IP
// address and just before the connection opens, so the check sees the
// address the connection actually goes to. A name that resolves to
// 127.0.0.1 or 192.168.1.1 fails here, as does a redirect to one, and a
// name whose DNS answer changes between a check and the dial can't slip
// past, because there is no earlier check.
func (w *webClients) publicDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("web_url_read: can't read the address %q: %w", address, err)
			}
			return w.allowAddr(ap)
		},
	}
}

// notPublicError is the error checkPublic returns. fetch finds it with
// errors.As inside the error the HTTP client wraps around it, and shows
// the model its text alone.
type notPublicError struct{ msg string }

// Error returns the reason the address isn't public.
func (e *notPublicError) Error() string { return e.msg }

// notPublic builds a *notPublicError from a format and its values.
func notPublic(format string, args ...any) error {
	return &notPublicError{msg: fmt.Sprintf(format, args...)}
}

// privateRanges lists address blocks that checkPublic refuses on top of
// what netip's own tests catch: 0.0.0.0/8, which reaches this machine on
// Linux, and 100.64.0.0/10, the shared address space that carrier NAT and
// VPNs such as Tailscale use for machines on your own network.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
}

// checkPublic returns nil when a is a public unicast address, and an error
// for this machine (loopback), a private network (10/8, 172.16/12,
// 192.168/16, fc00::/7), link-local, unspecified, multicast and the
// blocks in privateRanges. Unmap turns an IPv4 address written as IPv6,
// ::ffff:127.0.0.1, back into IPv4 first.
func checkPublic(a netip.Addr) error {
	a = a.Unmap()
	switch {
	case a.IsLoopback():
		return notPublic("%s is this machine", a)
	case a.IsPrivate():
		return notPublic("%s is on a private network", a)
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
		return notPublic("%s is a link-local address", a)
	case a.IsUnspecified(), a.IsMulticast(), a.IsInterfaceLocalMulticast():
		return notPublic("%s isn't a single public machine", a)
	}
	for _, p := range privateRanges {
		if p.Contains(a) {
			return notPublic("%s is in %s, which isn't public", a, p)
		}
	}
	return nil
}

// webSearch runs one SearXNG search and returns the results as numbered
// lines for the model. It fails, with text the model reads, on bad
// arguments, when SearXNG doesn't answer or answers HTML, and when the
// answer isn't SearXNG's JSON.
func (t *Tools) webSearch(ctx context.Context, raw json.RawMessage) (string, error) {
	var a webSearchArgs
	if err := decode(WebSearch, raw, &a); err != nil {
		return "", err
	}
	a.Query = strings.TrimSpace(a.Query)
	switch {
	case a.Query == "":
		return "", errors.New("web_search: pass query, the words to search for")
	case a.MaxResults < 0 || a.MaxResults > config.MaxWebResults:
		return "", fmt.Errorf("web_search: max_results %d is out of range; pass 1 to %d", a.MaxResults, config.MaxWebResults)
	case a.TimeRange != "" && !slices.Contains(timeRanges, a.TimeRange):
		return "", fmt.Errorf("web_search: time_range %q is unknown; pass one of %s, or leave it out",
			a.TimeRange, strings.Join(timeRanges, ", "))
	}
	if a.MaxResults == 0 {
		a.MaxResults = t.web.maxResults
	}

	body, err := t.web.searxng(ctx, a)
	if err != nil {
		return "", err
	}
	var ans searxngAnswer
	if err := json.Unmarshal(body, &ans); err != nil {
		return "", fmt.Errorf("web_search: SearXNG's answer isn't the JSON Meru expects: %v", err)
	}
	return formatResults(a, ans), nil
}

// searxng sends the search to SearXNG and returns the JSON body. It turns
// each way the request can fail into a message that says what to do.
func (w *webClients) searxng(ctx context.Context, a webSearchArgs) ([]byte, error) {
	q := url.Values{}
	q.Set("q", a.Query)
	q.Set("format", "json")
	if a.TimeRange != "" {
		q.Set("time_range", a.TimeRange)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.searxngURL+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("web_search: %v", err)
	}
	resp, err := w.search.Do(req)
	if err != nil {
		// errors.As looks through err's chain of wrapped errors for a
		// *net.OpError and points op at it; a failed "dial" means nothing
		// accepted the connection.
		var op *net.OpError
		switch {
		case errors.As(err, &op) && op.Op == "dial":
			return nil, fmt.Errorf("web_search: SearXNG isn't answering on %s. %s", w.searxngURL, catalog.SearXNGDocs)
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			return nil, fmt.Errorf("web_search: SearXNG took longer than %v to answer; try again, or search with fewer words", searchTimeout)
		}
		return nil, fmt.Errorf("web_search: %v", err)
	}
	// defer runs resp.Body.Close() when this function returns, on every
	// path, so the connection goes back to the pool.
	defer resp.Body.Close()
	body, err := readCapped(resp.Body, searchBodyCap)
	if err != nil {
		return nil, fmt.Errorf("web_search: read SearXNG's answer: %v", err)
	}
	// A SearXNG with JSON off answers 403 with an HTML page; check the
	// body before the status, so that case gets the message that fixes it.
	if strings.HasPrefix(strings.TrimSpace(string(body[:min(len(body), 512)])), "<") {
		return nil, fmt.Errorf("web_search: %s", catalog.SearXNGFormatsHint)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web_search: SearXNG answered %s", resp.Status)
	}
	return body, nil
}

// formatResults writes up to a.MaxResults results as numbered lines:
// "[n] title — url", the snippet, and the date when SearXNG has one.
func formatResults(a webSearchArgs, ans searxngAnswer) string {
	if len(ans.Results) == 0 {
		return fmt.Sprintf("SearXNG found no results for %q. Try other words, or a wider time_range.", a.Query)
	}
	n := min(a.MaxResults, len(ans.Results))
	var b strings.Builder
	fmt.Fprintf(&b, "Web results for %q, %d of %d. Cite each result you use by its URL.\n", a.Query, n, len(ans.Results))
	for i, r := range ans.Results[:n] {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = "(no title)"
		}
		fmt.Fprintf(&b, "\n[%d] %s — %s\n", i+1, title, r.URL)
		if s := strings.Join(strings.Fields(r.Content), " "); s != "" {
			b.WriteString(cut(s, snippetChars) + "\n")
		}
		if r.PublishedDate != nil && len(*r.PublishedDate) >= 10 {
			// SearXNG writes ISO 8601 dates; the first ten characters
			// are the day.
			fmt.Fprintf(&b, "Published %s.\n", (*r.PublishedDate)[:10])
		}
	}
	return b.String()
}

// webURLRead fetches one public page and returns up to maxReadChars
// characters of its text from offset, headed by the final URL, the title
// and the size. It fails, with text the model reads, on bad arguments, a
// private or loopback address, a failed fetch, a page over 5 MiB and a
// content type it doesn't read.
func (t *Tools) webURLRead(ctx context.Context, raw json.RawMessage) (string, error) {
	var a webURLReadArgs
	if err := decode(WebURLRead, raw, &a); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(a.URL))
	switch {
	case err != nil:
		return "", fmt.Errorf("web_url_read: %q isn't a web address; pass a full http:// or https:// URL", a.URL)
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("web_url_read: %q must start with http:// or https://", a.URL)
	case u.Host == "":
		return "", fmt.Errorf("web_url_read: %q has no host; pass a full http:// or https:// URL", a.URL)
	case a.Offset < 0:
		return "", fmt.Errorf("web_url_read: offset %d is negative", a.Offset)
	}
	page, err := t.web.fetch(ctx, u.String())
	if err != nil {
		return "", err
	}

	runes := []rune(page.text)
	if a.Offset > len(runes) {
		return "", fmt.Errorf("web_url_read: offset %d is past the end; the page has %d characters", a.Offset, len(runes))
	}
	end := min(a.Offset+maxReadChars, len(runes))
	var b strings.Builder
	b.WriteString(page.url + "\n")
	if page.title != "" {
		b.WriteString("Title: " + page.title + "\n")
	}
	fmt.Fprintf(&b, "%s, %s. Characters %d to %d of %d.\n\n", size(int64(page.size)), page.kind, a.Offset, end, len(runes))
	b.WriteString(string(runes[a.Offset:end]))
	if end < len(runes) {
		fmt.Fprintf(&b, "\n\n[%d more characters. To read on, call web_url_read with offset %d.]", len(runes)-end, end)
	}
	return b.String(), nil
}

// fetched is one page web_url_read read: where it ended up, its title, how
// many bytes came back, what kind of page it was, and its text.
type fetched struct {
	url, title, kind, text string
	size                   int
}

// fetch GETs rawURL through the page client and converts the body to text.
// It fails when the address isn't public, the request fails or answers
// other than 200, the body passes pageBodyCap, or the content type isn't
// HTML, PDF or plain text.
func (w *webClients) fetch(ctx context.Context, rawURL string) (fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetched{}, fmt.Errorf("web_url_read: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html, application/xhtml+xml, application/pdf, text/plain;q=0.9")
	resp, err := w.page.Do(req)
	if err != nil {
		// errors.As finds a *notPublicError anywhere in err's chain and
		// points np at it.
		var np *notPublicError
		switch {
		case errors.As(err, &np):
			return fetched{}, fmt.Errorf("web_url_read: refused %s: %s. Meru reads only public pages, "+
				"never ones on this machine or your local network", rawURL, np.msg)
		case isTimeout(err):
			return fetched{}, fmt.Errorf("web_url_read: %s took longer than %v", rawURL, pageTimeout)
		}
		return fetched{}, fmt.Errorf("web_url_read: %v", err)
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String() // after any redirects
	if resp.StatusCode != http.StatusOK {
		return fetched{}, fmt.Errorf("web_url_read: %s answered %s", final, resp.Status)
	}
	// A header that can't be parsed leaves mediaType "", which the switch
	// below refuses.
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !slices.Contains([]string{"text/html", "application/xhtml+xml", "application/pdf", "text/plain"}, mediaType) {
		return fetched{}, fmt.Errorf("web_url_read: %s is %q; Meru reads HTML, PDF and plain text pages only", final, mediaType)
	}
	body, err := readCapped(resp.Body, pageBodyCap)
	if err != nil {
		return fetched{}, fmt.Errorf("web_url_read: %s: %v", final, err)
	}

	p := fetched{url: final, size: len(body)}
	switch mediaType {
	case "application/pdf":
		// The indexer's PDF reader works on bytes in memory, so no
		// temporary file is needed.
		pages, err := index.PDFText(body)
		if err != nil {
			return fetched{}, fmt.Errorf("web_url_read: %s: %v", final, err)
		}
		p.kind = "PDF"
		p.text = joinPages(index.Text{Kind: index.KindPDF, Pages: pages})
	case "text/plain":
		p.kind = "plain text"
		p.text = strings.ToValidUTF8(string(body), "�")
	default:
		p.kind = "HTML"
		p.title, p.text = index.HTMLText(strings.ToValidUTF8(string(body), "�"))
	}
	return p, nil
}

// readCapped reads r to the end, and fails when it holds more than limit
// bytes rather than returning a cut page.
func readCapped(r io.Reader, limit int) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("the page is larger than %s, which is as much as Meru reads", size(int64(limit)))
	}
	return body, nil
}

// isTimeout reports whether err is a network timeout, such as the
// client's Timeout passing.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// toolSpecs returns the specs of the web tools that [web] turns on.
func (w *webClients) toolSpecs() []engine.ToolSpec {
	var specs []engine.ToolSpec
	if w.searxngURL != "" {
		specs = append(specs, engine.ToolSpec{
			Name: WebSearch,
			Description: "Searches the web and returns numbered results, each with a title, URL, snippet and, when known, the date. " +
				"Use it for news, recent events, prices, releases and anything else your training data may not hold or may hold out of date. " +
				"In your answer, cite each result you use by its URL.",
			Parameters: mustSchema(map[string]any{
				"query": prop("string", "The words to search for, as you would type them into a search engine."),
				"max_results": prop("integer", fmt.Sprintf("How many results to return, 1 to %d. Default %d.",
					config.MaxWebResults, w.maxResults)),
				"time_range": map[string]any{
					"type":        "string",
					"enum":        timeRanges,
					"description": "Only results from the last day, week, month or year. Leave it out unless the question names such a period: a page that states the latest version is often months old.",
				},
			}, "query"),
		})
	}
	if w.readPages {
		specs = append(specs, engine.ToolSpec{
			Name: WebURLRead,
			Description: "Fetches one public web page, HTML, PDF or plain text, and returns its text. " +
				"Use it when a search snippet isn't enough and you need what the page says. " +
				"It returns up to 12,000 characters per call; when the page is longer, the result ends with the offset to pass next. " +
				"Cite the page by its URL in your answer.",
			Parameters: mustSchema(map[string]any{
				"url":    prop("string", "The page's full http:// or https:// address, such as a URL from web_search."),
				"offset": prop("integer", "Where to start, in characters from the start of the page's text. Default 0."),
			}, "url"),
		})
	}
	return specs
}
