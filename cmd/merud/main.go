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

// serve does the work between reading config and shutting down: telemetry,
// the engine, the runtime check, claiming the socket, warming the models,
// opening the store, and then three jobs side by side until ctx is
// cancelled: answering requests, the startup scan of the [index] folders,
// and the file watcher. Questions get answers while the first scan runs;
// they search whatever the index holds so far.
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
	version, err := checkRuntime(ctx, eng)
	if err != nil {
		return err
	}
	log.Info("ollama ok", "version", version)

	// Claim the socket before warming, which can take minutes on a cold
	// start: a second merud then fails at once instead of loading models for
	// nothing. Clients that connect meanwhile wait in the socket's queue
	// until Serve starts accepting.
	ln, err := rpc.Listen(ctx, socketPath)
	if err != nil {
		return err
	}
	// served turns true once rpc.Serve owns the listener; until then, any
	// return below must close it. This deferred function reads served when
	// serve returns, not now.
	served := false
	defer func() {
		if !served {
			_ = ln.Close()
		}
	}()

	if err := warm(ctx, eng, cfg.Models, log); err != nil {
		if ctx.Err() != nil {
			return nil // stopped during warm-up
		}
		return err
	}
	st, err := openStore(ctx, cfg, eng, log)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("close store", "err", err)
		}
	}()
	ix, err := index.New(cfg.Index, st, eng, log)
	if err != nil {
		return fmt.Errorf("index: %w", err)
	}

	rt, err := newRouter(cfg, eng, log)
	if err != nil {
		return err
	}
	a := agent.New(cfg, eng, rt, searchAdapter{st: st, eng: eng}, log)
	idx := newIndexService(ix, st, cfg.Index.Folders, configPath, log)
	log.Info("listening", "socket", socketPath)

	// An errgroup runs each function in its own goroutine and Wait waits
	// for all of them. gctx is cancelled when ctx is, or when one of them
	// returns an error, so a failed server stops the scan and the watcher
	// too. The scan and the watcher log their own errors and return nil.
	served = true
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return rpc.Serve(gctx, ln, handler(a, idx), log) })
	g.Go(func() error { idx.startupScan(gctx); return nil })
	g.Go(func() error { idx.watch(gctx); return nil })
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

// handler returns the rpc.Handler merud serves: questions go to the agent,
// the index ops to the index service. The rpc server answers pings itself.
func handler(a *agent.Agent, idx *indexService) rpc.Handler {
	return func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
		switch req.Op {
		case rpc.OpAsk:
			return a.Handle(ctx, req, emit)
		case rpc.OpIndex:
			return idx.handleIndex(ctx, req, emit)
		case rpc.OpIndexStatus:
			return idx.handleStatus(ctx, emit)
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
// A nil *http.Client makes the engine use its own default client.
func newEngine(cfg config.Config, log *slog.Logger) (engine.Engine, error) {
	return engine.NewOllama(cfg.Ollama.BaseURL, cfg.Ollama.KeepAlive, cfg.Models.Embed, nil, log)
}

// newRouter builds the router the agent asks for each turn's route: the
// one-token classifier in internal/router, running on the fast model and
// writing its debug lines to log.
func newRouter(cfg config.Config, eng engine.Engine, log *slog.Logger) (agent.Router, error) {
	rc, err := router.ConfigFrom(cfg.Router, cfg.Models.Fast)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	rc.Log = log
	return routerAdapter{eng: eng, cfg: rc}, nil
}

// routerAdapter lets router.Decide serve as an agent.Router. The two packages
// don't import each other, so this small type in main joins them.
type routerAdapter struct {
	eng engine.Engine
	cfg router.Config
}

// Decide asks the router for this turn's route and copies the result into the
// agent's own Decision type. router.Decide records the meru.route span and
// metric itself.
func (r routerAdapter) Decide(ctx context.Context, question string, history []engine.Message) (agent.Decision, error) {
	d, err := router.Decide(ctx, r.eng, r.cfg, router.Turn{History: history, Question: question})
	if err != nil {
		return agent.Decision{}, err
	}
	return agent.Decision{Route: string(d.Route), Confidence: d.Confidence, Outcome: string(d.Outcome)}, nil
}
