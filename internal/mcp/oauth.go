// This file signs Meru in to a remote MCP server that asks for OAuth, as
// the MCP authorization spec describes: the server answers 401 with a link
// to its sign-in metadata, and the client finds the authorization server,
// gets a client (registered on the spot, or the app the user set up once),
// sends the user to the browser, takes the code back on a loopback
// callback, and trades it for tokens. The Go MCP SDK's auth package does
// that protocol; this file adds what a daemon needs around it:
//
//   - signIn, the handler the transport asks for a token on every request.
//     With no sign-in under way it answers a 401 with errSignInNeeded at
//     once, so merud never waits on a browser at startup.
//   - tokenStore, the files under ~/.meru/oauth/ that keep a sign-in across
//     restarts.
//   - callback, the one-time HTTP listener on loopback the browser returns
//     to.
//   - newOAuthClient, the HTTP client for the sign-in requests, with the
//     address rules in ARCHITECTURE.md, "MCP".
//
// No token, code, state value or sign-in link goes in a log line or an
// error. See the design in issue #237.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/aarora79/meru/internal/loopback"
)

// errSignInNeeded is what a call to a signed-out server fails with. The
// pool turns it into the "sign in needed" state instead of a failure.
// errors.Is finds it through the SDK's wrapping.
var errSignInNeeded = errors.New("sign in needed")

// signInNeeded is the text Status reports as LastError for a server in
// that state. Clients show their own words for it.
const signInNeeded = "sign in needed"

// Timeouts for a sign-in. A person needs a few minutes to sign in;
// signInTimeout ends an attempt they walked away from. linkTimeout caps how
// long SignIn waits for the server's metadata before it has a link to give.
const (
	signInTimeout = 10 * time.Minute
	linkTimeout   = 30 * time.Second
	oauthTimeout  = 30 * time.Second
)

// ---- the token store ----

// storedSignIn is what ~/.meru/oauth/<server>.json holds: enough to renew
// the token after a restart with no new sign-in. A confidential client's
// secret stays in secrets.toml; this file never holds it.
type storedSignIn struct {
	Server    string           `json:"server"`
	Resource  string           `json:"resource"` // the server URL the token is for
	ClientID  string           `json:"client_id"`
	TokenURL  string           `json:"token_url"`
	AuthStyle oauth2.AuthStyle `json:"auth_style"`
	Scopes    []string         `json:"scopes,omitempty"`
	Token     *oauth2.Token    `json:"token"`
	Saved     time.Time        `json:"saved"`
}

// tokenStore reads and writes the files under one folder. It refuses a
// folder or a file other users can open, as secrets.toml's loader does.
type tokenStore struct {
	dir string
	mu  sync.Mutex // one write at a time, so a refresh and a sign-in don't interleave
}

// path returns the file for server; server names hold only letters,
// digits, '-' and '_' (checkServerName), so the name is a safe file name.
func (s *tokenStore) path(server string) string {
	return filepath.Join(s.dir, server+".json")
}

// load returns the server's saved sign-in, or ok == false when it has none.
// It fails when the file can't be read or other users can read it.
func (s *tokenStore) load(server string) (storedSignIn, bool, error) {
	var si storedSignIn
	info, err := os.Stat(s.path(server))
	if errors.Is(err, fs.ErrNotExist) {
		return si, false, nil
	}
	if err != nil {
		return si, false, err
	}
	// checkTokenMode differs by system: see mode_unix.go and mode_windows.go.
	if err := checkTokenMode(s.path(server), info); err != nil {
		return si, false, err
	}
	b, err := os.ReadFile(s.path(server)) // #nosec G304 -- a file merud writes under ~/.meru/oauth
	if err != nil {
		return si, false, err
	}
	if err := json.Unmarshal(b, &si); err != nil {
		return si, false, fmt.Errorf("%s: %w", s.path(server), err)
	}
	return si, si.Token != nil, nil
}

