// Command merud is Meru's resident daemon. It loads config, keeps the models
// warm in Ollama, keeps the search index of your [index] folders up to date
// in ~/.meru/meru.db, and answers questions from meru over a Unix socket at
// ~/.meru/merud.sock. See ARCHITECTURE.md, "The shape: daemon + thin client".
//
// Usage:
//
//	merud [-config path] [-socket path] [-v]
//
// merud logs to merud.log next to its config file and stops cleanly on
// SIGINT (Ctrl-C) or SIGTERM. -v logs at debug level, whatever [log] level
// says: a line for each stage of each turn, tagged with the turn's trace ID.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/router"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// main wires the operating system to run: it turns SIGINT and SIGTERM into a
// cancelled context, and turns an error from run into exit status 1.
func main() {
	// signal.NotifyContext returns a context that is cancelled when one of
	// the signals arrives. Everything merud does hangs off this context, so
	// one Ctrl-C stops all of it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stderr, newEngine)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "merud: %v\n", err)
		os.Exit(1)
	}
}

// run is merud from start to finish. It returns nil after a clean shutdown
// (ctx cancelled) and an error when startup fails.
//
// buildEngine is a parameter, not a direct call, so tests can run the whole
// daemon over a fake engine.
func run(ctx context.Context, args []string, stderr io.Writer, buildEngine engineBuilder) error {
	flags := flag.NewFlagSet("merud", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file (default ~/.meru/config.toml)")
	socketPath := flags.String("socket", "", "Unix socket to listen on (default merud.sock next to the config file)")
	verbose := flags.Bool("v", false, "log at debug level, overriding [log] level")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	// A pointer from flags.String: *configPath reads the value it points to.
	if *configPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		*configPath = p
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *verbose {
		cfg.Log.Level = "debug"
	}
	if *socketPath == "" {
		*socketPath = filepath.Join(cfg.Dir, "merud.sock")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", cfg.Dir, err)
	}

	log, closeLog, err := openLog(filepath.Join(cfg.Dir, "merud.log"), cfg.Log.Level)
	if err != nil {
		return err
	}
	defer closeLog()
	otlp := cfg.Observability.OTLPEndpoint
	if otlp == "" {
		otlp = "off"
	}
	log.Info("merud starting", "config", *configPath, "profile", cfg.Profile,
		"fast", cfg.Models.Fast, "main", cfg.Models.Main, "embed", cfg.Models.Embed,
		"base_url", cfg.Ollama.BaseURL, "keep_alive", cfg.Ollama.KeepAlive,
		"history_turns", cfg.Agent.HistoryTurns, "otlp", otlp,
		"traces", cfg.Observability.Traces, "capture_content", cfg.Observability.CaptureContent,
		"log_level", cfg.Log.Level, "index_folders", len(cfg.Index.Folders))

	err = serve(ctx, cfg, *configPath, *socketPath, log, buildEngine)
	if err != nil {
		log.Error("merud stopped", "err", err)
		return err
	}
	log.Info("merud stopped")
	return nil
}

// serve does the work between reading config and shutting down:
// telemetry, the engine, claiming the socket, the wait for Ollama, warming
// the fast and embedding models, opening the store and replaying the
// transcripts into it, and then these jobs side by side until ctx is
// cancelled: loading the answer model, answering requests, the startup
// scan of the [index] folders, the file watcher, the session summarizer
// and the watch on Ollama. Questions get answers while the first scan
// runs; they search whatever the index holds so far.
//
// The socket opens before merud checks Ollama, so a client can hear why
// merud can't answer yet (see ollama.go) instead of finding no merud.
// Until merud is ready, requests go through a gate that waits, or answers
// from the watch while Ollama fails; nothing that needs the engine starts
// before Ollama is ready.
func serve(ctx context.Context, cfg config.Config, configPath, socketPath string, log *slog.Logger, buildEngine engineBuilder) error {
	shutdownObs, err := obs.Setup(ctx, cfg.Observability)
	if err != nil {
		return fmt.Errorf("observability: %w", err)
	}
	defer func() {
		// Give the exporter a few seconds to send its last batch. ctx is
		// already cancelled here, so start from a fresh one.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownObs(sctx); err != nil {
			log.Warn("observability shutdown", "err", err)
		}
	}()

	eng, err := buildEngine(cfg, log)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	ollama, err := newOllamaWatch(cfg, eng, log)
	if err != nil {
		return err
	}

	// Claim the socket first: a second merud then fails at once, and a
	// client can ask what merud is waiting for.
	ln, err := rpc.Listen(ctx, socketPath)
	if err != nil {
		return err
	}
	log.Info("listening", "socket", socketPath)

	// An errgroup runs each function in its own goroutine and Wait waits
	// for all of them. gctx is cancelled when ctx is, when stop is
	// called, or when one of them returns an error, so a failed server
	// stops the other jobs too. The scan, the summarizer, the watch on
	// Ollama and the two watchers, of the [index] folders and of the
	// memory folder, log their own errors and return nil.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	g, gctx := errgroup.WithContext(ctx)
	requests := newGate(ollama)
	g.Go(func() error { return rpc.Serve(gctx, ln, requests.handle, log) })
	// fail stops the server and returns err, for a startup step that
	// fails once the server runs.
	fail := func(err error) error {
		stop()
		_ = g.Wait()
		return err
	}

	// Wait for Ollama: it must answer, be new enough, and load the fast
	// and embedding models. Until the gate opens, a request waits, or
	// hears from the watch while Ollama fails.
	if !ollama.waitReady(gctx) {
		return g.Wait() // stopped while waiting
	}
	g.Go(func() error { ollama.watch(gctx); return nil })
	warnMissingSetModels(gctx, eng, cfg.Models.Sets, log)
	st, err := openStore(gctx, cfg, eng, log)
	if err != nil {
		if gctx.Err() != nil {
			return g.Wait()
		}
		return fail(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("close store", "err", err)
		}
	}()
	sessionsDir := filepath.Join(cfg.Dir, "sessions")
	replayTurns(gctx, st, sessionsDir, log)
	replaySessions(gctx, st, sessionsDir, log)
	ix, err := index.New(cfg.Index, st, eng, log)
	if err != nil {
		return fail(fmt.Errorf("index: %w", err))
	}

	// merud owns the memory folder: the agent reads the profile from it,
	// remember writes to it, and the memory ops answer `meru memory`. Its
	// syncer copies the files into the store, where recall searches them.
	mem, err := memory.Open(filepath.Join(cfg.Dir, "memory"))
	if err != nil {
		return fail(err)
	}
	mems := memoryService{mem: mem, sync: index.NewMemories(mem, st, eng, log), log: log}
	// merud owns the skills folder too: the agent lists and loads skills
	// from it each turn, and the skill ops answer `meru skills`.
	sk, err := newSkillService(filepath.Join(cfg.Dir, "skills"), cfg.Skills.Disabled, log)
	if err != nil {
		return fail(err)
	}
	// The file tools read through the indexer. With no [index] folders
	// they have nothing to read, so merud leaves them out and the model
	// never sees them, until the desktop app adds a folder.
	search := searchAdapter{st: st, eng: eng}
	tools, err := newToolService(gctx, cfg, configPath, st, mem, ix, search, eng, mems.syncNow, log)
	if err != nil {
		return fail(err)
	}
	defer tools.Close()
	idx := newIndexService(ix, st, mems, cfg.Index, configPath, tools.bt.EditConfig, log)
	// The router comes after the tools, because its prompt names what the
	// tool service connects.
	rt, err := newRouter(cfg, eng, tools.connectedTools, idx.currentFolders, log)
	if err != nil {
		return fail(err)
	}
	turns := turnRecorder{st: st, sessionsDir: sessionsDir, log: log}
	a := agent.New(cfg, eng, rt, search, tools.dispatcher, turns, profileAdapter{mem: mem, st: st, eng: eng}, log)
	a.UseSkills(sk)
	a.UseFolders(idx.currentFolders)
	// A question's images come from the uploads folder through the
	// built-in tools' Image, which refuses any other path.
	a.UseImages(tools.bt.Image, capabilityCheck(eng, engine.Vision))
	// An answer model that can't call tools, such as gemma3:12b, answers
	// with none; see internal/agent/notools.go.
	a.UseToolCheck(capabilityCheck(eng, engine.ToolUse))
	machine := machineLine(gctx)
	a.UseMachine(machine)
	log.Info("machine", "line", machine)
	sum, err := newSummarizer(cfg, st, eng, sessionsDir, log)
	if err != nil {
		return fail(err)
	}
	// The session ops show source paths as ~/... like a live turn does, and
	// so does about_meru. A home folder merud can't find leaves the paths
	// whole.
	home, _ := os.UserHomeDir()
	hist := newHistoryService(sessionsDir, home, st, a.ForgetIncognito, log)
	// about_meru reads every source above, so merud hands it them last. It
	// reads them on each call, so it reports the setup as it is then.
	tools.bt.UseAbout(aboutService{
		cfg: cfg, main: a.Main, eng: eng, st: st, folders: idx.currentFolders, servers: tools.dispatcher.Servers,
		skills: sk, mem: mem, machine: machine, home: home, web: tools.conns.webOK, log: log,
	}.facts)
	// config.Load has checked output_dir, so expandHome fails only when
	// the OS can't say where home is; then saves have nowhere to go.
	outputDir, _ := expandHome(cfg.Skills.OutputDir)
	svc := services{
		agent: a, idx: idx, tools: tools, mems: mems, skills: sk, hist: hist, st: st, configPath: configPath,
		ollama: ollama,
		save:   saveService{dispatcher: tools.dispatcher, sessionsDir: sessionsDir, outputDir: outputDir, home: home, now: time.Now},
		models: newModelService(cfg, configPath, outputDir, eng, a, tools.bt.EditConfig, log),
	}

	// The answer model loads beside the server, so ping, settings and the
	// router answer at once. StartWarm runs before the full handler takes
	// over, so a question that comes during the load waits for it instead
	// of starting another. newModelService has already told the agent
	// which model answers and whether it thinks.
	warmAnswer := a.StartWarm()
	g.Go(func() error { warmAnswer(gctx); return nil })
	requests.open(handler(svc))
	log.Info("ready")
	g.Go(func() error { idx.startupScan(gctx); return nil })
	g.Go(func() error { idx.watchAndRescan(gctx); return nil })
	g.Go(func() error { sum.Run(gctx); return nil })
	g.Go(func() error { mems.watch(gctx); return nil })
	return g.Wait()
}

