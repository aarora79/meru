// This file holds web_fetch's URL guard: the per-session set of URLs the
// model may fetch without asking, and ConfirmCall, which dispatch asks
// before each call. The guard stops the model, or a page that talks the
// model into it, from sending the user's data out inside a URL it made up,
// such as https://attacker.example/?notes=<your notes>. See
// ARCHITECTURE.md, "Web search".

package builtin

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

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
// in a question. It lives only in memory, as session approvals do, so a
// restart of merud forgets it; the user's own questions come back with the
// next call (see ConfirmCall), and a search-result URL then asks once.
type knownURLs struct {
	// mu guards sessions. Tool calls in one round run side by side.
	mu       sync.Mutex
	sessions map[string]*sessionURLs
}

// sessionURLs is one session's set, and when it was last used.
type sessionURLs struct {
	urls map[string]bool
	used time.Time
}

// newKnownURLs returns an empty set.
func newKnownURLs() *knownURLs {
	return &knownURLs{sessions: map[string]*sessionURLs{}}
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
	if c.Name != WebFetch || !t.web.fetch {
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
	case !t.web.known.has(c.Session, a.URL):
		return dispatch.ConfirmAlways, true
	case a.Save:
		return dispatch.ConfirmAsk, true
	}
	return dispatch.ConfirmNever, false
}
