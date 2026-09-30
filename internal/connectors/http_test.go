// This file tests the supervisor with an http connector, the way it runs
// Google: a fake workspace-mcp served over Streamable HTTP inside the
// test, which wants a sign-in until the test says the user signed in; a
// port another program holds; and the sign-in link's detection and its
// place in the log. The real server's behaviour is checked by
// TestIntegrationGoogle, which runs only with the integration tag.

package connectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// fakeLink is the sign-in link the fake server gives: Google's shape, with
// made-up values.
const fakeLink = "https://accounts.example.test/o/oauth2/auth?client_id=123-invented.apps.googleusercontent.com" +
	"&redirect_uri=http%3A%2F%2Flocalhost%3A8000%2Foauth2callback&state=4f2a"

// fakeGoogle plays workspace-mcp: an MCP server over Streamable HTTP with
// the health tool list_calendars and one mail tool. Until signedIn is
// set, list_calendars fails with the text workspace-mcp 1.30.0 gives,
// which holds the sign-in link.
type fakeGoogle struct {
	t        *testing.T
	srv      *httptest.Server
	signedIn atomic.Bool
	checks   atomic.Int32 // list_calendars calls
	busy     atomic.Bool  // when set, dial reports the port taken
	starts   atomic.Int32 // programs "started"

	mu   sync.Mutex
	exit chan struct{} // closes when the running "program" exits
}

// newFakeGoogle starts the fake server and stops it when the test ends.
func newFakeGoogle(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{t: t}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-workspace-mcp", Version: "1.30.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list_calendars", Description: "List calendars."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			f.checks.Add(1)
			if !f.signedIn.Load() {
				text := "**ACTION REQUIRED: Google Authentication Needed for Google Calendar for 'dana@example.com'**\n\n" +
					"1. Open this URL in your browser to authorize Google Calendar access using all required permissions:\n" +
					"   Authorization URL: " + fakeLink + "\n" +
					"2. After successful authorization, **retry their original command**."
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `- "Dana" (Primary)`}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "search_gmail_messages", Description: "Search mail."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "no mail"}}}, nil, nil
		})
	f.srv = httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(f.srv.Close)
	return f
}

// dial stands in for httpTransport: it "starts the program", whose exit
// channel closes when procCtx ends or the test crashes it, and returns a
// transport to the fake server. With busy set it fails as a taken port
// does, before anything starts.
func (f *fakeGoogle) dial(_, procCtx context.Context, _ Cmd, _ *tailLog) (mcp.Transport, <-chan struct{}, error) {
	if f.busy.Load() {
		return nil, nil, fmt.Errorf("127.0.0.1:8000: %w", ErrPortBusy)
	}
	f.starts.Add(1)
	exit := make(chan struct{})
	f.mu.Lock()
	f.exit = exit
	f.mu.Unlock()
	// The "program" exits when procCtx ends, as exec.CommandContext's
	// does, or when crash closes exit first.
	go func() {
		select {
		case <-procCtx.Done():
			f.mu.Lock()
			defer f.mu.Unlock()
			select {
			case <-exit:
			default:
				close(exit)
			}
		case <-exit:
		}
	}()
	return &mcp.StreamableClientTransport{Endpoint: f.srv.URL, DisableStandaloneSSE: true}, exit, nil
}

// crash makes the running "program" exit, as a crash would.
func (f *fakeGoogle) crash() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.exit:
	default:
		close(f.exit)
	}
}

// googleManifest returns the real Google manifest.
func googleManifest(t *testing.T) Manifest {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == "google" {
			return m
		}
	}
	t.Fatal("no google manifest")
	return Manifest{}
}