// openStore opens the search index at meru.db in the Meru home. It asks the
// embedding model for one vector to learn the vector size first. When the
// model or size changed since the last run, the store drops the old
// vectors and the startup scan embeds every file again.
func openStore(ctx context.Context, cfg config.Config, eng engine.Engine, log *slog.Logger) (*store.Store, error) {
	dims, err := embedDims(ctx, eng)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.Models.Embed, err)
	}
	path := filepath.Join(cfg.Dir, "meru.db")
	st, err := store.Open(ctx, store.Options{Path: path, EmbedModel: cfg.Models.Embed, Dims: dims})
	if err != nil {
		return nil, err
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	log.Info("store open", "path", path, "embed", cfg.Models.Embed, "dims", dims,
		"documents", stats.Documents, "chunks", stats.Chunks, "vectors", stats.Vectors)
	return st, nil
}

// replayTurns rebuilds the turns table from the transcripts under
// sessionsDir when the table is empty, as after meru.db was deleted or
// came from a Meru without it. A failed replay costs `meru usage` some
// history, not the answer to any question, so it only logs a warning.
func replayTurns(ctx context.Context, st *store.Store, sessionsDir string, log *slog.Logger) {
	n, err := st.ReplayTurns(ctx, sessionsDir)
	if err != nil {
		log.Warn("rebuild turns from transcripts", "err", err)
		return
	}
	if n > 0 {
		log.Info("turns rebuilt from transcripts", "turns", n)
	}
}

