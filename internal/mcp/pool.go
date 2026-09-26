// This file holds the Pool: one connection per configured server, the list
// of allowed tools they offer, and the one rule for connecting: once at
// startup, and once more at the start of a turn that offers tools
// (Refresh). The same turn asks each connected server for its tool list
// again, so the list follows a server that restarted with new tools.
// Nothing reconnects or re-lists on a timer or in the background.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/loopback"
	"github.com/aarora79/meru/internal/obs"
)

// connectTimeout caps starting a server, the MCP handshake and the first
// tool listing, at startup, in a probe, and for a stdio server a turn tries
// again. A server that takes longer is broken or still downloading itself
// ("npx -y" on a cold cache); either way merud shouldn't wait on it.
const connectTimeout = 30 * time.Second

// httpRetryTimeout caps a turn's try at an HTTP server that wasn't
// connected. The server is somebody else's process and either answers at
// once or isn't running, so a turn shouldn't wait longer on it.
const httpRetryTimeout = 5 * time.Second

// relistTimeout caps the tools/list a turn sends to a server that is
// already connected. On loopback the answer takes well under a millisecond
// (ARCHITECTURE.md, "MCP"), so two seconds only trips on a server that is
// stuck, and a turn shouldn't wait longer on one.
const relistTimeout = 2 * time.Second

// dialFunc builds the transport for one server. procCtx bounds a stdio
// child's life. NewPool uses dialTransport; tests swap in an in-memory
// transport.
type dialFunc func(procCtx context.Context, cfg ServerConfig, log *slog.Logger) (mcp.Transport, error)

// Pool holds a connection to each configured MCP server. Create it with
// NewPool and stop it with Close. Its methods are safe to call from many
// goroutines at once.
type Pool struct {
	log    *slog.Logger
	client *mcp.Client
	dial   dialFunc

	// servers keeps config order, for Status. byName finds one by the part
	// of a tool name before the '.'. Both are fixed after NewPool.
	servers []*server
	byName  map[string]*server

	// watchers counts the goroutines that wait for a session to end; Close
	// waits for all of them.
	watchers sync.WaitGroup
}

// server is the Pool's record of one configured server. cfg, allow,
// confirm and always never change after NewPool; mu guards the fields
// after it.
type server struct {
	cfg     ServerConfig
	allow   map[string]bool
	confirm map[string]bool
	always  map[string]bool // AlwaysConfirm

	mu      sync.Mutex
	session *mcp.ClientSession // nil while not connected
	// stop ends the stdio child's process context. The SDK's own shutdown
	// closes stdin, then signals, then kills; stop is the backstop. It is a
	// no-op for HTTP servers.
	stop context.CancelFunc
	// tools holds the allowed tools from the last successful listing, named
	// "<server>.<tool>" and sorted. Tools offers them only while session is
	// set, so a server that died drops out of the next turn's tool list.
	tools   []engine.ToolSpec
	offered int      // how many tools the server offered at the last listing
	unknown []string // allow entries the server didn't offer
	lastErr string   // why the last start or call failed, for Status
	closed  bool     // set by Close; no connects after it
}

// ServerStatus is one server's health, for `meru tools list`, `meru mcp`
// and the logs. Status builds it from what the Pool holds, without asking
// the server anything.
type ServerStatus struct {
	Name      string
	Transport string // "stdio" or "http"
	URL       string // the endpoint of an HTTP server; "" for stdio
	Connected bool
	// Offered counts the tools the server lists; Allowed counts the ones
	// the model sees. Both come from the last successful listing.
	Offered int
	Allowed int
	// Unknown holds allow entries the server doesn't offer, usually typos.
	Unknown []string
	// LastError says why the last start or call failed. A successful start
	// or call clears it.
	LastError string
	// Listed counts the tools config allows, and Confirms the allowed tools
	// that ask first (confirm and always_confirm together). Both come from
	// config, so they show while the server is down.
	Listed   int
	Confirms int
}