// googleSupervisor returns a supervisor for the Google manifest over the
// fake server and a fake clock, installed already, with its secrets in a
// temporary secrets.toml and log lines in logBuf. It closes the
// supervisor when the test ends.
func googleSupervisor(t *testing.T, f *fakeGoogle, logBuf *bytes.Buffer) (*Supervisor, *fakeClock, *secrets.Secrets) {
	t.Helper()
	clock := newFakeClock()
	log := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := newSupervisor(googleManifest(t), log, clock, t.TempDir(), filepath.Join(t.TempDir(), "state", "google.json"))
	inst := Installed{ID: "google", Version: "1.30.0", Dir: "/fake/pkg"}
	s.installed = func() (Installed, bool) { return inst, true }
	s.install = func(context.Context, func(string)) (Installed, error) { return inst, nil }
	s.launch = func(Installed, map[string]string) (Cmd, error) {
		return Cmd{Path: "/fake/pkg/.venv/bin/workspace-mcp"}, nil
	}
	s.dial = f.dial
	t.Cleanup(s.Close)

	path := secrets.Path(t.TempDir())
	if err := secrets.Set(path, SecretName("google", "client_secret"), "invented-secret"); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, clock, sec
}

// googleTable is a [connectors.google] table with made-up values.
func googleTable() config.Connector {
	return config.Connector{"enabled": true, "email": "dana@example.com", "client_id": "123-invented.apps.googleusercontent.com"}
}

// TestGoogleSignIn runs the Google connector from its first check, which
// wants a sign-in, to the sign-in, and checks what each step reports.
func TestGoogleSignIn(t *testing.T) {
	f := newFakeGoogle(t)
	var logBuf bytes.Buffer
	s, clock, sec := googleSupervisor(t, f, &logBuf)

	s.Configure(googleTable(), sec, false)
	waitPhase(t, s, phaseSignIn)
	st := s.Status()
	if st.State != StateNeedsConfig || st.Sentence != "Google needs you to sign in." || st.Link != fakeLink {
		t.Fatalf("status = %s %q link %q, want needs_config, the sign-in sentence and the link", st.State, st.Sentence, st.Link)
	}
	if len(st.Fix) != 0 {
		t.Errorf("Fix = %v; a sign-in has no config key to set", st.Fix)
	}
	if s.Tools() != nil {
		t.Error("a connector that waits for a sign-in offers tools")
	}
	if _, _, err := s.Spawn(context.Background(), time.Second); err == nil || err.Error() != "Google needs you to sign in." {
		t.Errorf("Spawn = %v, want the sign-in sentence", err)
	}
	// The program keeps running for the sign-in's callback.
	if n := f.starts.Load(); n != 1 {
		t.Errorf("started %d programs, want 1", n)
	}

	// Not signed in yet: the next check keeps waiting.
	clock.Advance(signInPoll)
	waitFor(t, "the second check", func() bool { return f.checks.Load() >= 2 })
	waitFor(t, "the next timer", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.signInTimer != nil
	})
	if p := phaseOf(s); p != phaseSignIn {
		t.Fatalf("phase = %s after a check with no sign-in, want sign_in", p)
	}

	// The user signs in; the next check passes and the program, still
	// the same one, is ok.
	f.signedIn.Store(true)
	clock.Advance(signInPoll)
	waitPhase(t, s, phaseOK)
	if st := s.Status(); st.State != StateOK || st.Sentence != "Google is running." || st.Link != "" {
		t.Errorf("status = %s %q link %q, want ok and running, with no link", st.State, st.Sentence, st.Link)
	}
	if n := f.starts.Load(); n != 1 {
		t.Errorf("started %d programs, want the one that waited", n)
	}
	if got := len(s.Tools()); got != 2 {
		t.Errorf("Tools() has %d tools, want 2", got)
	}
	cs, done, err := s.Spawn(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Spawn after the sign-in: %v", err)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_gmail_messages", Arguments: map[string]any{}}); err != nil {
		t.Errorf("call: %v", err)
	}
	done()

	// merud.log never holds the link's query.
	if strings.Contains(logBuf.String(), "state=4f2a") || strings.Contains(logBuf.String(), "client_id=") {
		t.Errorf("the log holds the sign-in link's query:\n%s", logBuf.String())
	}
}

