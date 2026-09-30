// This file builds the tools the model may use and answers the tool ops. A
// toolService owns the secrets, the MCP pool, the A2A client, the built-in
// tools, the local commands and the dispatcher that joins them, and swaps in a new MCP pool
// when the configure tool changes config.toml or a client asks for a
// reload. It also answers the probe op, which tries a server before the
// user adds it.

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
	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/commands"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/mcp"
	"github.com/aarora79/meru/internal/memory"
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
	dir        string // the Meru home, which holds secrets.toml
	st         *store.Store
	log        *slog.Logger
	bt         *builtin.Tools // the built-in tools; their lock guards every config.toml write
	dispatcher *dispatch.Dispatcher

	// started is config as merud last loaded each part of it. A reload
	// takes only the part it reloads, [mcp], [a2a] or [builtin], from
	// config.toml; the commands keep what merud started with. The router's
	// list of what is connected comes from it. mu guards it.
	started config.Config

	// conns holds the connector supervisors. It is set once and never
	// swapped: a reload hands it new config, and the pool it builds asks
	// the same supervisors for sessions.
	conns *connectorSet

	mu        sync.Mutex       // guards secrets, pool, a2a and connected
	secrets   *secrets.Secrets // swapped on reload
	pool      *mcp.Pool        // swapped on reload
	a2a       *a2a.Client      // swapped when the desktop app changes an agent's skills
	connected []string         // what the router's prompt names; rebuilt on reload
	reload    sync.Mutex       // lets one reload run at a time
}

// newToolService loads secrets.toml, checks the [[commands]] entries,
// starts the MCP pool and the A2A client, builds the built-in tools over the memory folder mem, with
// onRemember to run after remember saves a memory, over the indexer
// ix, which the file tools read through, with search, which search_files
// searches through, and over eng, whose fast model answers web_fetch's
// prompt, and joins them in one dispatcher that
// writes its rows to st. It then rebuilds the tool_calls table from the
// transcripts if the table is empty, so a deleted meru.db loses no history.
//
// It fails when secrets.toml can't be read or is readable by others, or
// when a command, server or agent entry is wrong; merud then refuses to
// start, so a bad entry shows at once. A command's program missing from
// PATH is only a warning in the log.
func newToolService(ctx context.Context, cfg config.Config, configPath string, st *store.Store, mem *memory.Store, ix *index.Indexer, search builtin.FileSearcher, eng builtin.Generator, onRemember func(context.Context), log *slog.Logger) (*toolService, error) {
	sec, err := secrets.Load(secrets.Path(cfg.Dir))
	if err != nil {
		return nil, err
	}
	// The commands come first: they start nothing, so a bad entry stops
	// merud before any MCP server starts.
	cmds, err := commands.New(cfg.Commands, log)
	if err != nil {
		return nil, fmt.Errorf("commands: %w", err)
	}
	// The connectors come before the pool, which reaches each one through
	// its supervisor. They outlive every pool a reload builds.
	conns, err := newConnectorSet(cfg.Dir, log)
	if err != nil {
		return nil, err
	}
	pool, err := newPool(ctx, cfg, sec, conns, log)
	if err != nil {
		conns.Close()
		return nil, err
	}
	agents, err := a2aAgents(cfg.A2A.Agents, sec.Resolve)
	if err != nil {
		pool.Close()
		conns.Close()
		return nil, err
	}
	ac, err := a2a.New(ctx, agents, log)
	if err != nil {
		pool.Close()
		conns.Close()
		return nil, fmt.Errorf("a2a: %w", err)
	}

	s := &toolService{configPath: configPath, dir: cfg.Dir, st: st, log: log, a2a: ac, secrets: sec, pool: pool,
		conns: conns, started: cfg, connected: agent.ConnectedTools(conns.routerServers(cfg))}
	outputDir, err := expandHome(cfg.Skills.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("skills.output_dir: %w", err)
	}
	// The file tools share merud's indexer, so they skip what it skips.
	// They don't reload when configure changes config.toml: a change to
	// [index] needs a restart anyway. builtin.New also lets them read the
	// output folder, which the indexer never indexes; see index.ReadAlso.
	bt := builtin.New(configPath, cfg.Builtin, cfg.Web, mem, outputDir, ix, s.reloadMCP, onRemember)
	s.bt = bt
	// web_fetch's prompt runs on the fast model, the router's.
	bt.UseModel(eng, cfg.Models.Fast)
	// web_search goes to the model only while the SearXNG connector is
	// ok, so a model never calls a search engine that isn't there.
	bt.UseWebCheck(conns.webOK)
	// search_files runs the same hybrid search a turn runs before the
	// answer, through the same store and embedding model.
	bt.UseSearch(search)
	// list_folder, grep and read_file also read the past chats, so the
	// model can answer about any earlier conversation. The indexer never
	// indexes the sessions folder; see internal/builtin/chats.go.
	bt.ReadSessions(filepath.Join(cfg.Dir, "sessions"))
	// A tool [builtin] tools lists but whose setting is missing stays off;
	// say why, so the user isn't left guessing.
	for _, off := range bt.Off() {
		log.Info("built-in tool off", "tool", off.Tool, "reason", off.Reason)
	}
	// Backend order decides which one keeps a tool name two of them offer:
	// the built-ins first, so no server can shadow configure, then the
	// commands, so an MCP server named "cmd" can't shadow one.
	s.dispatcher = dispatch.New(
		[]dispatch.Backend{bt, cmds, mcpBackend{pool: pool}, ac},
		st,
		// Attachments hands dispatch the text of a mail attachment that an
		// MCP or A2A call saved, read by read_file's rules.
		dispatch.Options{Redact: s.redact, Log: log, Attachments: bt.AttachmentText},
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
		"commands", cmds.Len(), "tools", len(s.dispatcher.Tools()))
	return s, nil
}

