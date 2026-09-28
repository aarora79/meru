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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/loopback"

	"github.com/BurntSushi/toml"
)

// MaxWebResults caps [web] max_results, and the max_results a web_search
// call may ask for.
const MaxWebResults = 20

// builtinTools names every built-in tool, in the order the template lists
// them: the default for [builtin] tools. The names match the constants in
// internal/builtin, which imports this package; a test there checks that
// each name here is a tool it serves.
var builtinTools = []string{
	"configure", "datetime", "about_meru", "remember", "write_file",
	"read_file", "list_folder", "grep", "search_files", "web_search", "web_fetch",
}

// BuiltinTools returns the names of all eleven built-in tools, the default
// for [builtin] tools. It returns a copy, so a caller can't change the
// list the defaults use.
func BuiltinTools() []string { return slices.Clone(builtinTools) }

// movedFetch is the message for the old [web] fetch key and for
// read_pages, the name before it.
const movedFetch = "web.fetch moved: list web_fetch in [builtin] tools, or remove it to turn page fetching off"

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
		Main:  "qwen3.6:35b-a3b-mxfp8",
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
			BaseURL:       "http://127.0.0.1:11434",
			KeepAlive:     "-1",
			ContextLength: 32768,
		},
		Agent: Agent{
			MaxRounds:       8,
			MaxOutputTokens: 8192,
			TurnTimeout:     "5m",
			HistoryTurns:    10,
			SummaryIdle:     "30m",
		},
		Router: Router{
			TopLogProbs:     20,
			Temperature:     1.25,
			MinConfidence:   0.45,
			Fallback:        "search+tools",
			Decision:        "top",
			SearchThreshold: 0.30,
			ToolsThreshold:  0.25,
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
			Retrieval:     RetrievalAuto,
		},
		// Every built-in tool is on. web_fetch, which fetches public pages
		// off this machine, is on too: a small model needs the page itself
		// to answer "what's the latest release?" right, and the URL guard
		// in internal/builtin asks before any fetch the model could use to
		// leak data. write_file asks before each call: a file it writes
		// stays on your disk after the chat ends (ARCHITECTURE.md,
		// "Approving a tool call").
		Builtin: Builtin{Tools: BuiltinTools(), Confirm: []string{"write_file"}},
		Skills:  Skills{OutputDir: "~/meru-output", Disabled: []string{}},
		// web_search runs through a SearXNG the user starts on this port.
		Web:  Web{SearXNGURL: "http://127.0.0.1:8888", MaxResults: 8},
		Chat: Chat{MouseCopy: true},
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
				if renamedNetwork(k) {
					return Config{}, fmt.Errorf("config %s: network was renamed remote: write remote = true in %s "+
						"to let merud connect to a URL on another machine", path, k[0]+"."+k[1])
				}
				if s := k.String(); s == "web.fetch" || s == "web.read_pages" {
					return Config{}, fmt.Errorf("config %s: %s", path, movedFetch)
				}
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
	// Below 2048 tokens not even the system prompt fits.
	if n := cfg.Ollama.ContextLength; n != 0 && n < 2048 {
		add("ollama.context_length is %d; it must be 0, for Ollama's own setting, or 2048 or more", n)
	}

	if cfg.Agent.MaxRounds < 1 {
		add("agent.max_rounds is %d; it must be 1 or more", cfg.Agent.MaxRounds)
	}
	if cfg.Agent.MaxOutputTokens < 1 {
		add("agent.max_output_tokens is %d; it must be 1 or more", cfg.Agent.MaxOutputTokens)
	}
	if d, err := time.ParseDuration(cfg.Agent.TurnTimeout); err != nil || d <= 0 {
		add("agent.turn_timeout %q must be a positive duration such as \"5m\"", cfg.Agent.TurnTimeout)
	}
	if d, err := time.ParseDuration(cfg.Agent.SummaryIdle); err != nil || d <= 0 {
		add("agent.summary_idle %q must be a positive duration such as \"30m\"", cfg.Agent.SummaryIdle)
	}
	if o := cfg.Skills.OutputDir; o == "" {
		add("skills.output_dir is empty; set a folder such as \"~/meru-output\"")
	} else if home := o == "~" || strings.HasPrefix(o, "~/") || strings.HasPrefix(o, `~\`); !home && !filepath.IsAbs(o) {
		// Like an [index] folder, the output folder must name one place,
		// whatever directory merud starts in.
		add("skills.output_dir %q must be an absolute path or start with \"~/\"", o)
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
	if r.Decision != "top" && r.Decision != "marginal" {
		add("router.decision %q is unknown; use \"top\" or \"marginal\"", r.Decision)
	}
	if !(r.SearchThreshold >= 0 && r.SearchThreshold <= 1) {
		add("router.search_threshold is %v; it must be between 0 and 1", r.SearchThreshold)
	}
	if !(r.ToolsThreshold >= 0 && r.ToolsThreshold <= 1) {
		add("router.tools_threshold is %v; it must be between 0 and 1", r.ToolsThreshold)
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

	if u := cfg.Web.SearXNGURL; u != "" {
		if err := loopback.CheckURL(u); err != nil {
			add("web.searxng_url: %w; SearXNG must run on this machine, or set searxng_url = \"\" to turn web search off", err)
		}
	}
	// 20 results already fill a few thousand tokens of the prompt.
	if n := cfg.Web.MaxResults; n < 1 || n > MaxWebResults {
		add("web.max_results is %d; it must be between 1 and %d", n, MaxWebResults)
	}

	for _, err := range checkSets(cfg.Models.Sets) {
		add("%w", err)
	}
	for _, err := range checkIndex(cfg.Index) {
		add("%w", err)
	}
	for _, err := range checkBuiltin(cfg.Builtin) {
		add("%w", err)
	}
	// Agentic retrieval runs no search before the answer, so without
	// search_files the model could only grep: it would lose search by
	// meaning altogether.
	if cfg.Index.Retrieval == RetrievalAgentic && !slices.Contains(cfg.Builtin.Tools, "search_files") {
		add("index.retrieval is \"agentic\", but builtin.tools doesn't list search_files; add it, or use retrieval = \"auto\"")
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

// setName is what a [[models.sets]] name may hold. The name goes on a
// command line and in the chat's header, so it keeps to characters that
// need no quotes.
var setName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// checkSets checks the [[models.sets]] entries and returns one error per
// problem: a missing or odd name, a name used twice, and a set that names
// no model. It doesn't ask Ollama whether each model is there; merud warns
// about that at startup, since a model can be pulled later.
func checkSets(sets []ModelSet) []error {
	var errs []error
	var seen []string
	for i, s := range sets {
		switch {
		case s.Name == "":
			errs = append(errs, fmt.Errorf("models.sets: entry %d has no name", i+1))
		case !setName.MatchString(s.Name):
			errs = append(errs, fmt.Errorf("models.sets: name %q may hold only letters, digits, \".\", \"-\" and \"_\"", s.Name))
		case slices.Contains(seen, s.Name):
			errs = append(errs, fmt.Errorf("models.sets: the name %q is used twice", s.Name))
		}
		seen = append(seen, s.Name)
		if s.Main == "" && s.Fast == "" && s.Embed == "" {
			errs = append(errs, fmt.Errorf("models.sets: set %q names no model; give it main, fast or embed", s.Name))
		}
	}
	return errs
}

// FindSet returns the set called name from sets, and whether there is one.
func FindSet(sets []ModelSet, name string) (ModelSet, bool) {
	i := slices.IndexFunc(sets, func(s ModelSet) bool { return s.Name == name })
	if i < 0 {
		return ModelSet{}, false
	}
	return sets[i], true
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
	if ix.Retrieval != RetrievalAuto && ix.Retrieval != RetrievalAgentic {
		errs = append(errs, fmt.Errorf("index.retrieval %q is unknown; use %q or %q", ix.Retrieval, RetrievalAuto, RetrievalAgentic))
	}
	// Overlap past half a chunk would make each chunk mostly a copy of the
	// one before it.
	if ix.OverlapTokens < 0 || ix.OverlapTokens > ix.ChunkTokens/2 {
		errs = append(errs, fmt.Errorf("index.overlap_tokens is %d; it must be between 0 and half of chunk_tokens (%d)",
			ix.OverlapTokens, ix.ChunkTokens/2))
	}
	return errs
}

// checkBuiltin checks the [builtin] section and returns one error per
// problem: a tool name Meru doesn't have, or a confirm entry that tools
// doesn't list. The second is almost always a typo or a tool taken out of
// tools and forgotten in confirm; either way the confirm line would do
// nothing, so Load says so.
func checkBuiltin(b Builtin) []error {
	var errs []error
	for _, name := range b.Tools {
		if !slices.Contains(builtinTools, name) {
			errs = append(errs, fmt.Errorf("builtin.tools: %q is not a built-in tool; the built-in tools are %s",
				name, strings.Join(builtinTools, ", ")))
		}
	}
	for _, name := range b.Confirm {
		if !slices.Contains(b.Tools, name) {
			errs = append(errs, fmt.Errorf("builtin.confirm: %q isn't in builtin.tools; add it there, or take it out of confirm", name))
		}
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

// ProfileModels returns the models the named profile fills the tiers with,
// and false for a profile it doesn't know. meru setup uses it to download a
// profile's models before any config.toml exists.
func ProfileModels(name string) (Models, bool) {
	m, ok := profiles[name]
	return m, ok
}

// renamedNetwork reports whether k is the old network key of an
// [[mcp.servers]] or [[a2a.agents]] entry, which is now called remote. Load
// names the rename rather than calling the key unknown, so a config written
// before the rename gets a message that says what to change.
func renamedNetwork(k toml.Key) bool {
	return len(k) == 3 && k[2] == "network" &&
		((k[0] == "mcp" && k[1] == "servers") || (k[0] == "a2a" && k[1] == "agents"))
}