// handleUsage answers OpUsage with one "usage" event: the turns table added
// up over each usage window, with today, week and month in merud's local
// time, or, when req.Kind is rpc.UsageByModel, added up per answer model.
func handleUsage(ctx context.Context, st *store.Store, req rpc.Request, emit func(rpc.Event) error) error {
	var windows []rpc.UsageWindow
	var err error
	if req.Kind == rpc.UsageByModel {
		windows, err = st.UsageByModel(ctx)
	} else {
		windows, err = st.Usage(ctx, time.Now())
	}
	if err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventUsage, Usage: windows})
}

// services holds everything that answers a request, so handler takes one
// value instead of a long list.
type services struct {
	agent      *agent.Agent
	idx        *indexService
	tools      *toolService
	mems       memoryService
	skills     *skillService
	hist       historyService
	st         *store.Store
	save       saveService
	models     *modelService
	ollama     *ollamaWatch
	configPath string
}

// handler returns the rpc.Handler merud serves: questions go to the agent,
// the index and folder ops to the index service, the tools, log, MCP and
// connection ops to the tool service, the memory ops to the memory
// service, the skill ops to the skill service, the session ops and the
// chat folder ops to the history service, save_file to the save service,
// attach_file to the tool service, which holds the built-in tools, the
// model ops to the model service, and the usage op to the store. The rpc
// server answers pings itself.
func handler(svc services) rpc.Handler {
	a, idx, tools, mems, sk, hist, st := svc.agent, svc.idx, svc.tools, svc.mems, svc.skills, svc.hist, svc.st
	return func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
		switch req.Op {
		case rpc.OpAsk:
			return a.Handle(ctx, req, emit, approve)
		case rpc.OpIndex:
			return idx.handleIndex(ctx, req, emit)
		case rpc.OpIndexStatus:
			return idx.handleStatus(ctx, emit)
		case rpc.OpTools:
			return tools.handleTools(emit)
		case rpc.OpLog:
			return tools.handleLog(ctx, req.Limit, emit)
		case rpc.OpMCPProbe:
			return tools.handleProbe(ctx, req, emit)
		case rpc.OpMCPReload:
			return tools.handleReload(ctx, emit)
		case rpc.OpMCPStatus:
			return tools.handleMCPStatus(emit)
		case rpc.OpConnectors:
			return tools.handleConnectors(svc.ollama, emit)
		case rpc.OpUsage:
			return handleUsage(ctx, st, req, emit)
		case rpc.OpMemoryList:
			return mems.handleList(emit)
		case rpc.OpMemoryAdd:
			return mems.handleAdd(ctx, req, emit)
		case rpc.OpMemoryForget:
			return mems.handleForget(ctx, req)
		case rpc.OpSkills:
			return sk.handleList(ctx, emit)
		case rpc.OpSkillShow:
			return sk.handleShow(ctx, req, emit)
		case rpc.OpSkillReset:
			return sk.handleReset(ctx, req)
		case rpc.OpSessions:
			return hist.handleSessions(req.Limit, emit)
		case rpc.OpSessionTurns:
			return hist.handleTurns(req.Session, emit)
		case rpc.OpSessionDelete:
			return hist.handleDelete(ctx, req.Session)
		case rpc.OpSessionMove:
			return hist.handleMove(ctx, req, emit)
		case rpc.OpSessionTag:
			return hist.handleTag(ctx, req, emit)
		case rpc.OpChatFolders:
			return hist.handleChatFolders(emit)
		case rpc.OpChatFolderAdd:
			return hist.handleChatFolderAdd(req, emit)
		case rpc.OpChatFolderRename:
			return hist.handleChatFolderRename(ctx, req, emit)
		case rpc.OpChatFolderRemove:
			return hist.handleChatFolderRemove(ctx, req, emit)
		case rpc.OpConnections:
			return tools.handleConnections(emit)
		case rpc.OpToolPolicy:
			return tools.handleToolPolicy(ctx, req, emit)
		case rpc.OpMCPAdd:
			return tools.handleMCPAdd(ctx, req, emit)
		case rpc.OpMCPRemove:
			return tools.handleMCPRemove(ctx, req, emit)
		case rpc.OpSecretSet:
			return tools.handleSecretSet(ctx, req)
		case rpc.OpFolders:
			return idx.handleFolders(ctx, emit)
		case rpc.OpFolderAdd:
			return idx.handleFolderAdd(ctx, req, emit)
		case rpc.OpFolderRemove:
			return idx.handleFolderRemove(ctx, req, emit)
		case rpc.OpSaveFile:
			return svc.save.handleSave(ctx, req, emit, approve)
		case rpc.OpAttachFile:
			return tools.handleAttach(ctx, req, emit)
		case rpc.OpSkillEnable, rpc.OpSkillDisable:
			return sk.handleSetDisabled(ctx, req, req.Op == rpc.OpSkillDisable, svc.configPath, tools.bt.EditConfig, emit)
		case rpc.OpModels:
			return svc.models.handleModels(ctx, emit)
		case rpc.OpModelSet:
			return svc.models.handleModelSet(ctx, req, emit)
		case rpc.OpModelUse:
			return svc.models.handleModelUse(ctx, req, emit)
		case rpc.OpModelSave:
			return svc.models.handleModelSave(ctx, emit)
		default:
			return fmt.Errorf("unknown op %q", req.Op)
		}
	}
}

