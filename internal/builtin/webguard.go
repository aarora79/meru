// This file holds web_fetch's URL guard: the per-session set of URLs the
// model may fetch without asking, and ConfirmCall, which dispatch asks
// before each call. The guard stops the model, or a page that talks the
// model into it, from sending the user's data out inside a URL it made up,
// such as https://attacker.example/?notes=<your notes>. See
// ARCHITECTURE.md, "Web search".

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
)

// Bounds on the known-URL sets. merud runs for weeks, and each chat
// session leaves a set behind, so the sets need a cap. The oldest session
// goes first: a set left alone longest is the one least likely to be used
// again, and one that is lost only means the next fetch in that session
// asks. 256 sessions is more than anyone keeps open; 2,000 URLs is 100
// searches of 20 results.
const (
	maxURLSessions    = 256
	maxURLsPerSession = 2000
)

// knownURLs holds, per session, the URLs web_fetch may fetch without
// asking: each URL a web_search result showed and each URL the user wrote
// in a question. It also holds the hosts the user trusted: for one session
// ("This website for this chat"), or for all of them ("Always this
// website"), the [web] trusted_sites list. It lives only in memory, as session approvals do, so a
// restart of merud forgets it; the user's own questions come back with the
// next call (see ConfirmCall), and a search-result URL then asks once.
type knownURLs struct {
	// mu guards sessions and always. Tool calls in one round run side by
	// side.
	mu       sync.Mutex
	sessions map[string]*sessionURLs
	// always holds the hosts trusted in every session, by host name.
	always map[string]bool
}

// sessionURLs is one session's set, and when it was last used.
type sessionURLs struct {
	urls map[string]bool
	used time.Time
}

// newKnownURLs returns an empty set.
func newKnownURLs() *knownURLs {
	return &knownURLs{sessions: map[string]*sessionURLs{}, always: map[string]bool{}}
}

// trustKey is the key a host trusted for one session is kept under in the
// session's set, such as "trust:arxiv.org". The prefix keeps it apart from
// the URLs and the "host:" keys of known URLs.
func trustKey(host string) string { return "trust:" + host }