// NewPool validates servers, then starts or connects to each one, lists its
// tools and keeps the allowed ones. A server that fails to start doesn't
// fail the Pool: NewPool logs it, reports it in Status, and carries on with
// the rest. Refresh tries it again at the start of the next turn that
// offers tools. NewPool fails only when the config is invalid.
//
// ctx bounds the startup work only. The child processes live until Close.
//
// The servers start one after another. Most setups have a handful, and each
// start is bounded by connectTimeout.
func NewPool(ctx context.Context, servers []ServerConfig, log *slog.Logger) (*Pool, error) {
	return newPool(ctx, servers, log, dialTransport)
}

// newPool is NewPool with the transport builder as a parameter, so tests can
// connect over an in-memory transport.
func newPool(ctx context.Context, servers []ServerConfig, log *slog.Logger, dial dialFunc) (*Pool, error) {
	if err := ValidateAll(servers); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p := &Pool{
		log:    log,
		client: newClient(log),
		dial:   dial,
		byName: make(map[string]*server, len(servers)),
	}
	for _, cfg := range servers {
		s := &server{cfg: cfg, allow: toSet(cfg.Allow), confirm: toSet(cfg.Confirm),
			always: toSet(cfg.AlwaysConfirm), stop: func() {}}
		p.servers = append(p.servers, s)
		p.byName[cfg.Name] = s

		s.mu.Lock()
		err := p.connectLocked(ctx, s, connectTimeout)
		s.mu.Unlock()
		if err != nil {
			log.Warn("mcp server failed to start", "mcp_server", cfg.Name, "transport", cfg.transport(), "err", err)
			continue
		}
		log.Info("mcp server connected", "mcp_server", cfg.Name, "transport", cfg.transport(),
			"tools_offered", s.offered, "tools_allowed", len(s.tools))
		if len(s.unknown) > 0 {
			log.Warn("mcp server doesn't offer some allowed tools", "mcp_server", cfg.Name, "tools", s.unknown)
		}
	}
	return p, nil
}

// newClient returns the MCP client the Pool and Probe connect with. The
// Implementation is how Meru introduces itself in the MCP handshake.
// Capabilities is empty on purpose: Meru offers servers no roots, no
// sampling and no elicitation, so a server can't read the user's folders or
// ask Meru's model for anything.
func newClient(log *slog.Logger) *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "meru"}, &mcp.ClientOptions{
		Logger:       log,
		Capabilities: &mcp.ClientCapabilities{},
	})
}

// dialTransport builds the real transport for cfg: a child process for a
// stdio entry, or a Streamable HTTP client for a URL entry.
func dialTransport(procCtx context.Context, cfg ServerConfig, log *slog.Logger) (mcp.Transport, error) {
	if cfg.URL == "" {
		return newStdioTransport(procCtx, cfg, log), nil
	}
	return &mcp.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: httpClient(cfg),
		// The standalone stream carries notifications the server sends on
		// its own, such as "my tool list changed". Meru lists tools again
		// at the start of each turn that offers them (Refresh) and ignores
		// those, so it doesn't hold the stream open.
		DisableStandaloneSSE: true,
	}, nil
}

