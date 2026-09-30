// This file tests Load: defaults, profiles, overrides and every validation
// error, plus the loopback check on its own.

package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/loopback"
)

// writeConfig writes body to config.toml in a fresh temporary directory and
// returns the file's path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := defaults()
	want.Models = profiles["lite"]
	want.Dir = dir
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load of a missing file:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadProfilesAndOverrides(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Models
	}{
		{"empty file is lite", "", profiles["lite"]},
		{"lite", `profile = "lite"`, profiles["lite"]},
		{"full", `profile = "full"`, Models{
			Fast:  "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M",
			Main:  "qwen3.6:35b-a3b-mxfp8",
			Embed: "qwen3-embedding:0.6b",
		}},
		{"full with main override", "profile = \"full\"\n[models]\nmain = \"my-model\"", Models{
			Fast:  "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M",
			Main:  "my-model",
			Embed: "qwen3-embedding:0.6b",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := (Models{Fast: cfg.Models.Fast, Main: cfg.Models.Main, Embed: cfg.Models.Embed}); got.Fast != tt.want.Fast || got.Main != tt.want.Main || got.Embed != tt.want.Embed {
				t.Errorf("models = %+v, want %+v", cfg.Models, tt.want)
			}
		})
	}
}

func TestLoadKeepsExplicitValues(t *testing.T) {
	body := `
[ollama]
base_url = "http://localhost:11434"
keep_alive = "30m"
context_length = 8192

[agent]
max_rounds = 3
max_output_tokens = 2048
turn_timeout = "90s"
history_turns = 0
system_prompt = "Be brief."

[router]
top_logprobs = 5
temperature = 0.7
min_confidence = 0
fallback = "direct"
decision = "marginal"
search_threshold = 0.4
tools_threshold = 0

[observability]
otlp_endpoint = "http://[::1]:4318"
metrics_interval = "1m"
traces = false
capture_content = true

[log]
level = "debug"

[index]
folders = ["~/notes", "~"]
ignore = ["*.log", "drafts/"]
max_file_mb = 20
chunk_tokens = 300
overlap_tokens = 0
watch = false
retrieval = "agentic"

[builtin]
tools   = ["configure", "grep", "search_files"]
confirm = ["configure"]

[skills]
disabled = ["explainer", "not-yet"]

[web]
searxng_url = "http://localhost:8889"
max_results = 5

[chat]
mouse_copy = false

[[mcp.servers]]
name    = "notes"
command = "notes-mcp"
args    = ["--root", "~/notes"]
env     = { NOTES_TOKEN = "secret:notes_token" }
allow   = ["search", "read"]
confirm = ["read"]
timeout = "30s"

[[mcp.servers]]
name    = "calendar"
url     = "http://127.0.0.1:8123/mcp"
headers = { Authorization = "secret:calendar_auth" }
allow   = ["list_events"]

[[a2a.agents]]
name    = "research"
url     = "http://127.0.0.1:9100"
allow   = ["summarize"]
confirm = ["summarize"]
remote  = false
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Profile: "lite",
		Models:  profiles["lite"],
		Ollama:  Ollama{BaseURL: "http://localhost:11434", KeepAlive: "30m", ContextLength: 8192},
		Agent:   Agent{MaxRounds: 3, MaxOutputTokens: 2048, TurnTimeout: "90s", HistoryTurns: 0, SystemPrompt: "Be brief.", SummaryIdle: "30m"},
		Skills:  Skills{OutputDir: "~/meru-output", Disabled: []string{"explainer", "not-yet"}},
		Router: Router{TopLogProbs: 5, Temperature: 0.7, MinConfidence: 0, Fallback: "direct",
			Decision: "marginal", SearchThreshold: 0.4, ToolsThreshold: 0},
		Observability: Observability{OTLPEndpoint: "http://[::1]:4318", MetricsInterval: "1m", Traces: false, CaptureContent: true},
		Log:           Log{Level: "debug"},
		Index: Index{
			Folders:       []string{"~/notes", "~"},
			Ignore:        []string{"*.log", "drafts/"},
			MaxFileMB:     20,
			ChunkTokens:   300,
			OverlapTokens: 0,
			Watch:         false,
			Retrieval:     RetrievalAgentic,
		},
		Builtin: Builtin{Tools: []string{"configure", "grep", "search_files"}, Confirm: []string{"configure"}},
		Web:     Web{SearXNGURL: "http://localhost:8889", MaxResults: 5},
		Chat:    Chat{MouseCopy: false}, // false in the file beats the default, true
		MCP: MCP{Servers: []MCPServer{
			{
				Name: "notes", Command: "notes-mcp", Args: []string{"--root", "~/notes"},
				Env:   map[string]string{"NOTES_TOKEN": "secret:notes_token"},
				Allow: []string{"search", "read"}, Confirm: []string{"read"}, Timeout: "30s",
			},
			{
				Name: "calendar", URL: "http://127.0.0.1:8123/mcp",
				Headers: map[string]string{"Authorization": "secret:calendar_auth"},
				Allow:   []string{"list_events"},
			},
		}},
		A2A: A2A{Agents: []A2AAgent{
			{Name: "research", URL: "http://127.0.0.1:9100", Allow: []string{"summarize"}, Confirm: []string{"summarize"}},
		}},
	}
	cfg.Dir = ""
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // a piece of the error message
	}{
		{"bad toml", "profile = ", "read config"},
		{"unknown key", "profil = \"lite\"", "unknown keys: profil"},
		{"unknown nested key", "[router]\ntop = 3", "unknown keys: router.top"},
		{"unknown profile", `profile = "huge"`, `profile "huge" is unknown`},
		{"ollama not loopback", "[ollama]\nbase_url = \"http://10.0.0.5:11434\"", "ollama.base_url"},
		{"ollama hostname", "[ollama]\nbase_url = \"http://ollama.example.com\"", "ollama.base_url"},
		{"ollama scheme", "[ollama]\nbase_url = \"ftp://127.0.0.1\"", "scheme must be http or https"},
		{"ollama empty", "[ollama]\nbase_url = \"\"", "ollama.base_url"},
		{"keep_alive", "[ollama]\nkeep_alive = \"forever\"", "ollama.keep_alive"},
		{"context_length", "[ollama]\ncontext_length = 100", "ollama.context_length"},
		{"max_rounds", "[agent]\nmax_rounds = 0", "agent.max_rounds"},
		{"max_output_tokens", "[agent]\nmax_output_tokens = 0", "agent.max_output_tokens"},
		{"turn_timeout", "[agent]\nturn_timeout = \"soon\"", "agent.turn_timeout"},
		{"turn_timeout zero", "[agent]\nturn_timeout = \"0s\"", "agent.turn_timeout"},
		{"history_turns", "[agent]\nhistory_turns = -1", "agent.history_turns"},
		{"top_logprobs low", "[router]\ntop_logprobs = 0", "router.top_logprobs"},
		{"top_logprobs high", "[router]\ntop_logprobs = 21", "router.top_logprobs"},
		{"temperature zero", "[router]\ntemperature = 0.0", "router.temperature"},
		{"temperature negative", "[router]\ntemperature = -1.0", "router.temperature"},
		{"temperature inf", "[router]\ntemperature = inf", "router.temperature"},
		{"min_confidence high", "[router]\nmin_confidence = 1.5", "router.min_confidence"},
		{"min_confidence nan", "[router]\nmin_confidence = nan", "router.min_confidence"},
		{"fallback", "[router]\nfallback = \"guess\"", `router.fallback "guess"`},
		{"decision", "[router]\ndecision = \"both\"", `router.decision "both"`},
		{"search_threshold high", "[router]\nsearch_threshold = 1.5", "router.search_threshold"},
		{"tools_threshold nan", "[router]\ntools_threshold = nan", "router.tools_threshold"},
		{"otlp not loopback", "[observability]\notlp_endpoint = \"http://collector.example.com:4318\"", "observability.otlp_endpoint"},
		{"otlp all interfaces", "[observability]\notlp_endpoint = \"http://0.0.0.0:4318\"", "observability.otlp_endpoint"},
		{"interval bad", "[observability]\nmetrics_interval = \"often\"", "observability.metrics_interval"},
		{"interval zero", "[observability]\nmetrics_interval = \"0s\"", "observability.metrics_interval"},
		{"log level unknown", "[log]\nlevel = \"loud\"", `log.level "loud" is unknown`},
		{"log level empty", "[log]\nlevel = \"\"", `log.level "" is unknown`},
		{"log level upper case", "[log]\nlevel = \"DEBUG\"", `log.level "DEBUG" is unknown`},
		{"index relative folder", "[index]\nfolders = [\"notes\"]", `index.folders: "notes" must be an absolute path`},
		{"index tilde user", "[index]\nfolders = [\"~bob/notes\"]", `index.folders: "~bob/notes"`},
		{"index empty folder", "[index]\nfolders = [\"\"]", `index.folders: ""`},
		{"index empty ignore", "[index]\nignore = [\" \"]", "index.ignore"},
		{"index max_file_mb zero", "[index]\nmax_file_mb = 0", "index.max_file_mb"},
		{"index max_file_mb huge", "[index]\nmax_file_mb = 5000", "index.max_file_mb"},
		{"index chunk_tokens low", "[index]\nchunk_tokens = 10", "index.chunk_tokens"},
		{"index chunk_tokens high", "[index]\nchunk_tokens = 10000", "index.chunk_tokens"},
		{"index overlap negative", "[index]\noverlap_tokens = -1", "index.overlap_tokens"},
		{"index overlap past half", "[index]\nchunk_tokens = 100\noverlap_tokens = 51", "index.overlap_tokens"},
		{"index unknown key", "[index]\nfolder = []", "unknown keys: index.folder"},
		{"index retrieval unknown", "[index]\nretrieval = \"rag\"", `index.retrieval "rag" is unknown; use "auto" or "agentic"`},
		{"index retrieval empty", "[index]\nretrieval = \"\"", `index.retrieval "" is unknown`},
		{"agentic without search_files", "[index]\nretrieval = \"agentic\"\n[builtin]\ntools = [\"grep\"]\nconfirm = []", "builtin.tools doesn't list search_files"},
		{"old mcp network key", "[[mcp.servers]]\nname = \"g\"\nurl = \"http://127.0.0.1:8000/mcp\"\nnetwork = true", "network was renamed remote"},
		{"old a2a network key", "[[a2a.agents]]\nname = \"r\"\nurl = \"http://127.0.0.1:9100\"\nnetwork = false", "network was renamed remote"},
		{"output_dir empty", "[skills]\noutput_dir = \"\"", "skills.output_dir is empty"},
		{"output_dir relative", "[skills]\noutput_dir = \"out\"", `skills.output_dir "out" must be an absolute path`},
		{"searxng not loopback", "[web]\nsearxng_url = \"http://192.168.1.20:8888\"", "web.searxng_url"},
		{"searxng hostname", "[web]\nsearxng_url = \"https://searx.example.org\"", "web.searxng_url"},
		{"searxng scheme", "[web]\nsearxng_url = \"127.0.0.1:8888\"", "web.searxng_url"},
		{"web max_results zero", "[web]\nmax_results = 0", "web.max_results"},
		{"web max_results high", "[web]\nmax_results = 21", "web.max_results"},
		{"web unknown key", "[web]\nread_page = true", "unknown keys: web.read_page"},
		{"old web read_pages key", "[web]\nread_pages = true", movedFetch},
		{"old web fetch key", "[web]\nfetch = false", movedFetch},
		{"old web fetch key true", "[web]\nfetch = true", movedFetch},
		{"builtin unknown tool", "[builtin]\ntools = [\"grep\", \"shell\"]", `builtin.tools: "shell" is not a built-in tool; the built-in tools are configure, datetime,`},
		{"builtin confirm not in tools", "[builtin]\ntools = [\"grep\"]\nconfirm = [\"write_file\"]", `builtin.confirm: "write_file" isn't in builtin.tools`},
		{"set without a name", "[[models.sets]]\nmain = \"m\"", "models.sets: entry 1 has no name"},
		{"set name with a space", "[[models.sets]]\nname = \"my set\"\nmain = \"m\"", `models.sets: name "my set" may hold only`},
		{"set name twice", "[[models.sets]]\nname = \"a\"\nmain = \"m\"\n[[models.sets]]\nname = \"a\"\nmain = \"n\"", `the name "a" is used twice`},
		{"set with no model", "[[models.sets]]\nname = \"empty\"\nthink = false", `set "empty" names no model`},
		{"set unknown key", "[[models.sets]]\nname = \"a\"\nmain = \"m\"\nmodel = \"x\"", "unknown keys: models.sets.model"},
		{"builtin default confirm, tools empty", "[builtin]\ntools = []", `builtin.confirm: "write_file" isn't in builtin.tools`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatalf("Load succeeded; want an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestBuiltinConfirmDefault checks that write_file asks by default, and that
// a config that lists no built-ins gets an empty list, not the default.
func TestBuiltinConfirmDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Builtin.Confirm, []string{"write_file"}) {
		t.Errorf("default builtin.confirm = %q, want [write_file]", cfg.Builtin.Confirm)
	}
	cfg, err = Load(writeConfig(t, "[builtin]\nconfirm = []"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Builtin.Confirm) != 0 {
		t.Errorf("builtin.confirm = %q after confirm = [], want empty", cfg.Builtin.Confirm)
	}
}

// TestBuiltinToolsDefault checks that every built-in tool is on by
// default, that tools = [] turns them all off, and that BuiltinTools
// hands out a copy.
func TestBuiltinToolsDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Builtin.Tools, builtinTools) || len(builtinTools) != 11 {
		t.Errorf("default builtin.tools = %q, want all eleven: %q", cfg.Builtin.Tools, builtinTools)
	}
	cfg, err = Load(writeConfig(t, "[builtin]\ntools = []\nconfirm = []"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Builtin.Tools) != 0 {
		t.Errorf("builtin.tools = %q after tools = [], want empty", cfg.Builtin.Tools)
	}
	names := BuiltinTools()
	names[0] = "changed"
	if builtinTools[0] != "configure" {
		t.Error("BuiltinTools returned the package's own slice")
	}
}

// TestWebDefaults checks the [web] defaults: search on at the SearXNG
// port the docs use, and an empty URL turning search off.
func TestWebDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Web{SearXNGURL: "http://127.0.0.1:8888", MaxResults: 8}
	if cfg.Web != want {
		t.Errorf("default web = %+v, want %+v", cfg.Web, want)
	}
	cfg, err = Load(writeConfig(t, "[web]\nsearxng_url = \"\"\nmax_results = 20"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want = Web{SearXNGURL: "", MaxResults: 20}
	if cfg.Web != want {
		t.Errorf("web = %+v, want %+v", cfg.Web, want)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	body := "profile = \"huge\"\n[agent]\nmax_rounds = 0\n[router]\nfallback = \"guess\""
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("Load succeeded; want an error")
	}
	for _, want := range []string{"profile", "agent.max_rounds", "router.fallback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't mention %s", err, want)
		}
	}
}

