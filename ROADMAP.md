# Meru — Roadmap

Each milestone is useful on its own. We ship them in this order.

## v0.1 — It talks, and answers fast
- `merud` daemon + Unix socket protocol
- `Engine` interface; `OllamaEngine` (loopback only)
- Model tiers (`fast`, `main`, `embed`) kept warm in Ollama; `lite` profile by default,
  `full` profile in config
- OpenTelemetry metrics + traces for turns and model calls, OTLP export (loopback only)
- `deploy/` observability stack (`grafana/otel-lgtm`) with a Meru dashboard
- `meru "..."` for one question (plain streamed text) and `meru chat`, a terminal UI
  built with Bubble Tea
- `config.toml` + `~/.meru/` layout
- Session transcripts appended to `~/.meru/sessions/*.jsonl`
- Service files to keep `merud` running: `launchd` (macOS) and `systemd` (Linux)

**Done when:** with `merud` already running, a fresh `meru "hello"` shows its first
token in under a second, and the dashboard shows the timing.

## v0.2 — It knows your files
- SQLite store via `ncruces/go-sqlite3` + `sqlite-vec` + FTS5 schema, rebuilt from files
- Transcripts replayed into `messages` + `message_fts`
- Structure-aware chunking (markdown, code, PDF)
- Incremental indexer (`meru index`, mtime + hash)
- Hybrid retrieval: FTS5 BM25 + `sqlite-vec` similarity, merged with reciprocal-rank
  fusion in Go
- Citations in answers

**Done when:** it answers a question about a local note and cites the file.

## v0.3 — It can do things
- MCP client: stdio + Streamable HTTP transports
- Server config, per-tool allowlist, deny-by-default
- Agent loop with single `dispatch` path: allowlist, confirmation, `tool_calls` audit
  trail, tool spans and metrics
- Approval prompt in `meru chat` and one-shot `meru`: approve once, approve for this
  session, or deny
- A2A client: remote agent skills exposed as tools through the same `dispatch`
- `meru setup` and `meru mcp add`: a catalog of starter servers (web search, fetch,
  Gmail, Calendar, Drive, Obsidian), added for you or by copy-paste
- Built-in `configure` tool that always asks; secrets in `~/.meru/secrets.toml`
- `meru tools list` / `meru log`

**Done when:** it answers a question by calling an MCP server you already run.

## v0.4 — It knows you
- Memory files under `~/.meru/memory/<kind>/`, indexed into `memories` with vector and
  keyword search
- Built-in `remember` tool through `dispatch`; recall by meaning, keyword and recency
- Session summaries appended to transcripts and embedded, for recall by episode
- `meru memory list | add | forget`, working on the files
- Skill registry with progressive disclosure
- `meru skills list | show | reset`
- Built-in skills `writing`, `explainer` and `poster-making`, plus the `write_file`
  tool limited to `~/meru-output/`
- Context budget policy across skills / memories / chunks

**Done when:** it recalls something from last week without a reminder.

## v0.5 — It acts unprompted
- Job definitions (prompt + cron) in the store
- In-daemon scheduler sharing the warm model
- Digests, notifications, `meru brief`

**Done when:** a morning brief lands without you asking.

## Later, maybe
- Voice: local speech-to-text, text-to-speech and a wake word
- Menu-bar companion app
- A larger escalation model loaded on demand, with clean eviction
- `LlamaCppEngine`: llama.cpp embedded via cgo, no Ollama needed
- `MLXEngine`, if Go bindings become practical
- Windows service and a Windows test pass
