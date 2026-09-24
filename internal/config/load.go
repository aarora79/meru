// This file loads config.toml: it fills in defaults, reads the file over them,
// fills empty model names from the chosen profile, and then checks every value.
// Load is the only way the rest of Meru gets a Config.

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/loopback"

	"github.com/BurntSushi/toml"
)

// routes lists the four routes the router can pick, in the router's letter
// order (A to D). The fallback must be one of them. See docs/fast-router.md.
var routes = []string{"direct", "search", "tools", "search+tools"}

// profiles maps each profile name to the models it fills the tiers with. See
// ARCHITECTURE.md, "Model tiers". A map literal like this one builds the map
// in one expression; the key is the profile name.
var profiles = map[string]Models{
	"lite": {
		Fast:  "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M",
		Main:  "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M",
		Embed: "nomic-embed-text",
	},
	"full": {
		Fast:  "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M",
		Main:  "qwen3.8:27b",
		Embed: "qwen3-embedding:0.6b",
	},
}

// DefaultDir returns the Meru home directory, ~/.meru. It fails only when the
// operating system can't say where the user's home directory is.
func DefaultDir() (string, error) {
	// Go functions can return more than one value. By convention the last one
	// is an error, and the caller checks it before using the others.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".meru"), nil
}

// DefaultPath returns the default config file path, ~/.meru/config.toml.
func DefaultPath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// defaults returns a Config holding every default except the model names,
// which depend on the profile and so get filled in after the file is read.
func defaults() Config {
	return Config{
		Profile: "lite",
		Ollama: Ollama{
			BaseURL:   "http://127.0.0.1:11434",
			KeepAlive: "-1",
		},
		Agent: Agent{
			MaxRounds:    8,
			HistoryTurns: 10,
		},
		Router: Router{
			TopLogProbs:   20,
			Temperature:   1.25,
			MinConfidence: 0.45,
			Fallback:      "search+tools",
		},
		Observability: Observability{
			MetricsInterval: "10s",
			Traces:          true,
		},
		Log: Log{Level: "info"},
		// Folders and Ignore start as empty lists rather than nil, so the
		// defaults compare equal to a file that says `folders = []`.
		Index: Index{
			Folders:       []string{},
			Ignore:        []string{},
			MaxFileMB:     5,
			ChunkTokens:   500,
			OverlapTokens: 50,
			Watch:         true,
		},
	}
}

