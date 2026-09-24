// This file holds the Pool: one connection per configured server, the list
// of allowed tools they offer, and how a server that dies gets reconnected.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/loopback"
)

// connectTimeout caps starting a server, the MCP handshake and the first
// tool listing. A server that takes longer is broken or still downloading
// itself ("npx -y" on a cold cache); either way merud shouldn't wait on it.
const connectTimeout = 30 * time.Second

// defaultReconnectAfter is how long a server that failed to start or died
// waits before a call may try to start it again. Without the wait, a server
// that crashes on start would be restarted on every tool call of a turn.
const defaultReconnectAfter = 10 * time.Second

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

	// reconnectAfter is defaultReconnectAfter outside tests.
	reconnectAfter time.Duration

	// watchers counts the goroutines that wait for a session to end; Close
	// waits for all of them.
	watchers sync.WaitGroup
}

// server is the Pool's record of one configured server. cfg, allow and
// confirm never change after NewPool; mu guards the fields after it.
type server struct {
	cfg     ServerConfig
	allow   map[string]bool
	confirm map[string]bool

	mu      sync.Mutex
	session *mcp.ClientSession // nil while not connected
	// stop ends the stdio child's process context. The SDK's own shutdown
	// closes stdin, then signals, then kills; stop is the backstop. It is a
	// no-op for HTTP servers.
	stop context.CancelFunc
	// tools holds the allowed tools from the last successful listing, named
	// "<server>.<tool>" and sorted. It survives a crash, so the model can
	// still call the tool and the call can restart the server.
	tools   []engine.ToolSpec
	offered int      // how many tools the server offered at the last listing
	unknown []string // allow entries the server didn't offer
	lastErr string   // why the last start or call failed, for Status
	lastTry time.Time
	closed  bool // set by Close; no reconnects after it
}

// ServerStatus is one server's health, for `meru tools list` and the logs.
type ServerStatus struct {
	Name      string
	Transport string // "stdio" or "http"
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
}

// NewPool validates servers, then starts or connects to each one, lists its
// tools and keeps the allowed ones. A server that fails to start doesn't
// fail the Pool: NewPool logs it, reports it in Status, and carries on with
// the rest. A call to one of its tools tries again later. NewPool fails only
// when the config is invalid.
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
		log: log,
		// Implementation is how Meru introduces itself in the MCP handshake.
		// Capabilities is empty on purpose: Meru offers servers no roots, no
		// sampling and no elicitation, so a server can't read the user's
		// folders or ask Meru's model for anything.
		client: mcp.NewClient(&mcp.Implementation{Name: "meru"}, &mcp.ClientOptions{
			Logger:       log,
			Capabilities: &mcp.ClientCapabilities{},
		}),
		dial:           dial,
		byName:         make(map[string]*server, len(servers)),
		reconnectAfter: defaultReconnectAfter,
	}
	for _, cfg := range servers {
		s := &server{cfg: cfg, allow: toSet(cfg.Allow), confirm: toSet(cfg.Confirm), stop: func() {}}
		p.servers = append(p.servers, s)
		p.byName[cfg.Name] = s

		s.mu.Lock()
		err := p.connectLocked(ctx, s)
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

// dialTransport builds the real transport for cfg: a child process for a
// stdio entry, or a Streamable HTTP client for a URL entry.
func dialTransport(procCtx context.Context, cfg ServerConfig, log *slog.Logger) (mcp.Transport, error) {
	if cfg.URL == "" {
		return newStdioTransport(procCtx, cfg, log), nil
	}
	return &mcp.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: httpClient(cfg.Network),
		// The standalone stream carries notifications the server sends on
		// its own, such as "my tool list changed". Meru lists tools when it
		// connects and ignores those, so it doesn't hold the stream open.
		DisableStandaloneSSE: true,
	}, nil
}

