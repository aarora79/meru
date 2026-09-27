// This file holds the two web tools: web_search, which searches the web
// through the SearXNG instance the user runs on this machine, and
// web_fetch, which fetches one public web page and returns its text, or
// asks the fast model a question about it. [builtin] tools turns each on;
// both are on by default, and web_search also needs [web] searxng_url.
// web_fetch's download mode lives in webdownload.go, and the
// guard that decides when it asks first in webguard.go. See
// ARCHITECTURE.md, "Web search" and "Privacy boundary".

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
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/obs"
)

// The web tools' names, as the model sees them.
const (
	WebSearch = "web_search"
	WebFetch  = "web_fetch"
)

// Limits on the web tools. The timeouts bound how long a turn waits; the
// body caps bound what merud holds in memory, or writes to disk, for one
// call.
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
	// maxRedirects is how many redirects web_fetch follows.
	maxRedirects = 5
	// userAgent names Meru to the sites web_fetch reads.
	userAgent = "Meru/0.3 (personal assistant; web_fetch)"
	// promptChars is how much of a page's text web_fetch hands the fast
	// model with a prompt: about 12,000 tokens, which fits the lite
	// model's context with room for the answer and covers most articles
	// and release notes whole. A longer page gets an answer from its first
	// part, and the result gives the offset for the rest.
	promptChars = 48000
	// answerTokens caps the fast model's answer to a prompt. A fact or a
	// short summary needs far less; the cap stops a model that rambles.
	answerTokens = 600
)

// timeRanges lists the time_range values SearXNG accepts.
var timeRanges = []string{"day", "week", "month", "year"}

// webSearchArgs and webFetchArgs are the JSON objects the model sends.
// The `json:"..."` struct tags name the JSON keys.
type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	TimeRange  string `json:"time_range"`
}

type webFetchArgs struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
	Offset int    `json:"offset"`
	Save   bool   `json:"save"`
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

// Generator is the one engine method web_fetch needs to answer a prompt.
// merud passes its engine.Engine; tests pass a fake. The interface lives
// here, in the package that calls it, with only the method it calls.
type Generator interface {
	Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error)
}

// webClients holds the HTTP clients of the two web tools and the URLs
// each session has seen. New builds them from [web]; tests swap allowAddr
// to reach an httptest server as if it were a public address.
type webClients struct {
	searxngURL string // "" leaves web_search out
	maxResults int

	search *http.Client // to SearXNG only
	// page reaches public addresses only. It has no Timeout of its own:
	// each call puts a deadline on its ctx, 20 seconds for text and 2
	// minutes for a download.
	page *http.Client

	// allowAddr decides, at dial time, whether web_fetch may connect to an
	// address. It is checkPublic in merud.
	allowAddr func(netip.AddrPort) error

	// known holds, per session, the URLs web_fetch may fetch without
	// asking; see webguard.go.
	known *knownURLs

	// model answers web_fetch's prompt, and fastModel names the model it
	// uses. UseModel sets both; with model nil, a prompt fails.
	model     Generator
	fastModel string
}

// newWebClients builds the web tools' clients from cfg.
func newWebClients(cfg config.Web) *webClients {
	w := &webClients{
		searxngURL: strings.TrimSuffix(cfg.SearXNGURL, "/"),
		maxResults: cfg.MaxResults,
		// A function literal: allowAddr holds a small function, which
		// tests replace.
		allowAddr: func(ap netip.AddrPort) error { return checkPublic(ap.Addr()) },
		known:     newKnownURLs(),
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

// UseModel lets web_fetch answer a prompt: g runs the model, and model is
// the fast model's name from config, the one the router uses. merud calls
// it once, before the first turn. Without it, web_fetch still returns a
// page's text, and a call with a prompt fails with a message that says to
// leave the prompt out.
//
// It is a method rather than a parameter of New so that the tests that
// build Tools without a model don't change.
func (t *Tools) UseModel(g Generator, model string) {
	t.web.model = g
	t.web.fastModel = model
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
				return fmt.Errorf("web_fetch: can't read the address %q: %w", address, err)
			}
			return w.allowAddr(ap)
		},
	}
}