// TestCheckLoopbackURL pins the loopback rule config relies on for the Ollama
// and OTLP addresses. The check itself lives in internal/engine.
func TestCheckLoopbackURL(t *testing.T) {
	tests := []struct {
		url string
		ok  bool
	}{
		{"http://127.0.0.1:11434", true},
		{"http://127.1.2.3:11434", true},
		{"https://127.0.0.1", true},
		{"http://[::1]:4318", true},
		{"http://[::ffff:127.0.0.1]:4318", true},
		{"http://localhost:11434", true},
		{"http://LOCALHOST:11434", true},
		{"http://10.0.0.1:11434", false},
		{"http://0.0.0.0:11434", false},
		{"http://[::]:11434", false},
		{"http://example.com", false},
		{"http://127.0.0.1.example.com", false},
		{"unix:///tmp/ollama.sock", false},
		{"127.0.0.1:11434", false},
		{"http://", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := loopback.CheckURL(tt.url)
			if tt.ok && err != nil {
				t.Errorf("loopback.CheckURL(%q) = %v, want nil", tt.url, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("loopback.CheckURL(%q) = nil, want an error", tt.url)
			}
		})
	}
}

// TestTemplateMatchesDefaults checks that the template parses, uses only
// known keys, and shows the real defaults: loading it gives the same
// Config as no file at all.
func TestTemplateMatchesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(Template()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load the template: %v", err)
	}
	want := defaults()
	want.Models = profiles["lite"]
	want.Dir = dir
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("template:\n got %+v\nwant %+v", cfg, want)
	}
}

