# Meru — Roadmap

Each milestone is useful on its own. We ship them in this order.

## v0.1 — It talks, and answers fast
- `merud` daemon + Unix socket protocol
- `Engine` interface; `OllamaEngine` (loopback only), with log probabilities for
  routing (Ollama v0.12.11 or later)
- Router: one-token route classification with a confidence floor and a
  `search+tools` fallback ([docs/fast-router.md](docs/fast-router.md))
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

**Measured:** on the development machine with the `lite` profile, the end-to-end
integration test got its first token in 185 ms, and the Grafana dashboard showed
the timing.

## v0.2 — It knows your files
- [x] SQLite store via `ncruces/go-sqlite3`: FTS5 for keywords, vectors in a plain
  table compared with vec1's distance function, rebuilt from files
- [x] Structure-aware chunking (Markdown, code, HTML, PDF)
- [x] Incremental indexer (`meru index`, mtime + hash), with skip rules,
  `.meruignore` and a file watcher
- [x] Hybrid retrieval: FTS5 BM25 + vector distance, merged with reciprocal-rank
  fusion in Go
- [x] Citations in answers: a `sources` event, and a `Sources:` list in `meru` and
  `meru chat`
- [x] Router calibration: a labelled set of 135 questions and `make router-eval`

**Done when:** it answers a question about a local note and cites the file.

**Measured:** on the development machine with the `lite` profile, the end-to-end
integration test indexed a notes folder, routed its question to `search+tools`,
searched in 10 ms, and answered "The Q3 budget for the garden project is 4,200
dollars … [1]", citing `garden.md`, with its first token at 565 ms.

With 768-dimension vectors on the same machine, the store indexes 100,000 chunks in
6.1 s, replaces a 100-chunk file in about 6 ms, and searches them in 147 ms by
vector and 90 ms by keyword. The calibrated router, told which folders you index,
picks the labelled route for 32 of 40 held-out questions in about 28 ms; the v0.1
prompt picked 17 of 36.

## v0.3 — It can do things
- [x] MCP client: stdio + Streamable HTTP transports
- [x] Server config, per-tool allowlist, deny-by-default
- [x] Agent loop with single `dispatch` path: allowlist, confirmation, `tool_calls` audit
  trail, tool spans and metrics
- [x] Approval prompt in `meru chat` and one-shot `meru`: approve once, approve for this
  session, or deny
- [x] A2A client: remote agent skills exposed as tools through the same `dispatch`
- [x] `meru setup` and `meru mcp add`: a catalog of starter servers (web search, fetch,
  Gmail, Calendar, Drive and Docs, Obsidian), added for you or by copy-paste
- [x] Built-in `configure` tool that always asks; secrets in `~/.meru/secrets.toml`
- [x] `meru tools list` / `meru log`

**Done when:** it answers a question by calling an MCP server you already run.

**Measured:** on the development machine with the `lite` profile (MiniCPM5-2B),
Meru answered a question from an Obsidian vault through the owner's own MCP server
(`npx -y obsidian-mcp serve --vault ...`), with 3 of its 12 tools allowed:
`obsidian_list_vaults`, `obsidian_search_vault` and `obsidian_read_note`. The model made two tool calls over
three rounds, the turn took 8.3 s, and `meru log` showed both rows.

## v0.4 — It knows you
- Memory files under `~/.meru/memory/<kind>/`, indexed into `memories` with vector and
  keyword search
- Built-in `remember` tool through `dispatch`; recall by meaning, keyword and recency
- Session summaries appended to transcripts and embedded, for recall by episode
- Transcripts replayed into `messages` + `message_fts`, so search can find past
  conversations
- `meru memory list | add | forget`, working on the files
- [x] Skill registry with progressive disclosure
- [x] `meru skills list | show | reset`
- [x] Built-in skills `writing` and `explainer`, plus the `write_file`
  tool limited to `~/meru-output/`
- Context budget policy across skills / memories / chunks

**Done when:** it recalls something from last week without a reminder.

## v0.5 — It acts unprompted
- Job definitions (prompt + cron) as `[[jobs]]` in `config.toml`
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