// urlHost returns the host name of an http or https URL, in lower case and
// without a port, or "" when raw isn't one.
func urlHost(raw string) string {
	n, ok := normalizeURL(raw)
	if !ok {
		return ""
	}
	u, err := url.Parse(n)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// trust trusts host in session: later reads on that host, query strings
// included, run without asking until the session's set goes.
func (k *knownURLs) trust(session, host string) {
	if session == "" || host == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	s := k.sessions[session]
	if s == nil {
		if len(k.sessions) >= maxURLSessions {
			k.dropOldest()
		}
		s = &sessionURLs{urls: map[string]bool{}}
		k.sessions[session] = s
	}
	s.used = time.Now()
	s.urls[trustKey(host)] = true
}

// setAlways replaces the hosts trusted in every session with hosts, the
// [web] trusted_sites list.
func (k *knownURLs) setAlways(hosts []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.always = map[string]bool{}
	for _, h := range hosts {
		k.always[h] = true
	}
}

// trusted reports whether raw's host is trusted in session, or in every
// session.
func (k *knownURLs) trusted(session, raw string) bool {
	host := urlHost(raw)
	if host == "" {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.always[host] {
		return true
	}
	s := k.sessions[session]
	return session != "" && s != nil && s.urls[trustKey(host)]
}

// add records urls as known in session, normalized. A call with no
// session records nothing: there is no session to tie the URLs to. When
// the session is new and maxURLSessions sessions exist, the one used
// longest ago goes. A session at maxURLsPerSession takes no more URLs.
func (k *knownURLs) add(session string, urls ...string) {
	if session == "" || len(urls) == 0 {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	s := k.sessions[session]
	if s == nil {
		if len(k.sessions) >= maxURLSessions {
			k.dropOldest()
		}
		s = &sessionURLs{urls: map[string]bool{}}
		k.sessions[session] = s
	}
	s.used = time.Now()
	for _, raw := range urls {
		if len(s.urls) >= maxURLsPerSession {
			return
		}
		if n, ok := normalizeURL(raw); ok {
			s.urls[n] = true
			s.urls[hostKey(n)] = true
		}
	}
}

// dropOldest removes the session used longest ago. The caller holds mu. A
// walk over at most 256 sessions costs less than keeping a second
// structure in order.
func (k *knownURLs) dropOldest() {
	var oldest string
	var at time.Time
	for id, s := range k.sessions {
		if oldest == "" || s.used.Before(at) {
			oldest, at = id, s.used
		}
	}
	delete(k.sessions, oldest)
}

// has reports whether raw may be fetched without asking in session: the
// URL itself is known, or it has no query string and its host is one a
// known URL came from.
//
// The host rule exists because the model often knows a site's canonical
// page, such as go.dev/doc/devel/release, when the search results showed
// only go.dev/dl/. The query rule keeps the guard's point: a query string
// is where a made-up URL carries data out, so a URL with one must match
// exactly. An attacker's own site still has to appear in real search
// results, or in the user's own words, before it can be fetched unasked.
func (k *knownURLs) has(session, raw string) bool {
	n, ok := normalizeURL(raw)
	if session == "" || !ok {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	s := k.sessions[session]
	if s == nil {
		return false
	}
	u, _ := url.Parse(n)
	if !s.urls[n] && (u.RawQuery != "" || !s.urls[hostKey(n)]) {
		return false
	}
	s.used = time.Now()
	return true
}

// hostKey returns the key a normalized URL's host is known by, such as
// "host:go.dev". The prefix keeps hosts and whole URLs apart in one set.
func hostKey(normalized string) string {
	u, err := url.Parse(normalized)
	if err != nil {
		return ""
	}
	return "host:" + u.Host
}

// normalizeURL returns raw in the form the guard compares: scheme and host
// in lower case, the fragment (the part after "#") dropped, and an empty
// path written as "/". The query stays, because the query is where a made-up
// URL would carry data out. It returns false for anything but an http or
// https URL with a host.
func normalizeURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

// urlPattern finds http and https URLs in text: the scheme, then
// everything up to a space, a quote or an angle bracket. (?i) makes it
// ignore case, so "HTTPS://Go.dev" counts too.
var urlPattern = regexp.MustCompile("(?i)https?://[^\\s<>\"'`]+")

// questionURLs returns the http and https URLs in text, as a user types
// them in a question. It drops punctuation that ends the sentence rather
// than the URL: a trailing ".", ",", ";", ":", "!" or "?", and a ")" or
// "]" with no partner inside the URL, so "(see https://go.dev/doc)" gives
// https://go.dev/doc while a Wikipedia URL keeps its "(language)".
func questionURLs(text string) []string {
	found := urlPattern.FindAllString(text, -1)
	for i, u := range found {
		for {
			trimmed := strings.TrimRight(u, ".,;:!?")
			switch {
			case strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, "(") < strings.Count(trimmed, ")"):
				trimmed = trimmed[:len(trimmed)-1]
			case strings.HasSuffix(trimmed, "]") && strings.Count(trimmed, "[") < strings.Count(trimmed, "]"):
				trimmed = trimmed[:len(trimmed)-1]
			}
			if trimmed == u {
				break
			}
			u = trimmed
		}
		found[i] = u
	}
	return found
}

// ConfirmCall decides, per call, whether web_fetch asks first. dispatch
// calls it before each call to a built-in (see dispatch.CallConfirmer). It
// first records the URLs in c.Question, the user's questions, as known in
// the call's session. Then, for web_fetch:
//
//   - a URL the session doesn't know asks every time, offering once and
//     deny with no session choice, as configure does. A session choice
//     would let every later made-up URL through.
//   - save on a known URL asks as write_file does, offering once, session
//     and deny, because a download stays on disk.
//   - a known URL without save leaves the choice to Confirm: no question,
//     unless [builtin] confirm lists web_fetch.
//
// A scheduled job has nobody to ask, so dispatch declines each call that
// asks. It returns ok = false for any other tool, and for arguments that
// don't hold a URL: that call fails on its arguments, and a prompt about
// it would help nobody.
func (t *Tools) ConfirmCall(c dispatch.Call) (dispatch.Confirm, bool) {
	if c.Name != WebFetch || !t.enabled(WebFetch) {
		return dispatch.ConfirmNever, false
	}
	t.web.known.add(c.Session, questionURLs(c.Question)...)
	var a webFetchArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return dispatch.ConfirmNever, false
	}
	if _, ok := normalizeURL(a.URL); !ok {
		return dispatch.ConfirmNever, false
	}
	switch {
	case a.Save && !t.web.known.has(c.Session, a.URL):
		// A download from a URL nobody gave asks every time, whatever
		// the user trusts: CallSite offers no website choice for it.
		return dispatch.ConfirmAlways, true
	case a.Save:
		return dispatch.ConfirmAsk, true
	case !t.web.known.has(c.Session, a.URL) && !t.web.known.trusted(c.Session, a.URL):
		return dispatch.ConfirmAlways, true
	}
	return dispatch.ConfirmNever, false
}

// CallSite returns the host a web_fetch call reads, for the "This website
// for this chat" and "Always this website" choices, or "" when they don't
// apply: another tool, a download, or arguments that hold no URL. dispatch
// asks it only for a call that asks, so the URL is one nobody gave.
func (t *Tools) CallSite(c dispatch.Call) string {
	if c.Name != WebFetch {
		return ""
	}
	var a webFetchArgs
	if err := json.Unmarshal(c.Args, &a); err != nil || a.Save {
		return ""
	}
	host := urlHost(a.URL)
	// A host that can't be saved, such as an address, gets no website
	// choice, so the prompt never offers what it can't keep.
	if config.CheckTrustedSite(host) != nil {
		return ""
	}
	return host
}

// TrustSite trusts host for c's session and, with always, in every
// session: it hands host to the saver merud set with UseSiteSaver, which
// adds it to [web] trusted_sites, and trusts it here once that worked. It
// fails when always can't be saved, or no saver is set; the session's
// trust holds either way.
func (t *Tools) TrustSite(ctx context.Context, c dispatch.Call, host string, always bool) error {
	t.web.known.trust(c.Session, host)
	if !always {
		return nil
	}
	if t.saveSite == nil {
		return errors.New("this merud can't save a trusted site")
	}
	return t.saveSite(ctx, host)
}

// UseTrustedSites sets the hosts web_fetch reads without asking in every
// session, from [web] trusted_sites. merud calls it at startup and each
// time it reloads config.
func (t *Tools) UseTrustedSites(hosts []string) {
	t.web.known.setAlways(hosts)
}

// UseSiteSaver gives TrustSite the function that adds a host to [web]
// trusted_sites. merud's saves the host through config's checked edit,
// then calls UseTrustedSites with the new list. Without one, "Always this
// website" trusts the host for the session only, and says why.
func (t *Tools) UseSiteSaver(save func(ctx context.Context, host string) error) {
	t.saveSite = save
}
