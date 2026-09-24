// This file holds the Client: one record per configured agent, the lazy
// fetch of each agent card, the tools the cards give the model, and the
// HTTP client that keeps a loopback agent's traffic on this machine.

package a2a

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	// The SDK's core package is also named a2a. `sdk` renames it inside
	// this file, so "a2a" always means this package and "sdk" the SDK.
	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// toolPrefix starts every tool name this package makes: "a2a.<agent>.<skill>".
const toolPrefix = "a2a."

// cardTimeout caps one agent card fetch. The card is a small JSON file, so
// an agent that takes longer is down or stuck. Tools and Status wait this
// long at most for each agent that has no card yet.
const cardTimeout = 5 * time.Second

// dialTimeout caps opening one TCP connection to an agent.
const dialTimeout = 5 * time.Second

// defaultRetryAfter is how long an agent whose card fetch failed waits
// before the next try. Without the wait, an agent that is down would cost a
// failed fetch on every Tools call and every tool call of a turn.
const defaultRetryAfter = 10 * time.Second

// messageSchema is the input schema every A2A tool shares. An A2A skill
// takes a message, so the model writes one string.
const messageSchema = `{"type":"object","properties":{"message":{"type":"string",` +
	`"description":"What to ask the agent, in plain words. The agent sees only this text, not the conversation."}},` +
	`"required":["message"]}`

// ErrNotAllowed means the model asked for a skill that no allow list names,
// or an agent that isn't configured. The Client refuses it without
// contacting any agent.
var ErrNotAllowed = errors.New("a2a skill not allowed")

// ErrUnavailable means the skill is allowed but the Client couldn't read
// the agent's card, now or within the last 10 seconds. The error text says
// why.
var ErrUnavailable = errors.New("a2a agent unavailable")

// errNotLoopback is the dialer's refusal of an address off this machine.
var errNotLoopback = errors.New("address is not loopback")

// Client reaches the configured A2A agents. It implements dispatch.Backend.
// Create it with New and stop it with Close. Its methods are safe to call
// from many goroutines at once.
type Client struct {
	log *slog.Logger

	// agents keeps config order, for Tools and Status. byName finds one by
	// the middle part of a tool name. Both are fixed after New.
	agents []*agent
	byName map[string]*agent

	// retryAfter is defaultRetryAfter outside tests.
	retryAfter time.Duration
}

// agent is the Client's record of one configured agent. cfg, allow,
// confirm and http never change after New; mu guards the fields after it.
type agent struct {
	cfg     AgentConfig
	allow   map[string]bool
	confirm map[string]bool
	http    *http.Client

	mu sync.Mutex
	// client talks to the agent. It is nil until the card fetch succeeds,
	// and again after a call finds the agent gone.
	client  *a2aclient.Client
	tools   []engine.ToolSpec // allowed skills from the last card, sorted
	offered int               // how many skills the last card listed
	unknown []string          // allow entries the last card didn't list
	lastErr string            // why the last fetch or call failed, for Status
	lastTry time.Time         // when the last card fetch started
	closed  bool              // set by Close; no fetches after it
}

// This line makes the compiler check that *Client has every method
// dispatch.Backend asks for. It costs nothing at run time.
var _ dispatch.Backend = (*Client)(nil)

// New validates agents and returns a Client for them. It fails only when
// the config is invalid.
//
// New doesn't contact any agent. The Client fetches an agent's card the
// first time Tools, Status or Call needs it, and again after a failure, at
// most once every 10 seconds. So merud starts at once even when an agent is
// down, and an agent that starts after merud shows up on the next turn.
// New does no I/O, so it ignores ctx; the parameter keeps its shape in line
// with mcp.NewPool, so merud builds both backends the same way.
func New(_ context.Context, agents []AgentConfig, log *slog.Logger) (*Client, error) {
	if err := ValidateAll(agents); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Client{
		log:        log,
		byName:     make(map[string]*agent, len(agents)),
		retryAfter: defaultRetryAfter,
	}
	for _, cfg := range agents {
		a := &agent{
			cfg:     cfg,
			allow:   toSet(cfg.Allow),
			confirm: toSet(cfg.Confirm),
			http:    newHTTPClient(cfg),
		}
		c.agents = append(c.agents, a)
		c.byName[cfg.Name] = a
	}
	return c, nil
}

// newHTTPClient returns the HTTP client for one agent. It adds the entry's
// headers to every request and follows no redirects.
//
// Unless cfg.Remote is true, its dialer refuses any address that isn't
// loopback. The config check covers only the card's URL; the card itself
// names the URL the calls go to, and this check covers that URL too, after
// DNS, so a card on loopback can't send Meru's messages off the machine.
// For the same reason it uses no proxy.
func newHTTPClient(cfg AgentConfig) *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	// A type assertion, x.(T), gets the concrete type out of an interface
	// value. DefaultTransport is an *http.Transport; Clone copies its
	// settings (connection pool sizes, TLS and HTTP/2 set-up) so this
	// client can change a few without touching the shared one.
	t := http.DefaultTransport.(*http.Transport).Clone()
	if !cfg.Remote {
		dialer.Control = refuseNonLoopback
		t.Proxy = nil
	}
	t.DialContext = dialer.DialContext

	return &http.Client{
		Transport: headerTransport{base: t, headers: cfg.Headers},
		// Headers may carry an API key, and a redirect could take it to
		// another host. A2A servers have no reason to redirect, so the
		// client refuses them all.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("redirect to %s refused: Meru doesn't follow redirects from A2A agents", req.URL.Redacted())
		},
	}
}