// save writes si through a 0600 temp file and a rename, in a 0700 folder,
// so a crash never leaves half a file. Its error never holds the token.
func (s *tokenStore) save(si storedSignIn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	si.Saved = time.Now().UTC()
	b, err := json.MarshalIndent(si, "", "  ")
	if err != nil {
		return errors.New("encode the sign-in")
	}
	tmp, err := os.CreateTemp(s.dir, "."+si.Server+"-*.json")
	if err != nil {
		return err
	}
	// defer removes the temp file if the rename never happens.
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(si.Server))
}

// prune removes the file of each server not in keep, so a token never
// outlives its [[mcp.servers]] entry. It leaves other files alone.
func (s *tokenStore) prune(keep map[string]bool, log *slog.Logger) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(name, ".") || keep[name] || e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, e.Name())); err == nil {
			log.Info("mcp sign-in removed", "mcp_server", name, "why", "no longer in config")
		}
	}
}

// ---- the callback ----

// callbackResult is what the browser brought back.
type callbackResult struct {
	code, state, iss, errCode string
}

// callback is the one-time listener the browser returns to after the user
// signs in. It listens on loopback only, takes the first request to its
// path, and closes when the sign-in ends.
type callback struct {
	url     string
	path    string
	servers []*http.Server
	got     atomic.Bool
	result  chan callbackResult // buffered, size 1
}

// oauthErrors are the error codes RFC 6749 defines for the authorization
// response. The callback page names only one of these, never text the
// server sent.
var oauthErrors = map[string]bool{
	"invalid_request": true, "unauthorized_client": true, "access_denied": true,
	"unsupported_response_type": true, "invalid_scope": true, "server_error": true,
	"temporarily_unavailable": true,
}

// listenCallback opens the listener. redirect is the URL the app
// registered, or "" for a free port on 127.0.0.1 and the path /callback.
// For localhost it listens on both 127.0.0.1 and [::1], since a browser may
// try either. It fails when the port is in use.
func listenCallback(redirect string) (*callback, error) {
	hosts, port, path := []string{"127.0.0.1"}, "0", "/callback"
	if redirect != "" {
		u, err := url.Parse(redirect)
		if err != nil {
			return nil, err
		}
		port, path = u.Port(), u.Path
		switch u.Hostname() {
		case "localhost":
			hosts = []string{"127.0.0.1", "::1"}
		default:
			hosts = []string{u.Hostname()}
		}
	}
	cb := &callback{path: path, result: make(chan callbackResult, 1)}
	for i, h := range hosts {
		ln, err := net.Listen("tcp", net.JoinHostPort(h, port))
		if err != nil {
			if i > 0 {
				continue // [::1] is a bonus; 127.0.0.1 is enough
			}
			cb.close()
			return nil, fmt.Errorf("port %s is in use; close what holds it, or change oauth.redirect and the app's callback URL", port)
		}
		if port == "0" {
			// The system picked the port; name it in the URL.
			port = fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
		}
		srv := &http.Server{Handler: cb, ReadHeaderTimeout: 5 * time.Second}
		cb.servers = append(cb.servers, srv)
		// Serve runs until close shuts the server down.
		go func() { _ = srv.Serve(ln) }()
	}
	if redirect != "" {
		cb.url = redirect
	} else {
		cb.url = "http://127.0.0.1:" + port + path
	}
	return cb, nil
}

// ServeHTTP answers the browser. Only the first GET to the path counts; a
// second gets 410, any other path 404. The page shows fixed text, and an
// RFC 6749 error code only, escaped: never what the query held otherwise.
func (cb *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != cb.path || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if !cb.got.CompareAndSwap(false, true) {
		http.Error(w, "This sign-in link was already used.", http.StatusGone)
		return
	}
	q := r.URL.Query()
	res := callbackResult{code: q.Get("code"), state: q.Get("state"), iss: q.Get("iss"), errCode: q.Get("error")}
	page := "Meru is signed in. You can close this tab."
	if res.errCode != "" {
		code := res.errCode
		if !oauthErrors[code] {
			code = "the server didn't say why"
		}
		res.errCode = code
		page = "Sign-in failed: " + html.EscapeString(code) + ". You can close this tab."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><title>Meru</title><p style=\"font:16px system-ui;margin:3em\">%s</p>", page)
	cb.result <- res
}

// close shuts every listener down.
func (cb *callback) close() {
	for _, s := range cb.servers {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.Shutdown(ctx)
		cancel()
	}
}

