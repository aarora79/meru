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
- `deploy/` observability stack (`grafana/otel-lgtm`) with a Meru dashboard; the
  Usage page and `/metrics` replaced it after v0.4.11 (#106)
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
searched in 10 ms, and answered from `garden.md` with a citation. The first token
came at 565 ms.

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
- [x] `meru setup` and `meru mcp add`: a catalog of two starter servers (`google`
  for Gmail, Calendar, Drive and Docs, `obsidian` for notes), added for you or by
  copy-paste
- [x] Web search: built-in `web_search` through a SearXNG the user runs on loopback,
  and `web_fetch` for public pages, on by default behind `[builtin] tools`: raw text, an
  answer from the fast model to a prompt, or a download to `~/meru-output/downloads/`.
  A URL that no search result or question gave asks first. `meru setup` checks
  SearXNG
- [x] `meru mcp` and `/mcp` in `meru chat`: each server's state and tool counts.
  `merud` tries each server once at startup and once more per turn that offers tools,
  with no retry loop
- [x] Built-in `configure` tool that always asks; secrets in `~/.meru/secrets.toml`
- [x] `meru tools list` / `meru log`
- [x] Local commands: `[[commands]]` entries become `cmd.<name>` tools with typed
  parameters, run through `dispatch` with no shell; the `tool_calls` row holds the
  argv. The catalog carries no shell server

**Done when:** it answers a question by calling an MCP server you already run.

**Measured:** on the development machine with the `lite` profile (MiniCPM5-2B),
Meru answered a question from an Obsidian vault through the owner's own MCP server
(`npx -y obsidian-mcp serve --vault ...`), with 3 of its 12 tools allowed:
`obsidian_list_vaults`, `obsidian_search_vault` and `obsidian_read_note`. The model made two tool calls over
three rounds, the turn took 8.3 s, and `meru log` showed both rows.

## v0.4 — It knows you
- [x] Memory files under `~/.meru/memory/<kind>/`, indexed into `memories` with vector and
  keyword search
- [x] Built-in `remember` tool through `dispatch`; recall by meaning, keyword and recency
- [x] Session summaries appended to transcripts and embedded, for recall by episode
- [x] Transcripts replayed into `messages` + `message_fts`, so search can find past
  conversations
- [x] `meru memory list | add | forget`, working on the files
- [x] Skill registry with progressive disclosure
- [x] `meru skills list | show | reset`
- [x] Built-in skills `writing`, `explainer` and `web-research`, plus the `write_file`
  tool limited to `~/meru-output/`
- [x] Context budget policy across skills / memories / chunks
- [x] Read-only `read_file`, `list_folder` and `grep` over the `[index]` folders,
  with the indexer's skip rules, offered on the search routes

**Done when:** it recalls something from last week without a reminder.

## Desktop app

The owner asked for this now, ahead of v0.5. It is a client, so it needs nothing
from a later milestone.

- [x] `meru-desktop`: a Wails v3 window over the same socket, built with
  `make desktop`, outside the cgo-free build; CI builds it on macOS
- [x] The chat screen: past chats grouped by day with search, `merud`'s status,
  a work strip per answer, streamed answers rendered as sanitized Markdown,
  source chips, Copy and Try again, the stats line, the queue and Stop
- [x] Approvals as a card inside the answer, with a mail's fields laid out
- [x] "What this answer used": sources, tool calls and who they contacted
- [x] `sessions` and `session_turns` ops, read from the transcripts
- [x] The Settings screen: each connection with Off, Ask and Allow
- [x] First run in the app: the Setup screen
- [x] A switch for where Meru looks, sent to `merud` as the turn's scope
- [x] Attachments through the attach button
- [x] Attachments by drag and drop: drop any file on the window, and `merud`'s
  `attach_file` copies it to `~/meru-output/uploads/`; images go to a vision model
- [x] The chat list, in both clients: delete a chat for good, chat folders, tags
  that recall searches, and incognito chats that leave no transcript
- [ ] Linux (WebKitGTK) and Windows (WebView2) builds in CI

**Done when:** you open the app, ask a question that uses a tool, approve it in
the answer, and reopen the chat the next day to carry on.

Each piece of that is in place: tool approvals in the answer, and past chats
that reopen from their transcripts.

## Shipped outside the milestones

No milestone names these, and each is on `main`:

- Model sets and switching at run time: `/model`, `meru model`, the old answer
  model unloaded before the new one loads, `/usage by model`, counts of
  malformed tool calls, and `model_switch` lines in the transcript (#42, with the
  model picker from #37)
- Web search first, the web tools on every turn, and web notes on follow-up
  questions (#45, #50)
- The built-in `about_meru` tool (#36)
- GitHub through `gh`, as local commands (#36)
- A warm-up in the background that loads the answer model with a real prompt (#50)
- `meru chat` does what the desktop app does (#49)
- Releases with `make release`, the one-line installer and the `meru-install`
  skill (#38, #39)
- The landing page (#41)
- The Apache-2.0 license (#44)
- `web_fetch` reads pages that build their text with JavaScript, through a
  headless Chrome that `merud` installs on first need and runs for one page at a
  time behind its own proxy (#100)
- Charts on the Usage page in both clients, from `meru.db`, and a `/metrics` page
  for a Prometheus you run, in place of the Grafana stack (#106)
- Apple Notes, read live and read-only from the Notes app's database through the
  built-in `search_notes`, `list_notes` and `read_note`, on a Mac with Full Disk
  Access for `merud`

## Headless mode

The owner asked for this in #52, ahead of v0.5. It is a client of the same
socket, so it needs nothing from a later milestone.

- [x] ARCHITECTURE.md, 200 and 100 describe `merud` as an agent harness in two
  layers: the harness core and Meru the assistant
- [x] `meru run --json "..."` writes each socket event to stdout as one JSON line
  and exits non-zero when the turn fails; a call that asks first is denied, and
  its `approval` line shows which
- [x] A second agent built on it, a weekly digest of an `obsidian` vault, with no
  change to Meru's prompt, and a record of what `agent.New` or `Handle` had to
  change for it (`docs/examples/vault-digest.sh`; nothing had to change, and the
  seams it found are in `docs/coding-notes/merud.md`)
- [x] The `done` event carries time to last token (`ttlt_ms`) and time per output
  token (`tpot_ms`) beside the token counts and time to first token

**Done when:** a script runs the second agent through `meru run --json`, reads
its events as JSON, and `meru log` shows its tool calls.

**Measured:** on the development machine, `vault-digest.sh` listed the vault,
searched it and read four notes through the `obsidian` server in 10.3 s, and
`meru log` showed all six calls.

## Benchmark

The owner asked for this ahead of v0.5. It measures the model sets Meru already
has, so it needs nothing from a later milestone.

- [x] `meru check` records each turn's model set, time to first and last token,
  time per output token and token counts, and `answer_none` fails an answer that
  claims an action no tool took
- [x] `meru check report` turns saved results into a Markdown page with Mermaid
  charts
- [x] `make bench` runs a private dataset in `bench/`, which git ignores, against
  every model set in a Meru home of its own; `make bench-report` writes
  `docs/benchmarks/results.md`
- [x] `docs/benchmarks/README.md` says the results come from private data and how
  to build a dataset of your own
- [x] A first published run over all three model sets

**Done when:** `docs/benchmarks/results.md` compares every model set on the
private dataset, with pass rates by kind of question and the timings.

**Measured:** on the development machine, three passes of the 50 questions per
set passed 92% with `gemma-moe`, 90% with `qwen-dense` and 89% with `qwen-moe`.
Chained tool calls split them most (78%, 67% and 61%), and `qwen-moe` answered
fastest, with a median of 6.5 s to the last token.

## Speech to text

The owner asked for this ahead of v0.5. It turns speech into a typed question,
so it needs nothing from a later milestone. The design is in ARCHITECTURE.md,
[Speech to text](ARCHITECTURE.md#speech-to-text).

- [x] A test of both paths, Gemma 4 in Ollama and whisper.cpp, and the design in
  ARCHITECTURE.md, 200 and 100
- [x] A GitHub Actions workflow that builds `whisper-server` for Apple silicon
  from pinned source and publishes it as the release `whisper-server-v1.9.5`
- [x] Connectors: a `service` kind and `[[install.files]]`
- [x] The `whisper` connector, with a Whisper model per profile as its `model`
  setting's default
- [x] `internal/speech` and the `transcribe` op, with the socket's limit at 4 MiB,
  and `meru transcribe`
- [x] Meru.app: the microphone button, the microphone prompt and the
  no-microphone message
- [ ] The installer step and a FAQ page

**Done when:** on a Mac with a microphone you press the button in Meru.app, say
a question, see the words in the box, fix one, press Send, and get an answer,
with no audio file left on disk.

## v0.5 — It acts unprompted
- [x] Job definitions (prompt + cron) as `[[jobs]]` in `config.toml`, with a
  named skill, every N weeks, and each run a chat in the Jobs folder
  ([#167](https://github.com/aarora79/meru/issues/167))
- [x] In-daemon scheduler sharing the warm model, with catch-up after sleep,
  Run now and Stop in both clients
- [x] Jobs that run a declared command, pre-approve tools, or write a file
- [x] Adding, changing, pausing and removing jobs from both clients
- [x] Adding jobs by asking Meru (`schedule_job`)

**Done when:** a morning brief lands without you asking. Shipped in v0.5.0: a
job such as "every weekday at 7, brief me on my calendar" runs in its own chat.

## v0.6 — Meru on Linux
- [x] Meru working end to end on Ubuntu 24.04: an install path, `merud` as a systemd
  user service, answer models picked by GPU memory, and the desktop app
  ([#84](https://github.com/aarora79/meru/issues/84))

**Done when:** one documented command sets up the same Meru on a fresh Ubuntu
machine. Shipped in v0.6.0: `install-linux.sh` installs Meru, Ollama, the model
that fits the graphics card, and `merud` as a systemd user service.

## v0.7 — It tells you, and reaches more of your tools
- [ ] Digests, notifications, `meru brief`
- [ ] Third-party MCP servers: Meru installs, configures, starts and heals more
  servers than Google, Obsidian and web search, through the same pinned
  connector manifests ([#87](https://github.com/aarora79/meru/issues/87))
- [ ] Read Word, Excel and CSV files, and answer questions about tables
  ([#184](https://github.com/aarora79/meru/issues/184))
- [ ] Live transcription: show the words while the user speaks
  ([#150](https://github.com/aarora79/meru/issues/150))
- [ ] New built-in tools reach configs that list them by hand
  ([#123](https://github.com/aarora79/meru/issues/123))
- [ ] Fixes: text from tool rounds runs together
  ([#154](https://github.com/aarora79/meru/issues/154)); metrics miss the fixed
  prompt and the fast and embed calls
  ([#134](https://github.com/aarora79/meru/issues/134)); profile memories past the
  cap never reach the prompt ([#132](https://github.com/aarora79/meru/issues/132));
  incognito text on spans with capture on
  ([#130](https://github.com/aarora79/meru/issues/130))

**Done when:** a digest of the day's job runs reaches you without opening Meru,
and a third-party MCP server Meru doesn't ship today installs and heals the way
Google and Obsidian do.

## Later, maybe
- Voice beyond speech to text: answers read aloud and a wake word
- Menu-bar companion app, next to the desktop app
- Escalation to a larger model without asking: you can switch models by hand, and
  the switch unloads the old one first, but Meru never picks the larger model itself
- `LlamaCppEngine`: llama.cpp embedded via cgo, no Ollama needed
- `MLXEngine`, if Go bindings become practical
- Windows service and a Windows test pass ([#85](https://github.com/aarora79/meru/issues/85))