// refuseNonLoopback is a net.Dialer Control function: the dialer calls it
// with the resolved IP address and port just before it connects, and a
// non-nil error stops the connection.
func refuseNonLoopback(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", errNotLoopback, address)
	}
	ip, err := netip.ParseAddr(host)
	// Unmap turns an IPv4 address written as IPv6 (::ffff:127.0.0.1) back
	// into plain IPv4, so the loopback test sees it for what it is.
	if err != nil || !ip.Unmap().IsLoopback() {
		return fmt.Errorf("%w: %s (set remote = true to allow an agent on another machine)", errNotLoopback, address)
	}
	return nil
}

// headerTransport is an http.RoundTripper, the piece of an http.Client that
// sends one request. It adds the configured headers, then hands the request
// to base. Putting the headers here covers every request the SDK makes,
// the card fetch included.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

// RoundTrip sends req with the headers added. A RoundTripper must not change
// the request it is given, so it changes a copy.
func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 {
		req = req.Clone(req.Context())
		for k, v := range t.headers {
			req.Header.Set(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

// Kind returns dispatch.KindA2A.
func (c *Client) Kind() string { return dispatch.KindA2A }

// Tools returns every allowed skill from every agent, as tools named
// "a2a.<agent>.<skill>", sorted by agent in config order and by name within
// an agent. It fetches the card of any agent that has none yet (see New),
// waiting up to 5 seconds for each. An agent whose card can't be read gives
// no tools.
func (c *Client) Tools() []engine.ToolSpec {
	var out []engine.ToolSpec
	for _, a := range c.agents {
		a.mu.Lock()
		c.fetchIfDueLocked(a)
		out = append(out, a.tools...)
		a.mu.Unlock()
	}
	return out
}

// Confirm returns dispatch.ConfirmAsk for a skill in its agent's confirm
// list, and dispatch.ConfirmNever for any other name.
func (c *Client) Confirm(name string) dispatch.Confirm {
	a, skill, ok := c.lookup(name)
	if ok && a.confirm[skill] {
		return dispatch.ConfirmAsk
	}
	return dispatch.ConfirmNever
}

// Locate splits "a2a.research.summarize" into ("research", "summarize"). A
// name without the "a2a." prefix, or without a skill part, returns an empty
// agent and the name as given.
func (c *Client) Locate(name string) (server, tool string) {
	rest, ok := strings.CutPrefix(name, toolPrefix)
	if !ok {
		return "", name
	}
	// Cut splits at the first '.'. Agent names can't hold a '.', so the
	// rest, dots included, is the skill ID.
	agentName, skill, found := strings.Cut(rest, ".")
	if !found || agentName == "" || skill == "" {
		return "", name
	}
	return agentName, skill
}

// Status describes each agent, in config order, for `meru tools list`. Like
// Tools, it fetches the card of any agent that has none yet.
func (c *Client) Status() []rpc.ServerInfo {
	out := make([]rpc.ServerInfo, 0, len(c.agents))
	for _, a := range c.agents {
		a.mu.Lock()
		c.fetchIfDueLocked(a)
		info := rpc.ServerInfo{
			Name:      a.cfg.Name,
			Kind:      dispatch.KindA2A,
			Transport: "http",
			Connected: a.client != nil,
			LastError: a.lastErr,
			Offered:   a.offered,
			Unknown:   slices.Clone(a.unknown),
			Tools:     make([]rpc.ToolInfo, 0, len(a.tools)),
		}
		for _, t := range a.tools {
			_, skill, _ := c.lookup(t.Name)
			info.Tools = append(info.Tools, rpc.ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				Confirm:     a.confirm[skill],
			})
		}
		a.mu.Unlock()
		out = append(out, info)
	}
	return out
}

// Close drops every agent's connection and stops later fetches. Calls
// after Close fail with ErrUnavailable. It makes no network requests.
func (c *Client) Close() {
	for _, a := range c.agents {
		a.mu.Lock()
		a.closed = true
		if a.client != nil {
			if err := a.client.Destroy(); err != nil {
				c.log.Debug("a2a client close", "a2a_agent", a.cfg.Name, "err", err)
			}
			a.client = nil
		}
		a.mu.Unlock()
		a.http.CloseIdleConnections()
	}
}

// lookup splits a full tool name into its agent and the skill ID. ok is
// false when no configured agent has that name.
func (c *Client) lookup(name string) (a *agent, skill string, ok bool) {
	agentName, skill := c.Locate(name)
	a, ok = c.byName[agentName]
	return a, skill, ok
}

// fetchIfDueLocked fetches a's card when a has no client and the retry wait
// has passed. The caller holds a.mu. It has no context to take, because
// Tools and Status have none, so cardTimeout bounds it.
func (c *Client) fetchIfDueLocked(a *agent) {
	if a.client != nil || a.closed || time.Since(a.lastTry) < c.retryAfter {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cardTimeout)
	defer cancel()
	_ = c.fetchLocked(ctx, a)
}

// clientFor returns a's client for one call, fetching the card first if a
// has none and the retry wait has passed. It fails with ErrUnavailable
// otherwise.
func (c *Client) clientFor(ctx context.Context, a *agent) (*a2aclient.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, fmt.Errorf("%w: %s: client closed", ErrUnavailable, a.cfg.Name)
	}
	if a.client != nil {
		return a.client, nil
	}
	if wait := c.retryAfter - time.Since(a.lastTry); wait > 0 {
		return nil, fmt.Errorf("%w: %s: %s; next try in %s", ErrUnavailable, a.cfg.Name, a.lastErr, wait.Round(time.Second))
	}
	fctx, cancel := context.WithTimeout(ctx, cardTimeout)
	defer cancel()
	if err := c.fetchLocked(fctx, a); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, a.cfg.Name, err)
	}
	return a.client, nil
}

