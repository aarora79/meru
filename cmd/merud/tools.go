// This file builds the tools the model may use and answers the tool ops. A
// toolService owns the secrets, the MCP pool, the A2A client, the built-in
// tools and the dispatcher that joins them, and swaps in a new MCP pool
// when the configure tool changes config.toml.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/a2a"
	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/mcp"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
	"github.com/aarora79/meru/internal/store"
)

// defaultLogLimit is how many rows `meru log` shows when it doesn't say.
const defaultLogLimit = 20

// maxLogResult caps each result in a "log" event. The row keeps more; the
// terminal needs a glimpse.
const maxLogResult = 300

// toolService holds everything tool calls need while merud runs.
type toolService struct {
	configPath string
	st         *store.Store
	log        *slog.Logger
	a2a        *a2a.Client
	dispatcher *dispatch.Dispatcher

	mu      sync.Mutex       // guards secrets and pool
	secrets *secrets.Secrets // swapped on reload
	pool    *mcp.Pool        // swapped on reload
	reload  sync.Mutex       // lets one reload run at a time
}

// newToolService loads secrets.toml, starts the MCP pool and the A2A
// client, builds the built-in tools, and joins them in one dispatcher that
// writes its rows to st. It then rebuilds the tool_calls table from the
// transcripts if the table is empty, so a deleted meru.db loses no history.
//
// It fails when secrets.toml can't be read or is readable by others, or
// when a server or agent entry is wrong; merud then refuses to start, so a
// bad entry shows at once.
func newToolService(ctx context.Context, cfg config.Config, configPath string, st *store.Store, log *slog.Logger) (*toolService, error) {
	sec, err := secrets.Load(secrets.Path(cfg.Dir))
	if err != nil {
		return nil, err
	}
	pool, err := newPool(ctx, cfg.MCP.Servers, sec, log)
	if err != nil {
		return nil, err
	}
	agents, err := a2aAgents(cfg.A2A.Agents, sec.Resolve)
	if err != nil {
		pool.Close()
		return nil, err
	}
	ac, err := a2a.New(ctx, agents, log)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("a2a: %w", err)
	}

	s := &toolService{configPath: configPath, st: st, log: log, a2a: ac, secrets: sec, pool: pool}
	bt := builtin.New(configPath, cfg.Builtin, s.reloadMCP)
	// Backend order decides which one keeps a tool name two of them offer:
	// the built-ins first, so no server can shadow configure.
	s.dispatcher = dispatch.New(
		[]dispatch.Backend{bt, mcpBackend{pool: pool}, ac},
		st,
		dispatch.Options{Redact: s.redact, Log: log},
	)

	n, err := st.ReplayToolCalls(ctx, filepath.Join(cfg.Dir, "sessions"))
	if err != nil {
		// The table is a copy of the transcripts, so a failed replay costs
		// `meru log` some history, not the answer to any question.
		log.Warn("rebuild tool_calls from transcripts", "err", err)
	} else if n > 0 {
		log.Info("tool_calls rebuilt from transcripts", "calls", n)
	}
	log.Info("tools ready", "mcp_servers", len(cfg.MCP.Servers), "a2a_agents", len(agents),
		"tools", len(s.dispatcher.Tools()))
	return s, nil
}

// newPool resolves the secrets in each server entry and starts the MCP
// pool.
func newPool(ctx context.Context, servers []config.MCPServer, sec *secrets.Secrets, log *slog.Logger) (*mcp.Pool, error) {
	cfgs, err := mcpServerConfigs(servers, sec.Resolve)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	pool, err := mcp.NewPool(ctx, cfgs, log)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	return pool, nil
}

// a2aAgents turns the [[a2a.agents]] entries into the A2A client's
// settings, resolving secrets in the headers and parsing the timeouts.
// It reports every problem at once and never puts a value in an error.
func a2aAgents(agents []config.A2AAgent, resolve func(string) (string, error)) ([]a2a.AgentConfig, error) {
	var errs []error
	out := make([]a2a.AgentConfig, 0, len(agents))
	for _, a := range agents {
		c := a2a.AgentConfig{
			Name: a.Name, URL: a.URL, Network: a.Network,
			Allow: a.Allow, Confirm: a.Confirm,
		}
		if a.Timeout != "" {
			d, err := time.ParseDuration(a.Timeout)
			if err != nil {
				errs = append(errs, fmt.Errorf("a2a agent %q: timeout: %w", a.Name, err))
			}
			c.Timeout = d
		}
		var err error
		if c.Headers, err = resolveAll(a.Headers, resolve); err != nil {
			errs = append(errs, fmt.Errorf("a2a agent %q: headers %w", a.Name, err))
		}
		out = append(out, c)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := a2a.ValidateAll(out); err != nil {
		return nil, fmt.Errorf("a2a: %w", err)
	}
	return out, nil
}

// redact hides every secret in text. It reads the current secrets, so a
// key added through a reload is hidden from then on.
func (s *toolService) redact(text string) string {
	s.mu.Lock()
	sec := s.secrets
	s.mu.Unlock()
	return sec.Redact(text)
}

// reloadMCP is the configure tool's change hook. It reads config.toml and
// secrets.toml again, starts a new MCP pool, and swaps it in for the old
// one, which it then closes. A call still running on the old pool fails;
// configure runs rarely, and the user approved it a moment ago.
//
// It fails, leaving the old pool in place, when config or secrets don't
// load or a server entry is wrong.
func (s *toolService) reloadMCP(ctx context.Context) error {
	s.reload.Lock()
	defer s.reload.Unlock()

	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	sec, err := secrets.Load(secrets.Path(cfg.Dir))
	if err != nil {
		return err
	}
	pool, err := newPool(ctx, cfg.MCP.Servers, sec, s.log)
	if err != nil {
		return err
	}

	s.mu.Lock()
	old := s.pool
	s.pool, s.secrets = pool, sec
	s.mu.Unlock()
	s.dispatcher.Replace(dispatch.KindMCP, mcpBackend{pool: pool})
	old.Close()
	s.log.Info("mcp servers reloaded", "servers", len(cfg.MCP.Servers), "tools", len(s.dispatcher.Tools()))
	return nil
}

// Close stops the MCP servers merud started and the A2A client.
func (s *toolService) Close() {
	s.mu.Lock()
	pool := s.pool
	s.mu.Unlock()
	pool.Close()
	s.a2a.Close()
}

// handleTools answers OpTools with one "tools" event listing every source.
func (s *toolService) handleTools(emit func(rpc.Event) error) error {
	return emit(rpc.Event{Type: rpc.EventTools, Servers: s.dispatcher.Servers()})
}

// handleLog answers OpLog with one "log" event: the latest limit rows of
// tool_calls, newest first, each result cut to maxLogResult characters.
func (s *toolService) handleLog(ctx context.Context, limit int, emit func(rpc.Event) error) error {
	if limit <= 0 {
		limit = defaultLogLimit
	}
	rows, err := s.st.ToolCalls(ctx, limit)
	if err != nil {
		return err
	}
	entries := make([]rpc.LogEntry, len(rows))
	for i, r := range rows {
		entries[i] = rpc.LogEntry{
			Time: r.Time.Format(time.RFC3339), Session: r.Session,
			Kind: r.Kind, Server: r.Server, Tool: r.Tool,
			Args: r.Args, Result: rpc.Cut(r.Result, maxLogResult),
			Outcome: r.Outcome, Approval: r.Approval,
			DurationMillis: r.DurationMillis, TraceID: r.TraceID,
		}
	}
	return emit(rpc.Event{Type: rpc.EventLog, Log: entries})
}