// TestGoogleCrashWhileSignIn checks that the program that waits for a
// sign-in counts as running: when it exits, the supervisor sees a crash
// and starts it again, which gives a new link.
func TestGoogleCrashWhileSignIn(t *testing.T) {
	f := newFakeGoogle(t)
	s, clock, sec := googleSupervisor(t, f, &bytes.Buffer{})
	s.Configure(googleTable(), sec, false)
	waitPhase(t, s, phaseSignIn)

	f.crash()
	waitPhase(t, s, phaseBackoff)
	if st := s.Status(); st.Link != "" {
		t.Errorf("a crashed connector still shows the link %q", st.Link)
	}
	clock.Advance(firstBackoff)
	waitPhase(t, s, phaseSignIn)
	if n := f.starts.Load(); n != 2 {
		t.Errorf("started %d programs, want 2", n)
	}
}

// TestGoogleSignedInAlready checks the path of a user whose sign-in is
// saved: the first check passes, and the connector is ready without a
// running program, as a stdio connector is.
func TestGoogleSignedInAlready(t *testing.T) {
	f := newFakeGoogle(t)
	f.signedIn.Store(true)
	s, _, sec := googleSupervisor(t, f, &bytes.Buffer{})
	s.Configure(googleTable(), sec, false)
	waitPhase(t, s, phaseReady)
	if st := s.Status(); st.Sentence != "Google is ready. It starts when a question needs it." {
		t.Errorf("sentence = %q", st.Sentence)
	}
	// The check's program was stopped.
	f.mu.Lock()
	exit := f.exit
	f.mu.Unlock()
	select {
	case <-exit:
	case <-time.After(5 * time.Second):
		t.Error("the check's program still runs")
	}
}

// TestGooglePortBusy checks that a port another program holds sets
// needs_config with a sentence that names it, starts nothing, and that
// the supervisor looks again after portRetry, and at once on a reload.
func TestGooglePortBusy(t *testing.T) {
	f := newFakeGoogle(t)
	f.busy.Store(true)
	s, clock, sec := googleSupervisor(t, f, &bytes.Buffer{})
	s.Configure(googleTable(), sec, false)
	waitPhase(t, s, phaseNeedsConfig)
	st := s.Status()
	want := "Google can't start: another program listens on 127.0.0.1:8000. Stop that program; Meru looks again every 30 seconds."
	if st.State != StateNeedsConfig || st.Sentence != want {
		t.Errorf("status = %s %q, want needs_config %q", st.State, st.Sentence, want)
	}
	if n := f.starts.Load(); n != 0 {
		t.Errorf("started %d programs while the port was taken", n)
	}

	// Still taken after 30 seconds: still waiting.
	clock.Advance(portRetry)
	waitFor(t, "the next look", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.phase == phaseNeedsConfig && s.retryTimer != nil
	})

	// A reload with the same config tries again at once.
	f.busy.Store(false)
	f.signedIn.Store(true)
	s.Configure(googleTable(), sec, false)
	waitPhase(t, s, phaseReady)
}

// TestHTTPTransportPortBusy checks the real start of an http connector:
// with another program on the port, it fails with ErrPortBusy and starts
// nothing.
func TestHTTPTransportPortBusy(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	endpoint := "http://" + l.Addr().String() + "/mcp"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err = httpTransport(ctx, ctx, Cmd{Path: "/no/such/program"}, &bytes.Buffer{}, endpoint)
	if !errors.Is(err, ErrPortBusy) {
		t.Errorf("httpTransport = %v, want ErrPortBusy", err)
	}
	if _, _, err := httpTransport(ctx, ctx, Cmd{Path: "relative/program"}, &bytes.Buffer{}, endpoint); !errors.Is(err, ErrNotAbsolute) {
		t.Errorf("httpTransport with a relative path = %v, want ErrNotAbsolute", err)
	}
}