// ---- the HTTP client for sign-in requests ----

// newOAuthClient returns the client for the metadata, registration and
// token requests to the servers serverURL's metadata names. Its rules:
//
//   - https only, unless the host is loopback (tests run there);
//   - no private or loopback address unless serverURL's own host is one,
//     checked on the address the name resolves to, so a server on the
//     internet can't point a token request at the user's router;
//   - no redirects, and no proxy from the environment;
//   - oauthTimeout per request.
func newOAuthClient(serverURL string) *http.Client {
	private := false
	if u, err := url.Parse(serverURL); err == nil {
		if a, err := netip.ParseAddr(u.Hostname()); err == nil {
			private = loopback.NearEdge(a)
		} else {
			private = loopback.IsURL(serverURL)
		}
	}
	d := &net.Dialer{Timeout: oauthTimeout}
	if !private {
		// Control runs after the name resolves, on the address itself.
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			if loopback.NearEdge(a.Unmap()) {
				return fmt.Errorf("the sign-in points at %s, a private address, which Meru doesn't trust for a server on the internet", a)
			}
			return nil
		}
	}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSHandshakeTimeout: oauthTimeout, ForceAttemptHTTP2: true}
	return &http.Client{
		Timeout:   oauthTimeout,
		Transport: httpsOnly{tr},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the sign-in server sent a redirect; Meru doesn't follow it")
		},
	}
}

// httpsOnly refuses a plain-http request to a host that isn't loopback.
type httpsOnly struct{ base http.RoundTripper }

// RoundTrip sends req when its scheme is https or its host is loopback.
func (t httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && !loopback.IsURL(req.URL.String()) {
		return nil, fmt.Errorf("the sign-in uses %s; Meru signs in over https only", req.URL.Scheme)
	}
	return t.base.RoundTrip(req)
}

// ---- the handler ----

// signIn is one server's OAuth state. It satisfies the SDK's
// auth.OAuthHandler interface (TokenSource and Authorize), so the
// transport asks it for a token on every request and calls Authorize on a
// 401 or 403.
type signIn struct {
	server   string
	resource string // the server's URL
	cfg      OAuthConfig
	client   *http.Client
	store    *tokenStore
	log      *slog.Logger

	// mu guards the two fields below. No code holds it across a network
	// call or a wait: it copies what it needs and lets go, so one slow
	// token exchange can't block every request to the server.
	mu     sync.Mutex
	source oauth2.TokenSource // nil until signed in
	now    *attempt           // non-nil only while a sign-in runs
}

// attempt is one sign-in, from the user's click to its end. SignIn builds
// a fresh one each time, so nothing from an earlier sign-in lingers.
type attempt struct {
	sdk    *auth.AuthorizationCodeHandler
	cb     *callback
	link   string        // set once, before ready closes
	ready  chan struct{} // closed when link is set
	failed chan error    // one error if the sign-in ends early; buffered, size 1
	done   chan struct{} // closed when the attempt ends
}

// newSignIn builds the handler for cfg and loads a saved sign-in, if any.
// A file for another URL is stale (the user changed the entry) and is
// ignored.
func newSignIn(cfg ServerConfig, store *tokenStore, log *slog.Logger) *signIn {
	s := &signIn{server: cfg.Name, resource: cfg.URL, cfg: cfg.OAuth, client: newOAuthClient(cfg.URL), store: store, log: log}
	si, ok, err := store.load(cfg.Name)
	switch {
	case err != nil:
		log.Warn("mcp sign-in unreadable", "mcp_server", cfg.Name, "err", err)
	case ok && si.Resource == cfg.URL:
		oc := &oauth2.Config{ClientID: si.ClientID, ClientSecret: cfg.OAuth.ClientSecret,
			Endpoint: oauth2.Endpoint{TokenURL: si.TokenURL, AuthStyle: si.AuthStyle}, Scopes: si.Scopes}
		s.source = s.saving(s.ctxWithClient(), oc, si.Token)
	}
	return s
}

// ctxWithClient returns a context that tells golang.org/x/oauth2 to make
// its requests with s.client. That is how the package takes a custom HTTP
// client: as a value stored in the context under the key oauth2.HTTPClient.
func (s *signIn) ctxWithClient() context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, s.client)
}