// fetchLocked reads a's agent card, builds a client from it and works out
// the allowed tools. The caller holds a.mu. On failure a.lastErr says why
// and a keeps no client.
func (c *Client) fetchLocked(ctx context.Context, a *agent) error {
	a.lastTry = time.Now()
	card, client, err := fetchCard(ctx, a)
	if err != nil {
		a.lastErr = err.Error()
		c.log.Warn("a2a agent card fetch failed", "a2a_agent", a.cfg.Name, "err", err)
		return err
	}
	a.client = client
	a.lastErr = ""
	a.offered = len(card.Skills)
	a.tools, a.unknown = allowedTools(a.cfg.Name, card, a.allow)
	c.log.Info("a2a agent card read", "a2a_agent", a.cfg.Name,
		"skills_offered", a.offered, "skills_allowed", len(a.tools), "streaming", card.Capabilities.Streaming)
	if len(a.unknown) > 0 {
		c.log.Warn("a2a agent doesn't offer some allowed skills", "a2a_agent", a.cfg.Name, "skills", a.unknown)
	}
	return nil
}

// fetchCard reads the agent card at a.cfg.URL and opens a client on one of
// the interfaces it lists. The client speaks JSON-RPC or REST over a.http;
// gRPC isn't offered, so the SDK's gRPC code stays out of merud.
func fetchCard(ctx context.Context, a *agent) (*sdk.AgentCard, *a2aclient.Client, error) {
	card, err := agentcard.NewResolver(a.http).Resolve(ctx, a.cfg.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("read agent card: %w", err)
	}
	client, err := a2aclient.NewFromCard(ctx, card,
		a2aclient.WithDefaultsDisabled(),
		a2aclient.WithJSONRPCTransport(a.http),
		a2aclient.WithRESTTransport(a.http),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	return card, client, nil
}

// allowedTools turns the card's skills that allow names into tools, sorted
// by name so the prompt stays the same from turn to turn. It also returns
// the allow entries the card doesn't list, sorted.
func allowedTools(agentName string, card *sdk.AgentCard, allow map[string]bool) (kept []engine.ToolSpec, unknown []string) {
	label := agentName
	if card.Name != "" && card.Name != agentName {
		label = fmt.Sprintf("%s (%s)", agentName, card.Name)
	}
	seen := make(map[string]bool, len(card.Skills))
	for _, s := range card.Skills {
		if seen[s.ID] {
			continue // a card that lists an ID twice gets one tool
		}
		seen[s.ID] = true
		if !allow[s.ID] {
			continue
		}
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = s.Name
		}
		kept = append(kept, engine.ToolSpec{
			Name:        toolPrefix + agentName + "." + s.ID,
			Description: fmt.Sprintf("%s (a skill of the A2A agent %s)", desc, label),
			Parameters:  []byte(messageSchema),
		})
	}
	slices.SortFunc(kept, func(x, y engine.ToolSpec) int { return strings.Compare(x.Name, y.Name) })
	for id := range allow {
		if !seen[id] {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	return kept, unknown
}

// markOK clears the last error after a call that got an answer.
func (a *agent) markOK() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastErr = ""
}

// markFailed records a failed call on a. When the agent couldn't be
// reached at all, it also drops the client, so the next use fetches the
// card again and picks up an agent that restarted on a new port.
func (a *agent) markFailed(client *a2aclient.Client, err error, unreachable bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastErr = err.Error()
	if unreachable && a.client == client {
		_ = client.Destroy()
		a.client = nil
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