// TestExampleIsTemplate checks that config.example.toml at the repo root is
// a byte-for-byte copy of template.toml, the file the binaries embed.
func TestExampleIsTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Template() {
		t.Error("config.example.toml differs from internal/config/template.toml. Edit the template, " +
			"then copy it over the example: cp internal/config/template.toml config.example.toml")
	}
}

// TestTemplateCommentedBlocks uncomments the sample [[mcp.servers]],
// [[a2a.agents]] and [[commands]] blocks in the template and checks that
// they load, so the samples can't drift from the real keys. A sample
// starts at a "# [[" line and ends at the first line that isn't "# " plus
// text. The commands package checks the [[commands]] samples further, and
// the catalog package checks that the catalog's servers match its Block.
func TestTemplateCommentedBlocks(t *testing.T) {
	var sample strings.Builder
	in := false
	for _, line := range strings.Split(Template(), "\n") {
		if strings.HasPrefix(line, "# [[") || strings.HasPrefix(line, "# [connectors.") {
			in = true
		}
		if in && !strings.HasPrefix(line, "# ") {
			in = false
		}
		if in {
			sample.WriteString(strings.TrimPrefix(line, "# ") + "\n")
		}
	}
	cfg, err := Load(writeConfig(t, sample.String()))
	if err != nil {
		t.Fatalf("Load the samples: %v\n%s", err, sample.String())
	}
	if len(cfg.MCP.Servers) != 3 || len(cfg.A2A.Agents) != 1 || len(cfg.Commands) != 25 || len(cfg.Models.Sets) != 3 {
		t.Errorf("samples hold %d servers, %d agents, %d commands and %d model sets, want 3, 1, 25 and 3",
			len(cfg.MCP.Servers), len(cfg.A2A.Agents), len(cfg.Commands), len(cfg.Models.Sets))
	}
	if len(cfg.Connectors) != 1 {
		t.Errorf("samples hold %d connector tables, want 1", len(cfg.Connectors))
	}
}