// newPool resolves the secrets in each [[mcp.servers]] entry, hands the
// connectors their part of cfg, and starts the MCP pool over both: the
// servers added by hand, which it connects to now, and the connectors
// that are on, which their supervisors start on the first call. The
// entries are checked before the connectors see the new config, so a bad
// entry changes nothing.
func newPool(ctx context.Context, cfg config.Config, sec *secrets.Secrets, conns *connectorSet, log *slog.Logger) (*mcp.Pool, error) {
	cfgs, err := mcpServerConfigs(cfg.MCP.Servers, sec.Resolve)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	conns.configure(cfg, sec)
	pool, err := mcp.NewPool(ctx, append(cfgs, conns.serverConfigs()...), log)
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
			Name: a.Name, URL: a.URL, Remote: a.Remote,
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

// reloadMCP is the configure tool's change hook, and OpMCPReload's work.
// It reads config.toml and secrets.toml again, starts a new MCP pool, and
// swaps it in for the old one, which it then closes. The new pool holds
// exactly the servers config lists now, so a server added, changed or
// removed takes effect, and closing the old pool stops every child it
// started, a removed server's included. A call still running on the old
// pool fails; reloads are rare, and the user asked for this one.
//
// The connectors are the exception: their supervisors outlive the pool.
// A reload hands each one the [connectors.<id>] table as it is now; one
// whose config didn't change keeps its program running, and one that
// failed gets a fresh try.
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
	pool, err := newPool(ctx, cfg, sec, s.conns, s.log)
	if err != nil {
		return err
	}

	s.mu.Lock()
	old := s.pool
	s.pool, s.secrets = pool, sec
	s.started.MCP = cfg.MCP
	s.started.Connectors = cfg.Connectors
	// The router's list takes the new [mcp] servers and connectors, and
	// keeps the rest as merud last loaded them.
	s.connected = agent.ConnectedTools(s.conns.routerServers(s.started))
	s.mu.Unlock()
	s.dispatcher.Replace(dispatch.KindMCP, mcpBackend{pool: pool})
	old.Close()
	s.log.Info("mcp servers reloaded", "servers", len(cfg.MCP.Servers), "tools", len(s.dispatcher.Tools()))
	return nil
}

// reloadA2A reads config.toml and secrets.toml again and swaps in a new A2A
// client for the [[a2a.agents]] entries, as reloadMCP does for the MCP
// servers. The desktop app calls it, through OpToolPolicy, after it
// changes an agent's allow or confirm list. It fails, leaving the old
// client in place, when config or secrets don't load or an entry is wrong.
func (s *toolService) reloadA2A(ctx context.Context) error {
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
	agents, err := a2aAgents(cfg.A2A.Agents, sec.Resolve)
	if err != nil {
		return err
	}
	ac, err := a2a.New(ctx, agents, s.log)
	if err != nil {
		return fmt.Errorf("a2a: %w", err)
	}
	s.mu.Lock()
	old := s.a2a
	s.a2a = ac
	s.started.A2A = cfg.A2A
	s.connected = agent.ConnectedTools(s.conns.routerServers(s.started))
	s.mu.Unlock()
	s.dispatcher.Replace(dispatch.KindA2A, ac)
	old.Close()
	s.log.Info("a2a agents reloaded", "agents", len(agents), "tools", len(s.dispatcher.Tools()))
	return nil
}

