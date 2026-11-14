// This file tests the OAuth sign-in against a fake authorization server and
// a protected MCP endpoint on one loopback test server: discovery,
// registration or a configured client, the browser's round trip to the
// callback, the token exchange, the saved sign-in a restart uses, and the
// refusals.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeAuth is the fake: an MCP endpoint at /mcp that wants a bearer token,
// and an authorization server at the same address.
type fakeAuth struct {
	srv *httptest.Server
	// knobs
	dcr        bool   // offer dynamic registration
	wrongState bool   // send the browser back with another state
	wantSecret string // a confidential client must send this secret
	// what it saw
	mu       sync.Mutex
	secret   string // the client secret the token request carried
	verifier bool   // the token request carried a PKCE verifier
	tokens   int    // tokens issued
	valid    map[string]bool
}

// newFakeAuth starts the fake. Its MCP server offers the echo tool.
func newFakeAuth(t *testing.T, dcr bool) *fakeAuth {
	t.Helper()
	f := &fakeAuth{dcr: dcr, valid: map[string]bool{}}
	mcpSrv := newTestServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil)
	mux := http.NewServeMux()
	base := func() string { return f.srv.URL }
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		ok := f.valid[tok]
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base()+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": base() + "/mcp", "authorization_servers": []string{base()}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer": base(), "authorization_endpoint": base() + "/authorize", "token_endpoint": base() + "/token",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post", "none"},
		}
		if f.dcr {
			meta["registration_endpoint"] = base() + "/register"
		}
		writeJSON(w, meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"client_id": "dyn-1", "redirect_uris": req["redirect_uris"], "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		if f.wrongState {
			state = "not-the-one"
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(state), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.secret = r.Form.Get("client_secret")
		if r.Form.Get("grant_type") == "authorization_code" {
			f.verifier = r.Form.Get("code_verifier") != ""
		}
		if f.wantSecret != "" && f.secret != f.wantSecret {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"error": "invalid_client"})
			return
		}
		f.tokens++
		tok := fmt.Sprintf("tok-%d", f.tokens)
		f.valid[tok] = true
		writeJSON(w, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600, "refresh_token": "r1"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// oauthPool opens a pool with one auth = "oauth" server at f.
func oauthPool(t *testing.T, f *fakeAuth, dir string, o OAuthConfig) *Pool {
	t.Helper()
	o.TokenDir = dir
	cfg := ServerConfig{Name: "gh", URL: f.srv.URL + "/mcp", Remote: true, Auth: AuthOAuth, Allow: []string{"echo"}, OAuth: o}
	p, err := NewPool(context.Background(), []ServerConfig{cfg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// status returns the pool's one server's status.
func status(p *Pool) ServerStatus { return p.Status()[0] }

// browse plays the browser: it opens the sign-in link and follows the
// redirect to the callback.
func browse(t *testing.T, link string) string {
	t.Helper()
	resp, err := http.Get(link)
	if err != nil {
		t.Fatalf("browser: %v", err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	b.Write(buf[:n])
	return b.String()
}

// waitConnected waits up to 5 s for the server to connect, or for the
// sign-in to end without it.
func waitConnected(p *Pool) ServerStatus {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := status(p)
		if st.Connected || (st.SignIn && st.Link == "" && !p.byName["gh"].signin.busy()) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	return status(p)
}

func TestSignInWithRegistration(t *testing.T) {
	f := newFakeAuth(t, true)
	dir := filepath.Join(t.TempDir(), "oauth")
	p := oauthPool(t, f, dir, OAuthConfig{})

	if st := status(p); st.Connected || !st.SignIn || st.LastError != signInNeeded {
		t.Fatalf("before sign-in: %+v, want sign in needed", st)
	}
	link, err := p.SignIn(context.Background(), "gh")
	if err != nil {
		t.Fatal(err)
	}
	if st := status(p); st.Link != link {
		t.Errorf("status link = %q, want the sign-in link while it waits", st.Link)
	}
	if page := browse(t, link); !strings.Contains(page, "Meru is signed in") {
		t.Errorf("callback page = %q", page)
	}
	st := waitConnected(p)
	if !st.Connected || st.SignIn || st.Allowed != 1 {
		t.Fatalf("after sign-in: %+v", st)
	}
	if res, err := p.Call(context.Background(), "gh.echo", json.RawMessage(`{"text":"hi"}`)); err != nil || res.Text != "hi" {
		t.Fatalf("Call = %+v, %v", res, err)
	}
	f.mu.Lock()
	if !f.verifier {
		t.Error("the token request carried no PKCE verifier")
	}
	f.mu.Unlock()

	// The sign-in is saved at 0600, and a new pool connects with it.
	// Windows has no mode bits, and Go reports its files as 0666, so the
	// mode check runs only where modes exist; the new pool's connection
	// below checks on every system that the saved file loads.
	info, err := os.Stat(filepath.Join(dir, "gh.json"))
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", info.Mode().Perm())
	}
	p.Close()
	p2 := oauthPool(t, f, dir, OAuthConfig{})
	if st := status(p2); !st.Connected {
		t.Errorf("after a restart: %+v, want connected with the saved token", st)
	}
}

func TestSignInWithAConfiguredClient(t *testing.T) {
	f := newFakeAuth(t, false) // no registration, as at GitHub
	f.wantSecret = "s3cret-value"
	dir := filepath.Join(t.TempDir(), "oauth")
	p := oauthPool(t, f, dir, OAuthConfig{ClientID: "app-1", ClientSecret: "s3cret-value", Scopes: []string{"repo"}})
	link, err := p.SignIn(context.Background(), "gh")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if q := u.Query(); q.Get("client_id") != "app-1" || q.Get("scope") != "repo" || q.Get("code_challenge") == "" {
		t.Errorf("authorize query = %v, want client_id app-1, scope repo and a PKCE challenge", q)
	}
	browse(t, link)
	if st := waitConnected(p); !st.Connected {
		t.Fatalf("after sign-in: %+v", st)
	}
	// The saved file holds no secret.
	b, _ := os.ReadFile(filepath.Join(dir, "gh.json"))
	if strings.Contains(string(b), "s3cret-value") {
		t.Error("the token file holds the client secret")
	}
}

func TestSignInWithoutRegistrationOrClient(t *testing.T) {
	f := newFakeAuth(t, false)
	p := oauthPool(t, f, t.TempDir(), OAuthConfig{})
	_, err := p.SignIn(context.Background(), "gh")
	if err == nil || !strings.Contains(err.Error(), "doesn't let Meru register itself") {
		t.Errorf("err = %v, want the sentence about oauth.client_id", err)
	}
}

func TestSignInWrongState(t *testing.T) {
	f := newFakeAuth(t, true)
	f.wrongState = true
	p := oauthPool(t, f, t.TempDir(), OAuthConfig{})
	link, err := p.SignIn(context.Background(), "gh")
	if err != nil {
		t.Fatal(err)
	}
	browse(t, link)
	if st := waitConnected(p); st.Connected || !st.SignIn {
		t.Errorf("after a wrong state: %+v, want still signed out", st)
	}
}

func TestSignInRefusals(t *testing.T) {
	f := newFakeAuth(t, true)
	p := oauthPool(t, f, t.TempDir(), OAuthConfig{})
	if _, err := p.SignIn(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "no server is named") {
		t.Errorf("unknown server: %v", err)
	}
	first, err := p.SignIn(context.Background(), "gh")
	if err != nil {
		t.Fatal(err)
	}
	// A second request while the first waits for the browser gets the same
	// link, as after the user closed the tab and clicked Sign in again.
	if again, err := p.SignIn(context.Background(), "gh"); err != nil || again != first {
		t.Errorf("second sign-in while one waits: %q, %v; want the first link", again, err)
	}
}

func TestCallback(t *testing.T) {
	cb, err := listenCallback("")
	if err != nil {
		t.Fatal(err)
	}
	defer cb.close()
	if !strings.HasPrefix(cb.url, "http://127.0.0.1:") || !strings.HasSuffix(cb.url, "/callback") {
		t.Fatalf("url = %q", cb.url)
	}
	base := strings.TrimSuffix(cb.url, "/callback")
	if resp, err := http.Get(base + "/other"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("other path: %v %v", resp, err)
	}
	resp, err := http.Get(cb.url + "?error=<script>alert(1)</script>&error_description=evil")
	if err != nil {
		t.Fatal(err)
	}
	page := make([]byte, 400)
	n, _ := resp.Body.Read(page)
	resp.Body.Close()
	if s := string(page[:n]); strings.Contains(s, "<script>") || strings.Contains(s, "evil") || !strings.Contains(s, "the server didn&#39;t say why") {
		t.Errorf("page = %q, want fixed text only", s)
	}
	if res := <-cb.result; res.errCode != "the server didn't say why" {
		t.Errorf("result = %+v", res)
	}
	if resp, err := http.Get(cb.url + "?code=x"); err != nil || resp.StatusCode != http.StatusGone {
		t.Errorf("second GET: %v %v, want 410", resp, err)
	}
}

func TestOAuthClientRules(t *testing.T) {
	c := newOAuthClient("https://mcp.example.com/mcp")
	if _, err := c.Get("http://example.com/x"); err == nil || !strings.Contains(err.Error(), "https only") {
		t.Errorf("plain http: %v", err)
	}
	// 10.255.255.1 never answers; the dial refuses it before trying.
	if _, err := c.Get("https://10.255.255.1/token"); err == nil || !strings.Contains(err.Error(), "private address") {
		t.Errorf("private address: %v", err)
	}
	// A server on loopback may sign in on loopback.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	if resp, err := newOAuthClient(ts.URL).Get(ts.URL); err != nil {
		t.Errorf("loopback for a loopback server: %v", err)
	} else {
		resp.Body.Close()
	}
}

func TestCheckAuth(t *testing.T) {
	base := ServerConfig{Name: "g", URL: "https://x.example/mcp", Remote: true, Auth: AuthOAuth}
	tests := []struct {
		name string
		edit func(*ServerConfig)
		want string
	}{
		{"fine", func(*ServerConfig) {}, ""},
		{"unknown auth", func(c *ServerConfig) { c.Auth = "basic" }, `auth "basic" is unknown`},
		{"not remote", func(c *ServerConfig) { c.Remote = false; c.URL = "http://127.0.0.1:1/mcp" }, "needs url and remote = true"},
		{"a header too", func(c *ServerConfig) { c.Headers = map[string]string{"authorization": "x"} }, "can't both be set"},
		{"secret without id", func(c *ServerConfig) { c.OAuth.ClientSecret = "s" }, "client_secret needs client_id"},
		{"http issuer", func(c *ServerConfig) { c.OAuth.Issuer = "http://x" }, "must be an https URL"},
		{"public redirect", func(c *ServerConfig) { c.OAuth.Redirect = "http://example.com:1/cb" }, "must be 127.0.0.1"},
		{"no port", func(c *ServerConfig) { c.OAuth.Redirect = "http://localhost/cb" }, "give a port"},
		{"table without auth", func(c *ServerConfig) { c.Auth = ""; c.OAuth.ClientID = "x" }, `needs auth = "oauth"`},
	}
	for _, tt := range tests {
		c := base
		tt.edit(&c)
		err := c.Validate()
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

// TestSignInError checks that each failure a sign-in can meet becomes a
// fixed sentence, and that the server's own words never pass through.
func TestSignInError(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("authorize: %w", context.Canceled), "merud restarted or quit"},
		{errors.New("state mismatch"), "didn't match this sign-in"},
		{errors.New(`oauth2: cannot fetch token: 400 Response: {"ok":false,"error":"invalid_client_id"}`), "check the app's client ID"},
		{errors.New("access_denied"), "didn't allow Meru"},
		{errors.New(`odd reply: {"secret":"xoxp-1"}`), "something went wrong"},
	}
	for _, tt := range tests {
		got := signInError(tt.err).Error()
		if !strings.Contains(got, tt.want) || strings.Contains(got, "xoxp") || strings.Contains(got, "invalid_client_id") {
			t.Errorf("signInError(%q) = %q, want it to say %q", tt.err, got, tt.want)
		}
	}
}