// openLog opens (or creates) the log file at path for appending, with mode
// 0600, and returns a slog logger that writes key=value lines at level and
// above. level is a [log] level name that config has already checked. Lines
// logged with a context that holds a span gain its trace_id (see
// obs.LogHandler). The returned func closes the file.
func openLog(path, level string) (*slog.Logger, func(), error) {
	lv, ok := config.LogLevel(level)
	if !ok {
		return nil, nil, fmt.Errorf("log level %q is unknown", level)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) // #nosec G304 -- merud.log inside the Meru home, not user input
	if err != nil {
		return nil, nil, fmt.Errorf("open log %s: %w", path, err)
	}
	text := slog.NewTextHandler(f, &slog.HandlerOptions{Level: lv})
	return slog.New(obs.LogHandler(text)), func() { _ = f.Close() }, nil
}

// engineBuilder is the type of newEngine: it builds the engine from config,
// giving it merud's logger. Naming the type keeps run's signature short.
type engineBuilder func(config.Config, *slog.Logger) (engine.Engine, error)

// newEngine builds the engine merud answers with: an OllamaEngine on the
// loopback address from config, which also knows the embed model for Embed.
// A nil *http.Client makes the engine use its own default client. The
// engine asks Ollama for [ollama] context_length tokens of room on every
// chat call.
func newEngine(cfg config.Config, log *slog.Logger) (engine.Engine, error) {
	e, err := engine.NewOllama(cfg.Ollama.BaseURL, cfg.Ollama.KeepAlive, cfg.Models.Embed, nil, log)
	if err != nil {
		return nil, err
	}
	e.ContextLength = cfg.Ollama.ContextLength
	return e, nil
}