// httpClient returns the HTTP client for a Streamable HTTP server. Unless
// cfg.Remote is true, it refuses to follow a redirect off this machine: a
// loopback server must not be able to send merud's request elsewhere. When
// cfg has Headers, the client adds them to each request (see
// headerTransport).
func httpClient(cfg ServerConfig) *http.Client {
	c := &http.Client{}
	// cfg passed Validate, so its URL parses; the check only guards a nil u.
	if u, err := url.Parse(cfg.URL); err == nil && len(cfg.Headers) > 0 {
		c.Transport = &headerTransport{base: http.DefaultTransport, host: u.Host, headers: cfg.Headers}
	}
	if cfg.Remote {
		return c
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := loopback.CheckURL(req.URL.String()); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return c
}

// headerTransport adds the server's configured headers to each request. It
// is an http.RoundTripper, the interface an http.Client sends requests
// through; this one wraps base, the transport that does the sending.
//
// It adds the headers only when the request goes to host, the server's own
// address. A server marked remote = true may redirect elsewhere, and an
// API key must not follow the redirect.
type headerTransport struct {
	base    http.RoundTripper
	host    string
	headers map[string]string
}

// RoundTrip sends req through base with the headers added. A RoundTripper
// must not change the request it gets, so RoundTrip changes a copy.
func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != t.host {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// connectLocked starts or connects to s, runs the handshake and lists its
// tools, all within limit. The caller holds s.mu. On success s has a live
// session and a fresh tool list; on failure s.lastErr says why and nothing
// is left running.
func (p *Pool) connectLocked(ctx context.Context, s *server, limit time.Duration) error {
	err := p.tryConnectLocked(ctx, s, limit)
	if err != nil {
		s.lastErr = err.Error()
		return err
	}
	s.lastErr = ""
	return nil
}

// tryConnectLocked does connectLocked's work and returns its error.
func (p *Pool) tryConnectLocked(ctx context.Context, s *server, limit time.Duration) error {
	// The child process must outlive ctx, which may be a startup deadline
	// or one tool call. So its context starts from Background, and s.stop
	// ends it at Close or when the session dies.
	procCtx, stop := context.WithCancel(context.Background())
	t, err := p.dial(procCtx, s.cfg, p.log)
	if err != nil {
		stop()
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cs, err := p.client.Connect(cctx, t, nil)
	if err != nil {
		stop()
		return fmt.Errorf("connect: %w", err)
	}
	tools, err := listTools(cctx, cs)
	if err != nil {
		_ = cs.Close()
		stop()
		return fmt.Errorf("list tools: %w", err)
	}

	s.session = cs
	s.stop = stop
	s.setToolsLocked(tools)

	// A goroutine is a function running at the same time as the rest of
	// the program; `go` starts one. This one waits for the session to end,
	// so a server that dies shows as not connected in Status at once. It
	// only records the death; it never starts the server again.
	p.watchers.Add(1)
	go p.watch(s, cs)
	return nil
}

// watch waits until cs ends, from Close or because the server went away.
// If cs is still s's current session, the server died: watch marks it not
// connected and cleans up. It doesn't reconnect, wait or retry; the next
// turn that offers tools tries the server once (Refresh), the same rule as
// for a server that never started.
func (p *Pool) watch(s *server, cs *mcp.ClientSession) {
	defer p.watchers.Done()
	err := cs.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != cs {
		return // Close, Refresh or a failed call already replaced it
	}
	s.session = nil
	if err == nil {
		err = errors.New("connection closed")
	}
	s.lastErr = "server went away: " + err.Error()
	p.log.Warn("mcp server went away", "mcp_server", s.cfg.Name, "err", err)
	// Close reaps the child process (for stdio) so no zombie stays behind.
	_ = cs.Close()
	s.stop()
}

// setToolsLocked keeps the allowed tools out of a fresh listing and
// records how many the server offered and which allow entries it lacks.
// The caller holds s.mu.
func (s *server) setToolsLocked(tools []*mcp.Tool) {
	s.offered = len(tools)
	s.tools, s.unknown = allowedTools(s.cfg.Name, s.allow, tools)
}

// listTools returns every tool the server offers. Tools is an iterator that
// fetches the next page from the server when the loop needs it.
func listTools(ctx context.Context, cs *mcp.ClientSession) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// allowedTools keeps the tools whose names are in allow, renamed to
// "<server>.<tool>" and sorted by name. A stable order keeps the prompt the
// same from turn to turn. It also returns the allow entries no tool matched.
func allowedTools(serverName string, allow map[string]bool, tools []*mcp.Tool) (kept []engine.ToolSpec, unknown []string) {
	seen := make(map[string]bool, len(tools))
	for _, t := range tools {
		seen[t.Name] = true
		if !allow[t.Name] {
			continue
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil || string(schema) == "null" {
			// The spec requires a schema; an empty object schema lets the
			// model call a tool that takes no arguments.
			schema = json.RawMessage(`{"type":"object"}`)
		}
		kept = append(kept, engine.ToolSpec{
			Name:        serverName + "." + t.Name,
			Description: t.Description,
			Parameters:  schema,
		})
	}
	slices.SortFunc(kept, func(a, b engine.ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	for name := range allow {
		if !seen[name] {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	return kept, unknown
}

// Tools returns every allowed tool from every connected server, named
// "<server>.<tool>", sorted by server in config order and by name within a
// server. A server that isn't connected, because it never started or
// because it died, offers nothing: the model can't call a tool whose server
// isn't there.
func (p *Pool) Tools() []engine.ToolSpec {
	var out []engine.ToolSpec
	for _, s := range p.servers {
		s.mu.Lock()
		if s.session != nil {
			out = append(out, s.tools...)
		}
		s.mu.Unlock()
	}
	return out
}

// Refresh brings each server up to date for this turn, one server after
// another, and returns when every server has had its turn. The agent loop
// calls it at the start of a turn that offers tools, before it builds the
// tool list.
//
// For a connected server it sends tools/list again and keeps the answer,
// so a server that restarted with new tools offers them on this turn
// (relistLocked). For a server that isn't connected, or one whose listing
// failed, it makes one try to start or connect, which lists the tools
// afresh: a server the user started after merud, or a stdio
// child that crashed, is back for this turn.
//
// This is the only reconnect path, and it runs only when a turn asks. There
// is no timer, no background goroutine and no backoff: a server that fails
// here is tried again at the next such turn, and a server nobody needs is
// never touched (ARCHITECTURE.md, "MCP"). A connect try is bounded by
// httpRetryTimeout for an HTTP server, which merud never starts, and by
// connectTimeout for a stdio server, which it starts as a child.
//
// A failure is logged and recorded for Status; the turn carries on without
// that server's tools.
func (p *Pool) Refresh(ctx context.Context) {
	for _, s := range p.servers {
		s.mu.Lock()
		p.refreshLocked(ctx, s)
		s.mu.Unlock()
	}
}

// refreshLocked does Refresh's work for one server. The caller holds s.mu,
// so a call to this server waits until the refresh ends.
func (p *Pool) refreshLocked(ctx context.Context, s *server) {
	if s.closed {
		return
	}
	if s.session != nil {
		p.relistLocked(ctx, s)
	}
	if s.session != nil {
		return
	}
	limit := connectTimeout
	if s.cfg.URL != "" {
		limit = httpRetryTimeout
	}
	if err := p.connectLocked(ctx, s, limit); err != nil {
		p.log.Warn("mcp server still not connected", "mcp_server", s.cfg.Name, "err", err)
		return
	}
	p.log.Info("mcp server connected", "mcp_server", s.cfg.Name,
		"tools_offered", s.offered, "tools_allowed", len(s.tools))
	if len(s.unknown) > 0 {
		p.log.Warn("mcp server doesn't offer some allowed tools", "mcp_server", s.cfg.Name, "tools", s.unknown)
	}
}

// relistLocked asks a connected server for its tools again and replaces
// the Pool's list with the answer. The caller holds s.mu.
//
// Only this turn-start listing catches a Streamable HTTP server that
// restarted: merud holds no stream open to it, so nothing arrives when it
// goes away, and the old session looks alive until merud sends something.
// A stdio server can't restart behind merud's back, since merud owns the
// process and watch sees it exit; listing it again costs one round trip
// over a pipe and catches a server that changes its tools while it runs.
// One rule for both transports keeps the code short.
//
// When the listing fails, relistLocked drops the session, and
// refreshLocked then makes its one connect try, which lists the tools
// afresh. It doesn't pick through the error first: a restarted server may
// answer "session not found" as the spec says, or put a JSON-RPC error in
// the body, or not answer at all, and a server that can't list its tools
// can't serve the turn either way. The one exception is a listing that ran
// out of time or whose turn ended: that keeps the session and the old
// list, since a slow answer doesn't prove the session is gone, and closing
// it would cut off another turn's call in flight.
func (p *Pool) relistLocked(ctx context.Context, s *server) {
	// The span follows the OpenTelemetry MCP conventions, like the one Call
	// records, so a trace shows what the listing added to the turn.
	ctx, span := obs.Tracer().Start(ctx, "tools/list",
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("mcp.method.name", "tools/list"),
			attribute.String("meru.tool.server", s.cfg.Name)))
	defer span.End()

	lctx, cancel := context.WithTimeout(ctx, relistTimeout)
	defer cancel()
	tools, err := listTools(lctx, s.session)
	if err != nil {
		obs.EndSpanErr(ctx, span, err)
		s.lastErr = "list tools: " + err.Error()
		if lctx.Err() != nil {
			p.log.Warn("mcp server slow to list its tools; keeping the old list",
				"mcp_server", s.cfg.Name, "err", err)
			return
		}
		p.log.Warn("mcp server failed to list its tools; reconnecting", "mcp_server", s.cfg.Name, "err", err)
		s.dropLocked()
		return
	}
	s.lastErr = ""

	before, offeredBefore, unknownBefore := s.tools, s.offered, s.unknown
	s.setToolsLocked(tools)
	if s.offered == offeredBefore && sameNames(before, s.tools) {
		return
	}
	p.log.Info("mcp server tool list changed", "mcp_server", s.cfg.Name,
		"tools_offered_before", offeredBefore, "tools_offered", s.offered,
		"tools_allowed_before", len(before), "tools_allowed", len(s.tools))
	added, removed := diffNames(before, s.tools)
	p.log.Debug("mcp server allowed tools changed", "mcp_server", s.cfg.Name,
		"added", added, "removed", removed)
	if len(s.unknown) > 0 && !slices.Equal(s.unknown, unknownBefore) {
		p.log.Warn("mcp server doesn't offer some allowed tools", "mcp_server", s.cfg.Name, "tools", s.unknown)
	}
}

// sameNames reports whether a and b hold the same tool names in the same
// order. Both come from allowedTools, which sorts them.
func sameNames(a, b []engine.ToolSpec) bool {
	return slices.EqualFunc(a, b, func(x, y engine.ToolSpec) bool { return x.Name == y.Name })
}

// diffNames returns the tool names in after but not before (added), and
// the ones in before but not after (removed).
func diffNames(before, after []engine.ToolSpec) (added, removed []string) {
	// has is a function value: a small function stored in a variable, so
	// both loops below can use it.
	has := func(list []engine.ToolSpec, name string) bool {
		return slices.ContainsFunc(list, func(t engine.ToolSpec) bool { return t.Name == name })
	}
	for _, t := range after {
		if !has(before, t.Name) {
			added = append(added, t.Name)
		}
	}
	for _, t := range before {
		if !has(after, t.Name) {
			removed = append(removed, t.Name)
		}
	}
	return added, removed
}

// Status reports each server's health, in config order. It reads only what
// the Pool holds, so it sends nothing to any server and answers at once
// while one is down.
func (p *Pool) Status() []ServerStatus {
	out := make([]ServerStatus, 0, len(p.servers))
	for _, s := range p.servers {
		// asks counts each tool once, whether confirm, always_confirm or
		// both name it.
		asks := len(s.confirm)
		for tool := range s.always {
			if !s.confirm[tool] {
				asks++
			}
		}
		s.mu.Lock()
		out = append(out, ServerStatus{
			Name:      s.cfg.Name,
			Transport: s.cfg.transport(),
			URL:       s.cfg.URL,
			Connected: s.session != nil,
			Offered:   s.offered,
			Allowed:   len(s.tools),
			Unknown:   slices.Clone(s.unknown),
			LastError: s.lastErr,
			Listed:    len(s.allow),
			Confirms:  asks,
		})
		s.mu.Unlock()
	}
	return out
}

// NeedsConfirm reports whether the namespaced tool name is in its server's
// confirm list. It returns false for a name the Pool doesn't know; Call
// refuses those anyway.
func (p *Pool) NeedsConfirm(name string) bool {
	s, tool, ok := p.lookup(name)
	return ok && (s.confirm[tool] || s.always[tool])
}

// AlwaysConfirms reports whether the namespaced tool name is in its
// server's always_confirm list: it asks on every call, and no approval for
// the session covers it.
func (p *Pool) AlwaysConfirms(name string) bool {
	s, tool, ok := p.lookup(name)
	return ok && s.always[tool]
}

// Close ends every session and stops every stdio child, then waits for them
// to exit. Each child gets its stdin closed first, the MCP spec's polite
// "please exit", then a signal, then a kill (see terminateWait). Calls after
// Close fail with ErrUnavailable.
func (p *Pool) Close() {
	for _, s := range p.servers {
		s.mu.Lock()
		cs := s.session
		s.session = nil
		s.closed = true
		stop := s.stop
		s.mu.Unlock()

		if cs != nil {
			if err := cs.Close(); err != nil {
				p.log.Debug("mcp server close", "mcp_server", s.cfg.Name, "err", err)
			}
		}
		stop()
	}
	p.watchers.Wait()
}

// lookup splits a namespaced tool name into its server and the server's own
// tool name. ok is false when no configured server has that name.
func (p *Pool) lookup(name string) (s *server, tool string, ok bool) {
	// Cut splits at the first '.'. Server names can't hold a '.', so the
	// rest, dots included, is the tool's own name.
	serverName, tool, found := strings.Cut(name, ".")
	if !found {
		return nil, "", false
	}
	s, ok = p.byName[serverName]
	return s, tool, ok
}

// sessionFor returns s's live session. It never starts or connects to the
// server: a call to a server that isn't connected fails at once with
// ErrUnavailable and the last error. Only Refresh, at the start of a turn,
// tries again.
func (p *Pool) sessionFor(s *server) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, fmt.Errorf("%w: %s: pool closed", ErrUnavailable, s.cfg.Name)
	case s.session == nil:
		return nil, fmt.Errorf("%w: %s: not connected: %s", ErrUnavailable, s.cfg.Name, s.lastErr)
	}
	return s.session, nil
}

// markOK clears the last error after a call that got an answer.
func (s *server) markOK() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = ""
}

// markFailed records a failed call on s. When the error means the session
// is gone (sessionGone), it also drops the session, so the server shows as
// not connected and the next turn that offers tools tries it once more.
func (s *server) markFailed(cs *mcp.ClientSession, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err.Error()
	if sessionGone(err) && s.session == cs {
		s.dropLocked()
	}
}

// dropLocked closes s's session, stops its child process if it has one,
// and marks s not connected. watch then sees the session end, finds it no
// longer current and leaves s alone. The caller holds s.mu.
func (s *server) dropLocked() {
	cs := s.session
	s.session = nil
	if cs != nil {
		_ = cs.Close()
	}
	s.stop()
}

// sessionGone reports whether err means the server no longer has merud's
// session, so every later request on it would fail too. It covers three
// cases: the connection closed (a stdio child exited), the server answered
// "session not found" (a Streamable HTTP server that restarted), and
// nothing listens at the server's address (one that stopped). The last
// shows up as a failed dial. A bare EOF doesn't count: the server may only
// have closed an idle keep-alive connection, which a running server does
// too. If the session did go, the next Refresh finds out when its listing
// fails. errors.As looks through the chain of wrapped errors for a
// *net.OpError and, when it finds one, stores it in opErr.
func sessionGone(err error) bool {
	if errors.Is(err, mcp.ErrConnectionClosed) || errors.Is(err, mcp.ErrSessionMissing) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// toSet turns a list of names into a set, for fast "is it in the list?"
// checks. A map whose values are all true is Go's usual set.
func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
