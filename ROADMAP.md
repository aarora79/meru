# Meru — Roadmap

Each milestone is independently useful. Ship in order.

## v0.1 — It talks, and it's instant
- `merud` daemon + Unix socket protocol
- `Engine` protocol; `MLXEngine` and `OllamaEngine`
- Model tier loading (`fast`, `main`, `embed`) held resident
- `meru "..."` one-shot and `meru chat` TUI, streaming
- `config.toml` + `~/.meru/` layout
- `launchd` plist for daemon lifecycle

**Done when:** a cold `meru "hello"` returns first token in under a second.

## v0.2 — It knows your files
- SQLite store + `sqlite-vec` + FTS5 schema
- Structure-aware chunking (markdown, code, PDF)
- Incremental indexer (`meru index`, mtime + hash)
- Hybrid retrieval with reciprocal-rank fusion
- Citations in answers

**Done when:** it answers a question about a local note and cites the file.

## v0.3 — It can do things
- MCP client: stdio + SSE transports
- Server config, per-tool allowlist, deny-by-default
- Tool-call loop with `tool_calls` audit trail
- `meru tools list` / `meru log`

**Done when:** it answers a question by calling an MCP server you already run.

## v0.4 — It's yours specifically
- `memories` table, memory tool, relevance + recency retrieval
- `meru memory list | add | forget`
- Skill registry with progressive disclosure
- `meru skills list | show`
- Context budget policy across skills / memories / chunks

**Done when:** it recalls something from last week without being told.

## v0.5 — It acts unprompted
- Job definitions (prompt + cron) in the store
- In-daemon scheduler sharing the warm model
- Digests, notifications, `meru brief`

**Done when:** a morning brief lands without you asking.

## Later, maybe
- Voice: local STT + TTS, wake word
- Menu-bar companion app
- `heavy` tier escalation with clean eviction
- Linux + CUDA engine backend