// notPublicError is the error checkPublic returns. get finds it with
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
// lines for the model. It adds each result's URL to the session's known
// URLs, so web_fetch can read those pages without asking. It fails, with
// text the model reads, on bad arguments, when SearXNG doesn't answer or
// answers HTML, and when the answer isn't SearXNG's JSON.
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
	// Only the results the model sees count as known.
	shown := ans.Results[:min(a.MaxResults, len(ans.Results))]
	urls := make([]string, len(shown))
	for i, r := range shown {
		urls[i] = r.URL
	}
	t.web.known.add(dispatch.SessionFrom(ctx), urls...)
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

// parseWebURL checks the URL the model passed and returns it parsed. It
// fails, with text the model reads, for anything but a full http:// or
// https:// address with a host.
func parseWebURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil:
		return nil, fmt.Errorf("web_fetch: %q isn't a web address; pass a full http:// or https:// URL", raw)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("web_fetch: %q must start with http:// or https://", raw)
	case u.Host == "":
		return nil, fmt.Errorf("web_fetch: %q has no host; pass a full http:// or https:// URL", raw)
	}
	return u, nil
}

// webFetch runs one web_fetch call in one of its three modes: save
// downloads the file (see download); a prompt gets the fast model's answer
// from the page's text (see answerFromPage); otherwise it returns up to
// maxReadChars characters of the page's text from offset, headed by the
// final URL, the title and the size. It fails, with text the model reads,
// on bad arguments, a private or loopback address, a failed fetch, a page
// over 5 MiB and a content type it doesn't read.
func (t *Tools) webFetch(ctx context.Context, raw json.RawMessage) (string, error) {
	var a webFetchArgs
	if err := decode(WebFetch, raw, &a); err != nil {
		return "", err
	}
	u, err := parseWebURL(a.URL)
	if err != nil {
		return "", err
	}
	a.Prompt = strings.TrimSpace(a.Prompt)
	switch {
	case a.Offset < 0:
		return "", fmt.Errorf("web_fetch: offset %d is negative", a.Offset)
	case a.Save && (a.Prompt != "" || a.Offset != 0):
		return "", errors.New("web_fetch: save takes url alone; download first, then read the file with read_file")
	case a.Save:
		return t.download(ctx, u.String())
	}

	page, err := t.web.fetchPage(ctx, u.String())
	if err != nil {
		return "", err
	}
	runes := []rune(page.text)
	if a.Offset > len(runes) {
		return "", fmt.Errorf("web_fetch: offset %d is past the end; the page has %d characters", a.Offset, len(runes))
	}
	if a.Prompt != "" {
		return t.web.answerFromPage(ctx, page, runes, a.Prompt, a.Offset)
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
		fmt.Fprintf(&b, "\n\n[%d more characters. To read on, call web_fetch with offset %d.]", len(runes)-end, end)
	}
	return b.String(), nil
}

// fetchInstructions is the system message for a prompt. It is short on
// purpose: a 2B model follows a few plain rules better than ten. The
// line about the latest entry comes from a live miss: on go.dev's release
// history, which lists go1.27.1 under a go1.27.0 heading, the lite model
// answered with the heading. It still does at times; see
// docs/coding-notes/builtin.md.
const fetchInstructions = "You answer a question using only the web page text the user gives you. " +
	"Quote numbers, versions, names and dates exactly as the page writes them. " +
	"For the latest or newest of something, compare the dates of every entry, minor revisions included. " +
	"If the page doesn't answer the question, say that the page doesn't say. " +
	"Answer in a few sentences."

