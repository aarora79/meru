# meru — agent guide

**Meru** is a personal AI assistant that runs entirely on a machine you control: a
resident daemon (`merud`) that keeps open-weight models warm in Ollama, plus a thin CLI
(`meru`) that talks to it over a Unix socket. It is written in **Go**, so it ships as
native binaries you can redistribute. macOS on Apple silicon is the primary platform;
Linux is supported; Windows should work but isn't tested at first.

Write every piece of code so that a developer new to Go can follow it, and explain
it in `docs/coding-notes/` (see [Explaining the code](#explaining-the-code)). Many
readers and contributors will come from Python, and code that reads plainly is also
easier to review.

## Read first

- [ARCHITECTURE.md](ARCHITECTURE.md) — the design contract (level 300), written ahead
  of the code. [docs/architecture/100.md](docs/architecture/100.md) and
  [200.md](docs/architecture/200.md) explain the same design at gentler levels.
  **Change ARCHITECTURE.md first**, then carry the change into 200, 100 and their
  HTML pages (`docs/architecture/*.html`) in the same PR.
  If code and this doc disagree, one of them has a bug. Say which one; don't pick
  without saying so.
- [ROADMAP.md](ROADMAP.md) — milestones v0.1 → v0.5, shipped in order. Each has a
  "Done when" line that serves as its acceptance test.
- [docs/lld.md](docs/lld.md) — the low-level design: packages, the interfaces between
  them, and one question traced function by function. Start here before reading code.
- [docs/coding-notes/](docs/coding-notes/) — plain-English explainers of the code.

## Status

Pre-alpha, **v0.1**: `merud` and `meru` answer questions with local models, stream
the answer, keep JSONL session transcripts and route each question with the
one-token router. Work goes milestone by milestone ([ROADMAP.md](ROADMAP.md)).
Don't build v0.2+ features (store, MCP, A2A, memory, skills, scheduler) ahead of
the milestone that owns them. [docs/running.md](docs/running.md) shows how to build
and run Meru.

Decided (details in ARCHITECTURE.md):

- **Agent loop:** hand-written. No agent framework.
- **Models:** Ollama on loopback. `lite` profile by default (MiniCPM5-2B +
  `nomic-embed-text`); `full` profile uses `qwen3.8:27b` + `qwen3-embedding:0.6b`.
- **Storage:** JSONL session transcripts are the source of truth; SQLite via
  `ncruces/go-sqlite3` + `sqlite-vec` (no cgo) is the rebuildable index.
- **Memory:** one Markdown file per memory under `~/.meru/memory/<kind>/`, indexed
  into SQLite; session summaries in the transcripts serve as episodic memory.
- **Protocols:** MCP client for tools, A2A client for other agents.
- **No sandbox.** Meru runs as an ordinary user process. Control sits at the tool
  and agent allowlists.
- **Observability:** OpenTelemetry metrics and traces, exported to loopback only.

## Design philosophy: simple wins every time

When two designs both work, pick the one with fewer moving parts, even if it is slower
or less general. Add complexity only when a measurement or a real second use case
demands it, and say which one in the PR. In practice:

- A function beats a type. A struct beats an interface. An interface appears when a
  second implementation exists, not before (`Engine` is the planned exception).
- One way to do each thing. One `dispatch` path, one store, one config file.
- No speculative flags, options, plugin points or "for later" code.
- Boring, well-known libraries over clever ones; the standard library over both.
- If a design needs a long explanation, look for a simpler design first.

## Writing

**All prose in this repo follows the `writing` skill** (`.claude/skills/writing/`):
docs, coding notes, code comments, commit messages, PR descriptions, issue text, HTML
pages and posters. Load the skill before drafting and run its revision pass before
committing. In short: short words, active voice with a named actor, no `-ly` padding,
no stock phrases, acronyms spelled out on first use, and none of the sentence-shape
tells it lists.

The skills `writing`, `explainer` and `poster-making` come from the owner's
`my-ai-assets` repo; don't rewrite them here. Meru also ships copies of `writing` and
`explainer` as built-in skills under `internal/skills/builtin/`; `poster-making` is a
repo tool only and doesn't ship. Update the built-ins by copying from `my-ai-assets`,
never by editing them in place.

## Non-negotiables

1. **No cloud-model code path.** The code must not contain one at all, whether behind
   a flag or switched off. If you find yourself adding an HTTP client for a model
   provider, stop and raise it instead. The only model HTTP traffic goes to Ollama on
   loopback.
2. **No telemetry leaves the machine, and no update checks or crash reports, ever.**
   Meru measures itself with OpenTelemetry, but exports only to a loopback endpoint
   the user runs. `merud` must refuse a non-loopback OTLP endpoint, and prompt and
   response text stay out of spans unless `capture_content = true`.
3. **Deny-by-default for tools and agents.** New MCP servers and A2A agents contribute
   nothing until config allowlists them. `merud` itself connects only to loopback,
   except to A2A agents and Streamable HTTP MCP servers marked `network = true`.
4. **Every tool call goes through `dispatch`**, which logs it to `tool_calls` and the
   session transcript. That covers MCP tools, A2A agents and built-in tools such as
   `remember`. Never add a second path.

## Shape

- `merud` is the resident daemon: keeps models warm, owns the store, the MCP and A2A
  clients, the scheduler and the agent loop.
- `meru` is a thin client over `~/.meru/merud.sock`. No model or store logic in the
  CLI, and no imports from engine or store packages.
- The `Engine` interface has four methods: `Generate`, `Stream`, `Embed`, `Info`.
  Resist growing it.
- Model names live in `config.toml`, never in code.
- Files are the source of truth. Anything in `~/.meru/meru.db` must be rebuildable from
  files and config.
- Instrument new stages with OTel. Use GenAI/MCP semantic-convention names where they
  exist, `meru.*` otherwise. Metric attributes must be bounded sets (model, tier,
  server, tool, outcome); never IDs, paths or text.

## Layout

The repo as it stands. Each package has a `doc.go` and a note in
`docs/coding-notes/`. Add a package only when its milestone needs it.

```text
cmd/
  merud/             the daemon: config, engine, router, socket, agent loop
  meru/              the thin client: one question, `meru chat`, `meru ping`
  fakeollama/        a fake Ollama server for end-to-end tests
internal/
  config/            config.toml: defaults, profiles, validation
  engine/            the Engine interface and OllamaEngine (chat, stream, embed, info)
  router/            the one-token route classifier (docs/fast-router.md)
  agent/             one turn: route, build the prompt, stream the answer
  transcript/        append-only JSONL session files
  rpc/               newline-delimited JSON over the Unix socket: client and server
  obs/               OpenTelemetry metrics and traces, loopback only
  loopback/          the one rule for "this address is on this machine"
  tui/               the Bubble Tea UI behind `meru chat`
  policy/            tests that enforce the non-negotiables and the thin client
  testutil/fakeollama/  the fake Ollama used by unit and e2e tests
test/e2e/            end-to-end tests: real binaries against the fake Ollama
deploy/              launchd and systemd files, the local Grafana stack, dashboards
docs/                architecture levels, coding notes, CI, running guide, posters
.github/             CI, security scans, Dependabot
config.example.toml  every config key with its default
Makefile             `make check` runs everything CI runs
```

**Dependency rule.** `cmd/meru` stays thin: it may import `rpc`, `config`, `tui`
and `loopback`, never `engine`, `transcript`, `agent` or anything that talks to a
model or stores data. `internal/policy` fails the build if that changes, directly
or through another package. `loopback` imports only the standard library, so any
package can use it.

Everything lives under `internal/`, because Meru is an app and no other module should
import it.

## Go practices

**Tooling.** One module at the repo root, with the Go version pinned in `go.mod`.
`gofmt` and `go vet` must be clean. Run `staticcheck` and `govulncheck` before a PR
when installed. Keep `go.mod` tidy (`go mod tidy`) and commit `go.sum`.

**Packages and names.**

- Name a package for what it provides: `engine`, `store`. Never `util`, `common`,
  `helpers`.
- Avoid stutter: prefer `engine.New` to `engine.NewEngine` when the short name stays
  clear.
- `MixedCaps`, no underscores. Acronyms keep their case: `ID`, `URL`, `HTTP`.
- Short names for short scopes (`i`, `err`, `ctx`); descriptive names for exported
  identifiers and long-lived variables.

**Errors.**

- Return `error` as the last result and check it on the next line.
- Add context when passing an error up: `fmt.Errorf("open store %s: %w", path, err)`.
- Compare with `errors.Is` / `errors.As`, never by string.
- Handle an error once: either log it or return it, not both.
- No `panic` for expected failures. Panic only for programmer mistakes during startup.

**Context and concurrency.**

- `ctx context.Context` is the first parameter of anything that does I/O, calls a
  model or calls a tool. Never store it in a struct.
- Every goroutine has an owner who can stop it and wait for it. Prefer
  `errgroup.Group` over a hand-rolled `sync.WaitGroup` plus error channels.
- The sender closes a channel, never the receiver. Protect shared state with a
  `sync.Mutex` declared next to the fields it guards.
- Don't start goroutines "for speed" without a measurement.

**Types and interfaces.**

- Accept interfaces, return concrete types. Define an interface in the package that
  uses it, with only the methods it calls.
- Make the zero value useful where you can; use `New…` constructors when setup is
  needed.
- No generics unless they remove real duplication. No reflection. No `init()` side
  effects. No package-level mutable state.

**Resources.** Put `defer x.Close()` right after the successful open. For files you
write, check the error from `Close`.

**Portability.** Build paths with `filepath`, find the home directory with
`os.UserHomeDir`, and never hard-code `/` separators or `~`. Put platform-specific
code behind build tags (`_darwin.go`, `_linux.go`, `_windows.go`) in as few files as
possible. `GOOS=linux go build ./...` and `GOOS=windows go build ./...` must succeed.

**Config.** Parse `config.toml` once at startup into typed structs, validate
everything (including "is this address loopback?"), and fail fast with a clear message.

**Logging.** `log/slog` with key-value pairs, to a local file. Never log prompt text
or tool results at info level.

**Tests.**

- Standard `testing` package only; no assertion libraries.
- Table-driven tests with `t.Run` subtests. `t.TempDir()` for files.
- Fake Ollama and MCP servers with `httptest` or in-process stubs; unit tests never
  need a real model.
- Fixtures and golden files go in `testdata/`.
- `go test -race ./...` must pass.

**Dependencies.** Standard library first. Each new module needs a reason in the PR
description. Expected ones: `modelcontextprotocol/go-sdk`, the A2A Go SDK,
`ncruces/go-sqlite3` + `sqlite-vec-go-bindings`, OpenTelemetry Go, a TOML parser,
`golang.org/x/sync`, and Bubble Tea with Bubbles for the `meru chat` terminal UI.

## Writing comments

Comments are for a reader who knows programming but not Go. They should be able to
read a file top to bottom and understand what it does and why.

- **Every package** has a `doc.go` with a package comment: what the package is for,
  where it sits in ARCHITECTURE.md, and what it chooses not to do.
- **Every file** opens with a short comment on what lives in it.
- **Every function and type**, exported or not, gets a doc comment starting with its
  name: what it does, what it returns, and when it fails.
- **Explain Go features the first time they appear in a file**, in one line: `:=`,
  multiple return values, `if err != nil`, pointer vs value receivers, `defer`,
  goroutines, channels, `select`, struct tags, embedding, `iota`, generics.
  Example: `// defer runs f.Close() when this function returns, even on an error.`
- **Explain why**, not only what: why this approach, why this limit, why this order.
  Link the ARCHITECTURE.md section when a design rule drives the code.
- Plain English, short sentences. Follow the `writing` skill. Don't narrate trivial
  lines like `i++`.

## Explaining the code

`docs/coding-notes/` holds plain-English explainers for someone new to Go. Rules:

- **Any PR that adds or changes code updates the matching note in the same PR.** A PR
  without its note doesn't merge.
- One note per package (`engine.md`, `agent.md`, …), plus short notes in
  `docs/coding-notes/go-basics/` for Go concepts, written the first time the code uses
  them (`errors.md`, `context.md`, `goroutines.md`, …).
- Follow the template in [docs/coding-notes/README.md](docs/coding-notes/README.md),
  and add each new note to its index.

## Skills in this repo

In `.claude/skills/`:

- **`writing`** — for all prose: docs, coding notes, comments, PR descriptions.
- **`new-feature-design`** — design a feature before coding it. Writes an issue, a
  low-level design, a review and a test plan to `.scratchpad/`.
- **`pr-review`** — multi-persona review of a PR, including a Go-mentor check that
  the comments and coding notes explain the change.
- **`explainer`** — build a self-contained HTML explainer. Used for the pages in
  `docs/architecture/`; update them when the Markdown they mirror changes.
- **`poster-making`** — make a one-page poster. Output goes in `docs/posters/`.

## Commands

```sh
make check            # everything CI runs, in order; run it before every PR
make test             # go test -race over every package
make e2e              # end-to-end tests: real binaries against the fake Ollama
make lint             # staticcheck
make vuln             # govulncheck
make sec              # gosec
make build            # binaries for five platforms in bin/
go run ./cmd/merud    # run the daemon from source
go run ./cmd/meru "..."
```

Tools run through `go run` at pinned versions, so nothing needs installing. The
integration tests that talk to a real local Ollama are opt-in:
`go test -tags integration ./...` and `go test -tags 'e2e integration' ./test/e2e/...`.
[docs/ci.md](docs/ci.md) explains each check.

## Attribution

Do not add AI or assistant attribution to commits, PRs, code comments, or docs.