// Load reads the config file at path and returns the checked result. A missing
// file isn't an error: Load returns the defaults for the lite profile.
//
// Config.Dir becomes the directory that holds the file, so pointing -config at
// another directory moves the whole Meru home (sessions, socket, log) there.
//
// Load fails when the file can't be read or parsed, when it holds a key Meru
// doesn't know (usually a typo), or when any value is out of range.
func Load(path string) (Config, error) {
	cfg := defaults()

	// The TOML decoder only sets the keys the file contains, so decoding over
	// the defaults leaves every missing key at its default. That also lets a
	// user set a value to zero on purpose, such as min_confidence = 0.
	md, err := toml.DecodeFile(path, &cfg)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// No file yet: keep the defaults.
	case err != nil:
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	default:
		if unknown := md.Undecoded(); len(unknown) > 0 {
			keys := make([]string, len(unknown))
			for i, k := range unknown {
				keys[i] = k.String()
			}
			slices.Sort(keys)
			return Config{}, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("config path %s: %w", path, err)
	}
	cfg.Dir = filepath.Dir(abs)

	// Fill empty model names from the profile. An unknown profile is caught
	// by validate below; the lookup just returns empty names for it.
	p := profiles[cfg.Profile]
	if cfg.Models.Fast == "" {
		cfg.Models.Fast = p.Fast
	}
	if cfg.Models.Main == "" {
		cfg.Models.Main = p.Main
	}
	if cfg.Models.Embed == "" {
		cfg.Models.Embed = p.Embed
	}

	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// validate checks every value in cfg and returns all the problems it finds,
// joined into one error, so the user can fix them in one pass.
func validate(cfg Config) error {
	var errs []error
	// add records one problem. A function literal like this one is a closure:
	// it can read and change errs from the surrounding function.
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if _, ok := profiles[cfg.Profile]; !ok {
		add("profile %q is unknown; use \"lite\" or \"full\"", cfg.Profile)
	}

	if err := loopback.CheckURL(cfg.Ollama.BaseURL); err != nil {
		add("ollama.base_url: %w", err)
	}
	if err := checkKeepAlive(cfg.Ollama.KeepAlive); err != nil {
		add("ollama.keep_alive: %w", err)
	}

	if cfg.Agent.MaxRounds < 1 {
		add("agent.max_rounds is %d; it must be 1 or more", cfg.Agent.MaxRounds)
	}
	if cfg.Agent.HistoryTurns < 0 {
		add("agent.history_turns is %d; it must be 0 or more", cfg.Agent.HistoryTurns)
	}

	r := cfg.Router
	if r.TopLogProbs < 1 || r.TopLogProbs > 20 {
		add("router.top_logprobs is %d; it must be between 1 and 20", r.TopLogProbs)
	}
	if !(r.Temperature > 0) || math.IsInf(r.Temperature, 0) {
		add("router.temperature is %v; it must be above 0", r.Temperature)
	}
	if !(r.MinConfidence >= 0 && r.MinConfidence <= 1) {
		add("router.min_confidence is %v; it must be between 0 and 1", r.MinConfidence)
	}
	if !slices.Contains(routes, r.Fallback) {
		add("router.fallback %q is unknown; use one of %s", r.Fallback, strings.Join(routes, ", "))
	}

	o := cfg.Observability
	if o.OTLPEndpoint != "" {
		if err := loopback.CheckURL(o.OTLPEndpoint); err != nil {
			add("observability.otlp_endpoint: %w", err)
		}
	}
	if d, err := time.ParseDuration(o.MetricsInterval); err != nil || d <= 0 {
		add("observability.metrics_interval %q must be a positive duration such as \"10s\"", o.MetricsInterval)
	}

	if _, ok := LogLevel(cfg.Log.Level); !ok {
		add("log.level %q is unknown; use \"debug\", \"info\", \"warn\" or \"error\"", cfg.Log.Level)
	}

	for _, err := range checkIndex(cfg.Index) {
		add("%w", err)
	}

	// errors.Join returns nil when errs is empty.
	return errors.Join(errs...)
}

// checkKeepAlive accepts the forms Ollama understands: a whole number of
// seconds ("-1" means forever, "0" unloads at once) or a Go duration such as
// "30m".
func checkKeepAlive(s string) error {
	if _, err := strconv.Atoi(s); err == nil {
		return nil
	}
	if _, err := time.ParseDuration(s); err == nil {
		return nil
	}
	return fmt.Errorf("%q must be a number of seconds (\"-1\" keeps models loaded) or a duration such as \"30m\"", s)
}

// checkIndex checks the [index] section and returns one error per problem.
// It checks the shape of each folder, not whether it exists: a folder on an
// unplugged drive shouldn't stop merud from starting. The indexer checks the
// ignore patterns themselves, because it owns the pattern syntax.
func checkIndex(ix Index) []error {
	var errs []error
	for _, f := range ix.Folders {
		// A folder must name one place, whatever directory merud starts in:
		// an absolute path, or one under the home directory.
		home := f == "~" || strings.HasPrefix(f, "~/") || strings.HasPrefix(f, `~\`)
		if !home && !filepath.IsAbs(f) {
			errs = append(errs, fmt.Errorf("index.folders: %q must be an absolute path or start with \"~/\"", f))
		}
	}
	for _, p := range ix.Ignore {
		if strings.TrimSpace(p) == "" {
			errs = append(errs, errors.New("index.ignore: a pattern can't be empty"))
		}
	}
	// 1 GB is far past any note or source file; the cap catches a typo that
	// would have merud read whole disk images.
	if ix.MaxFileMB < 1 || ix.MaxFileMB > 1024 {
		errs = append(errs, fmt.Errorf("index.max_file_mb is %d; it must be between 1 and 1024", ix.MaxFileMB))
	}
	// Below 50 tokens a chunk holds a sentence or two, too little to answer
	// from. Above 8192 it outgrows what small embedding models read.
	if ix.ChunkTokens < 50 || ix.ChunkTokens > 8192 {
		errs = append(errs, fmt.Errorf("index.chunk_tokens is %d; it must be between 50 and 8192", ix.ChunkTokens))
	}
	// Overlap past half a chunk would make each chunk mostly a copy of the
	// one before it.
	if ix.OverlapTokens < 0 || ix.OverlapTokens > ix.ChunkTokens/2 {
		errs = append(errs, fmt.Errorf("index.overlap_tokens is %d; it must be between 0 and half of chunk_tokens (%d)",
			ix.OverlapTokens, ix.ChunkTokens/2))
	}
	return errs
}

// LogLevel turns a [log] level name into the slog level merud logs at. It
// returns false for a name it doesn't know, so validate can refuse it.
func LogLevel(name string) (slog.Level, bool) {
	switch name {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}