// answerFromPage asks the fast model to answer prompt from the page's
// text, starting at offset and cut to promptChars characters, and returns
// "From <final URL> (fetched <date>): <answer>". When the page has text
// past the part the model read, the result says so and gives the offset
// for the rest.
//
// The call runs with thinking off, at temperature 0, under a gen_ai.chat
// span that nests under dispatch's meru.dispatch span, because ctx
// carries it. It fails when UseModel wasn't called or the model call
// fails.
//
// The tokens it uses don't join the turn's usage: the router and the
// skill pick, the other fast-model calls in a turn, don't either, and
// passing counts back through dispatch.Result would widen that type for
// one tool. The span records them.
func (w *webClients) answerFromPage(ctx context.Context, page fetched, runes []rune, prompt string, offset int) (string, error) {
	if w.model == nil {
		return "", errors.New("web_fetch: no model is set up to answer a prompt; call again without prompt to read the text")
	}
	end := min(offset+promptChars, len(runes))
	msgs := []engine.Message{
		{Role: engine.RoleSystem, Content: fetchInstructions},
		// The question goes both before and after the page, so the model
		// reads the page knowing what to look for and still has the
		// question fresh when it answers.
		{Role: engine.RoleUser, Content: "Question: " + prompt + "\n\nText of " + page.url + ":\n\n" +
			string(runes[offset:end]) + "\n\nQuestion: " + prompt},
	}

	cctx, chat := obs.StartChat(ctx, obs.Chat{Tier: "fast", Model: w.fastModel, MaxTokens: answerTokens})
	zero := 0.0
	comp, err := w.model.Generate(cctx, msgs, nil, engine.Options{
		Model: w.fastModel, MaxTokens: answerTokens, Temperature: &zero, NoThink: true,
	})
	if err != nil {
		obs.EndSpanErr(cctx, chat, err)
		chat.End()
		return "", fmt.Errorf("web_fetch: the model couldn't answer from %s: %v", page.url, err)
	}
	u := comp.Usage
	obs.ChatResult(chat, obs.Usage{
		PromptTokens: u.PromptTokens, OutputTokens: u.OutputTokens,
		LoadDuration: u.LoadDuration, PromptEvalDuration: u.PromptEvalDuration,
		EvalDuration: u.EvalDuration,
	}, comp.DoneReason)
	chat.End()

	answer := strings.TrimSpace(comp.Text)
	if answer == "" {
		answer = "(the model gave no answer)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From %s (fetched %s): %s", page.url, time.Now().Format("2006-01-02"), answer)
	if offset > 0 || end < len(runes) {
		fmt.Fprintf(&b, "\n\n[This answer covers characters %d to %d of the page's %d.", offset, end, len(runes))
		if end < len(runes) {
			fmt.Fprintf(&b, " For the rest, call web_fetch with the same prompt and offset %d.", end)
		}
		b.WriteString("]")
	}
	return b.String(), nil
}

// fetched is one page web_fetch read: where it ended up, its title, how
// many bytes came back, what kind of page it was, and its text.
type fetched struct {
	url, title, kind, text string
	size                   int
}

// textTypes are the content types web_fetch turns into text.
var textTypes = []string{"text/html", "application/xhtml+xml", "application/pdf", "text/plain"}

// get sends a GET for rawURL through the page client and returns the
// response, with its body still to read, and the URL it ended at after
// any redirects. ctx must carry the call's deadline. It fails when the
// address isn't public, the request fails, or the answer isn't 200.
func (w *webClients) get(ctx context.Context, rawURL, accept string, limit time.Duration) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("web_fetch: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	resp, err := w.page.Do(req)
	if err != nil {
		// errors.As finds a *notPublicError anywhere in err's chain and
		// points np at it.
		var np *notPublicError
		switch {
		case errors.As(err, &np):
			return nil, "", fmt.Errorf("web_fetch: refused %s: %s. Meru reads only public pages, "+
				"never ones on this machine or your local network", rawURL, np.msg)
		case isTimeout(err):
			return nil, "", fmt.Errorf("web_fetch: %s took longer than %v", rawURL, limit)
		}
		return nil, "", fmt.Errorf("web_fetch: %v", err)
	}
	final := resp.Request.URL.String() // after any redirects
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("web_fetch: %s answered %s", final, resp.Status)
	}
	return resp, final, nil
}

