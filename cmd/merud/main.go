// Command merud is Meru's resident daemon. It loads config, keeps the models
// warm in Ollama, and answers questions from meru over a Unix socket at
// ~/.meru/merud.sock. See ARCHITECTURE.md, "The shape: daemon + thin client".
//
// Usage:
//
//	merud [-config path] [-socket path]
//
// merud logs to merud.log next to its config file and stops cleanly on
// SIGINT (Ctrl-C) or SIGTERM.
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

	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
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
func run(ctx context.Context, args []string, stderr io.Writer, buildEngine func(config.Config) (engine.Engine, error)) error {
	flags := flag.NewFlagSet("merud", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config file (default ~/.meru/config.toml)")
	socketPath := flags.String("socket", "", "Unix socket to listen on (default merud.sock next to the config file)")
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
	if *socketPath == "" {
		*socketPath = filepath.Join(cfg.Dir, "merud.sock")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", cfg.Dir, err)
	}

	log, closeLog, err := openLog(filepath.Join(cfg.Dir, "merud.log"))
	if err != nil {
		return err
	}
	defer closeLog()
	log.Info("merud starting", "config", *configPath, "profile", cfg.Profile,
		"fast", cfg.Models.Fast, "main", cfg.Models.Main, "embed", cfg.Models.Embed)

	err = serve(ctx, cfg, *socketPath, log, buildEngine)
	if err != nil {
		log.Error("merud stopped", "err", err)
		return err
	}
	log.Info("merud stopped")
	return nil
}

// serve does the work between reading config and shutting down: telemetry,
// the engine, the runtime check, claiming the socket, warming the models and
// then answering requests until ctx is cancelled.
func serve(ctx context.Context, cfg config.Config, socketPath string, log *slog.Logger, buildEngine func(config.Config) (engine.Engine, error)) error {
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

	eng, err := buildEngine(cfg)
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
	if err := warm(ctx, eng, cfg.Models, log); err != nil {
		ln.Close()
		if ctx.Err() != nil {
			return nil // stopped during warm-up
		}
		return err
	}

	a := agent.New(cfg, eng, newRouter(cfg, eng), log)
	log.Info("listening", "socket", socketPath)
	return rpc.Serve(ctx, ln, a.Handle, log)
}

// openLog opens (or creates) the log file at path for appending, with mode
// 0600, and returns a slog logger that writes key=value lines to it. The
// returned func closes the file.
func openLog(path string) (*slog.Logger, func(), error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log %s: %w", path, err)
	}
	log := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return log, func() { f.Close() }, nil
}

// errNoEngine is what newEngine returns until the Ollama engine is wired in.
var errNoEngine = errors.New("the Ollama engine isn't wired in yet")

// newEngine builds the engine merud answers with.
//
// TODO(coordinator): return engine.NewOllama(...) from agent A's
// internal/engine once it lands, built from cfg.Ollama (base URL, keep_alive)
// and cfg.Models (the embed model for Embed). Until then merud refuses to
// start with errNoEngine.
func newEngine(cfg config.Config) (engine.Engine, error) {
	return nil, errNoEngine
}

// newRouter builds the router the agent asks for each turn's route.
//
// TODO(coordinator): replace fallbackRouter with a small adapter that calls
// router.Decide(ctx, eng, routerConfig(cfg), turn) from agent A's
// internal/router and copies Route, Confidence and Outcome into an
// agent.Decision.
func newRouter(cfg config.Config, eng engine.Engine) agent.Router {
	return fallbackRouter{route: cfg.Router.Fallback}
}

// fallbackRouter stands in for the real router until it is wired in. It
// always picks the configured fallback route and reports the outcome as
// "degraded", which is what the real router does when it can't decide.
type fallbackRouter struct {
	route string
}

// Decide returns the fallback route. It never fails.
func (r fallbackRouter) Decide(ctx context.Context, question string, history []engine.Message) (agent.Decision, error) {
	return agent.Decision{Route: r.route, Confidence: 0, Outcome: "degraded"}, nil
}