// TokenSource returns the stored token's source, or nil, which tells the
// SDK to send no Authorization header, as it should before a sign-in.
func (s *signIn) TokenSource(context.Context) (oauth2.TokenSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.source, nil
}

// Authorize runs on a 401 or 403. With no sign-in under way it returns
// errSignInNeeded at once. During a sign-in it hands the request to the
// attempt's SDK handler, which finds the metadata, gets a client, and
// calls fetch for the code; that may wait minutes for the browser.
func (s *signIn) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	s.mu.Lock()
	a := s.now
	s.source = nil // whatever we held was refused
	s.mu.Unlock()
	if a == nil {
		_ = resp.Body.Close() // the SDK says Authorize must close it
		return errSignInNeeded
	}
	return a.sdk.Authorize(ctx, req, resp)
}

// busy reports whether a sign-in is under way.
func (s *signIn) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now != nil
}

// start builds a new attempt and makes it current. It fails when one is
// already under way or the callback can't listen.
func (s *signIn) start() (*attempt, error) {
	s.mu.Lock()
	busy := s.now != nil
	s.mu.Unlock()
	if busy {
		return nil, errors.New("a sign-in is already waiting for the browser")
	}
	cb, err := listenCallback(s.cfg.Redirect)
	if err != nil {
		return nil, err
	}
	a := &attempt{cb: cb, ready: make(chan struct{}), failed: make(chan error, 1), done: make(chan struct{})}
	hc := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:         cb.url,
		RequestRefreshToken: true,
		Client:              s.client,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return s.fetch(ctx, a, args)
		},
		NewTokenSource: func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			return s.saving(ctx, oc, tok), nil
		},
	}
	if len(s.cfg.Scopes) > 0 {
		scopes := s.cfg.Scopes
		hc.ScopeFilter = func([]string) []string { return scopes }
	}
	if s.cfg.ClientID != "" {
		pc := &oauthex.ClientCredentials{ClientID: s.cfg.ClientID, Issuer: s.cfg.Issuer}
		if s.cfg.ClientSecret != "" {
			pc.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: s.cfg.ClientSecret}
		}
		hc.PreregisteredClient = pc
	} else {
		hc.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:              "Meru",
				RedirectURIs:            []string{cb.url},
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				TokenEndpointAuthMethod: "none",
			},
		}
	}
	h, err := auth.NewAuthorizationCodeHandler(hc)
	if err != nil {
		cb.close()
		return nil, err
	}
	a.sdk = h
	s.mu.Lock()
	s.now = a
	s.mu.Unlock()
	return a, nil
}

// end closes a's callback and makes it no longer current.
func (s *signIn) end(a *attempt) {
	a.cb.close()
	s.mu.Lock()
	if s.now == a {
		s.now = nil
	}
	s.mu.Unlock()
	close(a.done)
}

// link returns the sign-in link while an attempt waits for the browser.
func (s *signIn) link() string {
	s.mu.Lock()
	a := s.now
	s.mu.Unlock()
	if a == nil {
		return ""
	}
	select {
	case <-a.ready:
		return a.link
	default:
		return ""
	}
}

// fetch is the SDK's AuthorizationCodeFetcher: it publishes the link for
// the clients, then waits for the browser's return or ctx's end.
func (s *signIn) fetch(ctx context.Context, a *attempt, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	if u, err := url.Parse(args.URL); err != nil || u.Scheme != "https" {
		if !loopback.IsURL(args.URL) {
			return nil, errors.New("the sign-in page isn't an https address")
		}
	}
	// One attempt sends the user to the browser once. After a refused
	// reply, such as a state mismatch, the transport tries again and the
	// SDK asks for a second code; that ends the attempt instead, and the
	// user starts a new one.
	select {
	case <-a.ready:
		return nil, errors.New("the sign-in already ran once; start a new one")
	default:
	}
	a.link = args.URL
	close(a.ready)
	select {
	case res := <-a.cb.result:
		if res.errCode != "" {
			return nil, fmt.Errorf("the server refused the sign-in: %s", res.errCode)
		}
		return &auth.AuthorizationResult{Code: res.code, State: res.state, Iss: res.iss}, nil
	case <-ctx.Done():
		return nil, errors.New("you didn't finish signing in within 10 minutes")
	}
}