// newRouter builds the router the agent asks for each turn's route: the
// one-token classifier in internal/router, running on the fast model and
// writing its debug lines to log. Each turn, the router's prompt names what
// tools returns as connected, and the folders folders returns.
func newRouter(cfg config.Config, eng engine.Engine, tools, folders func() []string, log *slog.Logger) (agent.Router, error) {
	rc, err := router.ConfigFrom(cfg.Router, cfg.Models.Fast)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	rc.Log = log
	return routerAdapter{eng: eng, cfg: rc, folders: folders, tools: tools}, nil
}

// routerAdapter lets router.Decide serve as an agent.Router. The two packages
// don't import each other, so this small type in main joins them.
type routerAdapter struct {
	eng engine.Engine
	cfg router.Config
	// folders returns the [index] folders, which the router's prompt
	// names. It is a function because the desktop app can add or remove a
	// folder while merud runs.
	folders func() []string
	// tools returns what is connected, for the prompt to name. It is a
	// function because an MCP reload changes the list while merud runs.
	tools func() []string
}

// Decide asks the router for this turn's route and copies the result into the
// agent's own Decision type. router.Decide records the meru.route span and
// metric itself.
func (r routerAdapter) Decide(ctx context.Context, question string, history []engine.Message) (agent.Decision, error) {
	d, err := router.Decide(ctx, r.eng, r.cfg, router.Turn{History: history, Question: question, Folders: r.folders(), Tools: r.tools()})
	if err != nil {
		return agent.Decision{}, err
	}
	return agent.Decision{Route: string(d.Route), Confidence: d.Confidence, Outcome: string(d.Outcome)}, nil
}