// httpClient returns the HTTP client for a Streamable HTTP server. Unless
// network is true, it refuses to follow a redirect off this machine: a
// loopback server must not be able to send merud's request elsewhere.
func httpClient(network bool) *http.Client {
	if network {
		return &http.Client{}
	}
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := loopback.CheckURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect refused: %w", err)
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// connectLocked starts or connects to s, runs the handshake and lists its
// tools. The caller holds s.mu. On success s has a live session and a fresh
// tool list; on failure s.lastErr says why and nothing is left running.
func (p *Pool) connectLocked(ctx context.Context, s *server) error {
	s.lastTry = time.Now()
	err := p.tryConnectLocked(ctx, s)
	if err != nil {
		s.lastErr = err.Error()
		return err
	}
	s.lastErr = ""
	return nil
}

// tryConnectLocked does connectLocked's work and returns its error.
func (p *Pool) tryConnectLocked(ctx context.Context, s *server) error {
	// The child process must outlive ctx, which may be a startup deadline
	// or one tool call. So its context starts from Background, and s.stop
	// ends it at Close or when the session dies.
	procCtx, stop := context.WithCancel(context.Background())
	t, err := p.dial(procCtx, s.cfg, p.log)
	if err != nil {
		stop()
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
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
	s.offered = len(tools)
	s.tools, s.unknown = allowedTools(s.cfg.Name, s.allow, tools)

	// A goroutine is a function running at the same time as the rest of
	// the program; `go` starts one. This one waits for the session to end,
	// so a server that dies shows as disconnected in Status at once.
	p.watchers.Add(1)
	go p.watch(s, cs)
	return nil
}

// watch waits until cs ends, from Close or because the server went away.
// If cs is still s's current session, the server died: watch marks it
// disconnected and cleans up, so the next call starts it again.
func (p *Pool) watch(s *server, cs *mcp.ClientSession) {
	defer p.watchers.Done()
	err := cs.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != cs {
		return // Close or a reconnect already replaced it
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

// Tools returns every allowed tool from every server, named
// "<server>.<tool>", sorted by server in config order and by name within a
// server. A server that died keeps its tools here, so the model can still
// call one and the call can restart it. A server that never started has
// none.
func (p *Pool) Tools() []engine.ToolSpec {
	var out []engine.ToolSpec
	for _, s := range p.servers {
		s.mu.Lock()
		out = append(out, s.tools...)
		s.mu.Unlock()
	}
	return out
}

// Status reports each server's health, in config order.
func (p *Pool) Status() []ServerStatus {
	out := make([]ServerStatus, 0, len(p.servers))
	for _, s := range p.servers {
		s.mu.Lock()
		out = append(out, ServerStatus{
			Name:      s.cfg.Name,
			Transport: s.cfg.transport(),
			Connected: s.session != nil,
			Offered:   s.offered,
			Allowed:   len(s.tools),
			Unknown:   slices.Clone(s.unknown),
			LastError: s.lastErr,
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
	return ok && s.confirm[tool]
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

// sessionFor returns s's live session, starting the server again first if
// it died or never started and the reconnect wait has passed.
func (p *Pool) sessionFor(ctx context.Context, s *server) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("%w: %s: pool closed", ErrUnavailable, s.cfg.Name)
	}
	if s.session != nil {
		return s.session, nil
	}
	if wait := p.reconnectAfter - time.Since(s.lastTry); wait > 0 {
		return nil, fmt.Errorf("%w: %s: %s; next try in %s", ErrUnavailable, s.cfg.Name, s.lastErr, wait.Round(time.Second))
	}
	if err := p.connectLocked(ctx, s); err != nil {
		p.log.Warn("mcp server failed to restart", "mcp_server", s.cfg.Name, "err", err)
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, s.cfg.Name, err)
	}
	p.log.Info("mcp server reconnected", "mcp_server", s.cfg.Name, "tools_allowed", len(s.tools))
	return s.session, nil
}

// markOK clears the last error after a call that got an answer.
func (s *server) markOK() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = ""
}

// markFailed records a failed call on s. When the error means the
// connection is gone, it also drops the session, so the next call starts the
// server again instead of failing on a dead one.
func (s *server) markFailed(cs *mcp.ClientSession, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err.Error()
	if errors.Is(err, mcp.ErrConnectionClosed) && s.session == cs {
		s.session = nil
		_ = cs.Close()
		s.stop()
	}
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