// TestLoadConnectors checks the [connectors.<id>] tables: values load as
// written, Enabled and Value read them, and each shape rule refuses what
// it names.
func TestLoadConnectors(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[connectors.obsidian]
enabled = true
vault_path = "~/Notes/vault"

[connectors.searxng]
enabled = false
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	obs := cfg.Connectors["obsidian"]
	if on, ok := obs.Enabled(); !on || !ok {
		t.Errorf("obsidian Enabled() = %v, %v; want true, true", on, ok)
	}
	if v, ok := obs.Value("vault_path"); v != "~/Notes/vault" || !ok {
		t.Errorf("obsidian Value(vault_path) = %q, %v", v, ok)
	}
	if _, ok := obs.Value("enabled"); ok {
		t.Error("Value(enabled) found a string; enabled is a bool")
	}
	if on, ok := cfg.Connectors["searxng"].Enabled(); on || !ok {
		t.Errorf("searxng Enabled() = %v, %v; want false, true", on, ok)
	}
	if _, ok := (Connector{}).Enabled(); ok {
		t.Error("an empty table says enabled is set")
	}

	bad := []struct{ name, body, want string }{
		{"bad id", "[connectors.My-Notes]\nenabled = true", "connectors.My-Notes: a connector id may hold only"},
		{"bad key", "[connectors.obsidian]\nVault = \"x\"", `the key "Vault" may hold only`},
		{"enabled as a string", "[connectors.obsidian]\nenabled = \"yes\"", `connectors.obsidian.enabled is "yes"; write true or false`},
		{"number value", "[connectors.obsidian]\nport = 8000", "connectors.obsidian.port must be a string"},
		{"list value", "[connectors.obsidian]\nvaults = [\"a\"]", "connectors.obsidian.vaults must be a string"},
		{"nested table", "[connectors.obsidian.extra]\nx = \"y\"", "unknown keys: connectors.obsidian.extra.x"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestLoadModelSets checks that [[models.sets]] entries load as written,
// that think tells "off" from "left out", and that FindSet finds one by
// name.
func TestLoadModelSets(t *testing.T) {
	body := `profile = "full"

[[models.sets]]
name  = "gemma-moe"
main  = "gemma4:26b-mxfp8"
think = false

[[models.sets]]
name  = "small"
main  = "gemma3:12b"
fast  = "gemma3:1b"
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Main != "qwen3.6:35b-a3b-mxfp8" {
		t.Errorf("main = %q; a set must not change the default", cfg.Models.Main)
	}
	if len(cfg.Models.Sets) != 2 {
		t.Fatalf("sets = %+v, want 2", cfg.Models.Sets)
	}
	moe, ok := FindSet(cfg.Models.Sets, "gemma-moe")
	if !ok || moe.Main != "gemma4:26b-mxfp8" || !moe.ThinkOff() {
		t.Errorf("gemma-moe = %+v, %v; want its main and thinking off", moe, ok)
	}
	small, _ := FindSet(cfg.Models.Sets, "small")
	if small.ThinkOff() || small.Think != nil || small.Fast != "gemma3:1b" {
		t.Errorf("small = %+v; want fast set and think left out", small)
	}
	if _, ok := FindSet(cfg.Models.Sets, "nope"); ok {
		t.Error("FindSet found a set that isn't there")
	}
}

// TestProfileModels checks the accessor meru setup uses.
func TestProfileModels(t *testing.T) {
	if m, ok := ProfileModels("full"); !ok || m.Main != profiles["full"].Main || m.Fast != profiles["full"].Fast || m.Embed != profiles["full"].Embed {
		t.Errorf("ProfileModels(full) = %+v, %v", m, ok)
	}
	if _, ok := ProfileModels("huge"); ok {
		t.Error("ProfileModels found a profile that doesn't exist")
	}
}

// TestLogLevel checks each [log] level name and the slog level it maps to.
func TestLogLevel(t *testing.T) {
	tests := []struct {
		name string
		want slog.Level
		ok   bool
	}{
		{"debug", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"trace", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := LogLevel(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("LogLevel(%q) = %v, %v; want %v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	// t.Setenv sets an environment variable for this test only.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".meru", "config.toml")
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

// TestRetrievalDefault checks that [index] retrieval defaults to "auto",
// today's search before the answer, when the file leaves it out.
func TestRetrievalDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, "[index]\nfolders = [\"~/notes\"]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Index.Retrieval != RetrievalAuto {
		t.Errorf("index.retrieval = %q, want %q", cfg.Index.Retrieval, RetrievalAuto)
	}
}