// fetchPage GETs rawURL and converts the body to text. It fails when get
// fails, the body passes pageBodyCap or takes past pageTimeout, or the
// content type isn't HTML, PDF or plain text.
func (w *webClients) fetchPage(ctx context.Context, rawURL string) (fetched, error) {
	// WithTimeout returns a ctx that ends after pageTimeout; cancel frees
	// its timer when fetchPage returns. The deadline covers the body too.
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	resp, final, err := w.get(ctx, rawURL, "text/html, application/xhtml+xml, application/pdf, text/plain;q=0.9", pageTimeout)
	if err != nil {
		return fetched{}, err
	}
	defer resp.Body.Close()
	// A header that can't be parsed leaves mediaType "", which the check
	// below refuses.
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !slices.Contains(textTypes, mediaType) {
		return fetched{}, fmt.Errorf("web_fetch: %s is %q; Meru reads HTML, PDF and plain text pages. "+
			"To keep the file itself, call web_fetch with save", final, mediaType)
	}
	body, err := readCapped(resp.Body, pageBodyCap)
	if err != nil {
		if isTimeout(err) {
			return fetched{}, fmt.Errorf("web_fetch: %s took longer than %v", final, pageTimeout)
		}
		return fetched{}, fmt.Errorf("web_fetch: %s: %v", final, err)
	}
	p, err := pageText(mediaType, body)
	if err != nil {
		return fetched{}, fmt.Errorf("web_fetch: %s: %v", final, err)
	}
	p.url = final
	return p, nil
}

// pageText turns a body of one of the textTypes into text, with the
// page's title for HTML. HTML and PDF go through the indexer's own
// readers. It fails when a PDF can't be read.
func pageText(mediaType string, body []byte) (fetched, error) {
	p := fetched{size: len(body)}
	switch mediaType {
	case "application/pdf":
		// The indexer's PDF reader works on bytes in memory, so no
		// temporary file is needed.
		pages, err := index.PDFText(body)
		if err != nil {
			return fetched{}, err
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

// isTimeout reports whether err is a network timeout, such as a ctx
// deadline passing.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// webFetchDescription tells the model what web_fetch does and when to
// pass a prompt. saveDir is where a download goes; "" leaves save out.
func webFetchDescription(saveDir string) string {
	d := "Fetches one public web page, HTML, PDF or plain text. " +
		"Pass prompt when you want one fact or a summary: Meru reads the page and answers your prompt from the page alone, " +
		"quoting its numbers, versions and dates. Leave prompt out when you need the text itself: " +
		"you get up to 12,000 characters per call, and a longer page ends with the offset to pass next. " +
		"A URL that came from a web_search result or from the user, or another page with no query string on the same site, runs at once; any other URL asks the user first. "
	if saveDir != "" {
		d += "Set save to true only when the user asks to download a file: it saves the file in " + saveDir +
			" and always asks the user first. "
	}
	return d + "Cite the page by its URL in your answer."
}

// toolSpecs returns the specs of the web tools: web_search when [web]
// names a SearXNG URL, and web_fetch always. Tools then drops any that
// [builtin] tools leaves out. saveDir is where web_fetch saves downloads;
// "" leaves save out.
func (w *webClients) toolSpecs(saveDir string) []engine.ToolSpec {
	var specs []engine.ToolSpec
	if w.searxngURL != "" {
		specs = append(specs, engine.ToolSpec{
			Name: WebSearch,
			Description: "Searches the web and returns numbered results, each with a title, URL, snippet and, when known, the date. " +
				"Use it for news, recent events, prices, releases and anything else your training data may not hold or may hold out of date. " +
				"When the user names a product, company or person, search for the name as the user wrote it, in double quotes, and never swap in a name you know. " +
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
	props := map[string]any{
		"url": prop("string", "The page's full http:// or https:// address, such as a URL from web_search."),
		"prompt": prop("string", "What to find on the page, such as \"What is the latest stable version, and when was it released?\". "+
			"Leave it out to get the page's text."),
		"offset": prop("integer", "Where to start, in characters from the start of the page's text. Default 0."),
	}
	if saveDir != "" {
		props["save"] = prop("boolean", "Download the file to the user's disk instead of reading it. Only when the user asks for a download.")
	}
	specs = append(specs, engine.ToolSpec{
		Name:        WebFetch,
		Description: webFetchDescription(saveDir),
		Parameters:  mustSchema(props, "url"),
	})
	return specs
}