// reloadBuiltin reads config.toml again and hands the built-in tools their
// new [builtin] lists, and the SearXNG connector its config, so a policy
// the desktop app changed works on the next call. The router's list follows, since it names the web tools. It
// fails when config doesn't load, and the old lists stay.
func (s *toolService) reloadBuiltin() error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	s.bt.SetLists(cfg.Builtin)
	// Whether [builtin] tools lists web_search is part of the SearXNG
	// connector's config, so it hears the change too.
	s.conns.configureWeb(cfg)
	s.mu.Lock()
	s.started.Builtin = cfg.Builtin
	s.connected = agent.ConnectedTools(s.conns.routerServers(s.started))
	s.mu.Unlock()
	s.log.Info("built-in tools reloaded", "tools", len(cfg.Builtin.Tools), "confirm", len(cfg.Builtin.Confirm))
	return nil
}

// connectedTools returns what the router's prompt names as connected, as
// agent.ConnectedTools built it at startup or on the last reload. The text
// changes only then, so Ollama can reuse its work on the prompt's opening
// from one turn to the next.
func (s *toolService) connectedTools() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// Close stops the MCP servers merud started, the connectors' programs and
// the A2A client. The pool goes first, so no call asks a supervisor for a
// session while it stops.
func (s *toolService) Close() {
	s.mu.Lock()
	pool, ac := s.pool, s.a2a
	s.mu.Unlock()
	pool.Close()
	s.conns.Close()
	ac.Close()
}

// handleTools answers OpTools with one "tools" event listing every source.
func (s *toolService) handleTools(emit func(rpc.Event) error) error {
	return emit(rpc.Event{Type: rpc.EventTools, Servers: s.dispatcher.Servers()})
}

// handleMCPStatus answers OpMCPStatus with one "mcp_status" event: a row
// per configured MCP server, in config order. It reads the pool's own
// record of each server and sends nothing to any of them, so it answers at
// once while a server is down.
func (s *toolService) handleMCPStatus(emit func(rpc.Event) error) error {
	s.mu.Lock()
	pool := s.pool
	s.mu.Unlock()
	return emit(rpc.Event{Type: rpc.EventMCPStatus, MCP: mcpStatus(pool.Status())})
}

// handleReload answers OpMCPReload: it reloads the MCP servers from config
// and replies with one "tools" event, as OpTools does. On a bad config it
// returns the error, which names the problem, and the old pool stays.
//
// The reload runs to the end even if the client hangs up, so merud never
// holds a half-built pool: context.WithoutCancel keeps ctx's values but
// drops its cancellation. connectTimeout still bounds each server.
func (s *toolService) handleReload(ctx context.Context, emit func(rpc.Event) error) error {
	if err := s.reloadMCP(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return s.handleTools(emit)
}

// handleProbe answers OpMCPProbe with one "probe" event: the tools the
// server in req.Server offers, with their hints. It reads secrets.toml
// afresh, since `meru mcp add` may have saved the server's key a moment
// ago, and resolves the "secret:" values in env and headers.
//
// The probe calls no tool, and the model never sees what it finds, so it
// doesn't go through dispatch: like the memory ops, it is a command the
// user runs. The error text passes through Redact, in case a server
// echoes a key back.
func (s *toolService) handleProbe(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	if req.Server == nil {
		return errors.New("mcp_probe needs a server to probe")
	}
	sec, err := secrets.Load(secrets.Path(s.dir))
	if err != nil {
		return err
	}
	cfg, err := probeConfig(*req.Server, sec.Resolve)
	if err != nil {
		return err
	}
	info, err := mcp.Probe(ctx, cfg, s.log)
	if err != nil {
		return errors.New(sec.Redact(err.Error()))
	}
	return emit(rpc.Event{Type: rpc.EventProbe, Probe: probeResult(info)})
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
			DurationMillis: r.DurationMillis, TraceID: r.TraceID, Caller: r.Caller,
		}
	}
	return emit(rpc.Event{Type: rpc.EventLog, Log: entries})
}