// TestSignInLink checks which links in a server's error text count as a
// sign-in.
func TestSignInLink(t *testing.T) {
	tests := []struct{ name, text, want string }{
		{"workspace-mcp's line", "   Authorization URL: " + fakeLink + "\n2. Then retry.", fakeLink},
		{"Markdown around it", "Open **" + fakeLink + "**.", fakeLink},
		{"an OAuth-shaped link with no label", "go to " + fakeLink + " now", fakeLink},
		{"a help page is no sign-in", "Calendar API has not been used; enable it at https://console.example.test/apis/api/calendar?project=12", ""},
		{"plain http doesn't count", "Authorization URL: http://accounts.example.test/o/oauth2/auth?client_id=a&redirect_uri=b", ""},
		{"no link", "Token has been expired or revoked.", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signInLink(tt.text); got != tt.want {
				t.Errorf("signInLink = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHideQueries checks that the error output merud logs keeps a link's
// address and drops its query.
func TestHideQueries(t *testing.T) {
	got := hideQueries("Authorization URL: " + fakeLink + " (open it)")
	want := "Authorization URL: https://accounts.example.test/o/oauth2/auth?… (open it)"
	if got != want {
		t.Errorf("hideQueries = %q, want %q", got, want)
	}
	var buf bytes.Buffer
	w := &tailLog{log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	_, _ = w.Write([]byte("  Authorization URL: " + fakeLink + "\n"))
	if strings.Contains(buf.String(), "state=") || strings.Contains(w.last(), "state=") {
		t.Errorf("the tail log kept the query: %q / %q", buf.String(), w.last())
	}
}

// TestToolListOverrides checks the tool lists a [connectors.<id>] table
// may set in place of the manifest's, as Adopt writes them, and the rules
// they follow.
func TestToolListOverrides(t *testing.T) {
	m := obsidian(t)
	vault := t.TempDir()
	table := func(lists map[string][]any) config.Connector {
		c := config.Connector{"enabled": true, "vault_path": vault}
		for k, v := range lists {
			c[k] = v
		}
		return c
	}
	st := checkSettings(m, table(map[string][]any{
		"allow":   {"obsidian_read_note", "obsidian_create_note"},
		"confirm": {"obsidian_create_note"},
	}), nil, "")
	if st.problem != "" {
		t.Fatalf("problem = %q", st.problem)
	}
	if !equalLists(st.tools.Allow, "obsidian_read_note", "obsidian_create_note") || !equalLists(st.tools.Confirm, "obsidian_create_note") {
		t.Errorf("tools = %+v", st.tools)
	}

	// No table lists: the manifest's.
	if st := checkSettings(m, table(nil), nil, ""); !equalLists(st.tools.Allow, m.MCP.Allow...) {
		t.Errorf("allow = %v, want the manifest's %v", st.tools.Allow, m.MCP.Allow)
	}

	bad := []struct {
		name  string
		lists map[string][]any
		want  string
	}{
		{"confirm outside allow", map[string][]any{"allow": {"obsidian_read_note"}, "confirm": {"obsidian_create_note"}},
			`Obsidian's tool lists in [connectors.obsidian] need a fix: confirm: "obsidian_create_note" isn't in allow.`},
		{"a wildcard", map[string][]any{"allow": {"obsidian_*"}},
			`Obsidian's tool lists in [connectors.obsidian] need a fix: allow: "obsidian_*" must name one tool; wildcards are refused.`},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			st := checkSettings(m, table(tt.lists), nil, "")
			if st.problem != tt.want {
				t.Errorf("problem = %q, want %q", st.problem, tt.want)
			}
		})
	}

	// A container has no tool lists, so the key is a mistake there.
	web := Manifest{ID: "searxng", Name: "Web search", Kind: KindContainer}
	if st := checkSettings(web, config.Connector{"allow": []any{"x"}}, nil, ""); !strings.Contains(st.problem, "has no setting called allow") {
		t.Errorf("container problem = %q", st.problem)
	}
}

// equalLists reports whether got holds want, in order.
func equalLists(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