// saving wraps oc's token source so each new token is written to the
// store, and so a refresh the server refuses becomes errSignInNeeded.
func (s *signIn) saving(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) oauth2.TokenSource {
	ss := &savingSource{owner: s, oc: oc, base: oc.TokenSource(ctx, tok)}
	ss.save(tok)
	s.mu.Lock()
	s.source = ss
	s.mu.Unlock()
	return ss
}

// savingSource is an oauth2.TokenSource: an interface with one method,
// Token, that hands out a good token and refreshes it when it expires. This
// one wraps the package's own and saves what it hands out.
type savingSource struct {
	owner *signIn
	oc    *oauth2.Config
	base  oauth2.TokenSource
	mu    sync.Mutex
	last  string // the access token last saved
}

// Token returns a good token, saving it when it changed. A failed refresh
// clears the server's source and returns errSignInNeeded, logged once.
func (ss *savingSource) Token() (*oauth2.Token, error) {
	t, err := ss.base.Token()
	if err != nil {
		ss.owner.mu.Lock()
		was := ss.owner.source == oauth2.TokenSource(ss)
		if was {
			ss.owner.source = nil
		}
		ss.owner.mu.Unlock()
		if was {
			ss.owner.log.Warn("mcp token refresh refused", "mcp_server", ss.owner.server)
		}
		return nil, errSignInNeeded
	}
	ss.save(t)
	return t, nil
}

// save writes t to the store when its access token is new.
func (ss *savingSource) save(t *oauth2.Token) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if t == nil || t.AccessToken == ss.last {
		return
	}
	ss.last = t.AccessToken
	err := ss.owner.store.save(storedSignIn{
		Server: ss.owner.server, Resource: ss.owner.resource, ClientID: ss.oc.ClientID,
		TokenURL: ss.oc.Endpoint.TokenURL, AuthStyle: ss.oc.Endpoint.AuthStyle, Scopes: ss.oc.Scopes, Token: t,
	})
	if err != nil {
		ss.owner.log.Warn("mcp sign-in not saved", "mcp_server", ss.owner.server, "err", err)
	}
}

// signInError turns an error from a sign-in into a fixed sentence the
// user reads, so no server text, token or link ever reaches a client.
func signInError(err error) error {
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		// merud stopped, or a restart closed the pool, while the sign-in
		// waited for the browser.
		return errors.New("the sign-in stopped because merud restarted or quit; sign in again")
	case strings.Contains(msg, "state mismatch"):
		return errors.New("the reply didn't match this sign-in; try again")
	case strings.Contains(msg, "within 10 minutes"):
		return errors.New("you didn't finish signing in within 10 minutes; try again")
	case strings.Contains(msg, "access_denied"):
		return errors.New("you didn't allow Meru to connect")
	case strings.Contains(msg, "refused the sign-in"):
		return errors.New("the server refused the sign-in; check the app's client ID, secret and scopes")
	case strings.Contains(msg, "private address"):
		return errors.New("the server's sign-in points at an address on your own network, which Meru doesn't trust for a server on the internet")
	case strings.Contains(msg, "port") && strings.Contains(msg, "in use"):
		return err
	case strings.Contains(msg, "issuer"):
		return errors.New("the server's sign-in moved to another address than oauth.issuer names")
	case strings.Contains(msg, "registration"), strings.Contains(msg, "no client"):
		return errors.New("this server doesn't let Meru register itself; add oauth.client_id from an app you registered with it")
	case strings.Contains(msg, "missing access_token"), strings.Contains(msg, "cannot fetch token"):
		return errors.New("the server refused the sign-in; check the app's client ID, secret and scopes")
	case strings.Contains(msg, "metadata"):
		return errors.New("the server didn't say how to sign in")
	}
	// The raw error can quote the server's reply, so it never reaches a
	// log or a client; the sentence says what to check instead.
	return errors.New("something went wrong talking to the server; try again, and check the server's url and the app's settings")
}

// ---- the pool's side ----

// SignIn starts a sign-in for the named server and returns the link the
// user must open, once the SDK has found the server's metadata. The rest
// (waiting for the browser, the token exchange, the connect) runs in a
// goroutine the Pool owns, bounded by signInTimeout, which Close stops.
// It fails for an unknown server, one without auth = "oauth", one already
// signing in, or one whose sign-in can't start; each error is a fixed
// sentence the user reads.
func (p *Pool) SignIn(ctx context.Context, name string) (string, error) {
	s, ok := p.byName[name]
	switch {
	case !ok:
		return "", fmt.Errorf("no server is named %q", name)
	case s.signin == nil:
		return "", fmt.Errorf("%s doesn't sign in with OAuth; its entry needs auth = \"oauth\"", name)
	}
	// A sign-in already waiting for the browser hands back its link, so a
	// second click on Sign in, after the user closed the tab, opens the
	// same page again instead of failing.
	if link := s.signin.link(); link != "" {
		return link, nil
	}
	a, err := s.signin.start()
	if err != nil {
		return "", signInError(err)
	}
	p.log.Info("mcp sign-in started", "mcp_server", name)
	p.watchers.Add(1)
	go p.finishSignIn(s, a)
	select {
	case <-a.ready:
		return a.link, nil
	case err := <-a.failed:
		return "", err
	case <-time.After(linkTimeout):
		return "", errors.New("the server's sign-in didn't answer in 30 seconds; try again")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// finishSignIn connects s with the attempt a under way: the transport gets
// a 401, calls Authorize, which runs the SDK's flow and waits in fetch for
// the browser. It connects outside s.mu and takes the lock only to install
// the session, so Status and calls to other servers never wait on the
// browser.
func (p *Pool) finishSignIn(s *server, a *attempt) {
	defer p.watchers.Done()
	defer s.signin.end(a)
	ctx, cancel := context.WithTimeout(p.ctx, signInTimeout)
	defer cancel()
	cs, tools, stop, err := p.connectSession(ctx, s, signInTimeout)
	if err != nil {
		e := signInError(err)
		// trySend: the channel has room for one, and SignIn may have
		// stopped listening; select with default never blocks.
		select {
		case a.failed <- e:
		default:
		}
		s.mu.Lock()
		s.lastErr = signInNeeded
		s.mu.Unlock()
		p.log.Warn("mcp sign-in failed", "mcp_server", s.cfg.Name, "reason", e.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = cs.Close()
		stop()
		return
	}
	if s.session != nil {
		_ = s.session.Close()
		s.stop()
	}
	s.session, s.stop, s.lastErr = cs, stop, ""
	s.setToolsLocked(tools)
	p.watchers.Add(1)
	go p.watch(s, cs)
	p.log.Info("mcp signed in", "mcp_server", s.cfg.Name, "tools_offered", s.offered, "tools_allowed", len(s.tools))
}

// connectSession is tryConnectLocked without the lock and without touching
// s: it dials, runs the handshake and lists the tools, all within limit,
// and returns what tryConnectLocked would install.
func (p *Pool) connectSession(ctx context.Context, s *server, limit time.Duration) (*mcp.ClientSession, []*mcp.Tool, context.CancelFunc, error) {
	procCtx, stop := context.WithCancel(context.Background())
	t, err := p.dial(procCtx, s.cfg, p.log)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cs, err := p.client.Connect(cctx, t, nil)
	if err != nil {
		stop()
		return nil, nil, nil, fmt.Errorf("connect: %w", err)
	}
	tools, err := listTools(cctx, cs)
	if err != nil {
		_ = cs.Close()
		stop()
		return nil, nil, nil, fmt.Errorf("list tools: %w", err)
	}
	return cs, tools, stop, nil
}

// PruneSignIns removes the saved sign-in of each server in dir that keep
// doesn't name, so a token never outlives its entry. merud calls it after a
// config reload succeeds, with the auth = "oauth" servers that config holds.
func PruneSignIns(dir string, keep []string, log *slog.Logger) {
	set := make(map[string]bool, len(keep))
	for _, k := range keep {
		set[k] = true
	}
	(&tokenStore{dir: dir}).prune(set, log)
}
