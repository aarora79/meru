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
  **Change ARCHITECTURE.md first**: its text is the contract. Its figures are
  drawn in `docs/architecture/300.html` and rendered to PNG, so a figure change
  starts in that page's SVG. Then carry the change into 200, 100 and their HTML
  pages in the same PR: `300.html` mirrors ARCHITECTURE.md, `200.html` mirrors
  200.md, and `100.html` mirrors 100.md.
  The figures in ARCHITECTURE.md, 100.md and 200.md are PNG files in
  `docs/architecture/img/`, drawn from the SVG in 300.html, 100.html and
  200.html. After you change a figure's SVG in any of the three pages, run
  `make figures` to redraw them.
  If code and this doc disagree, one of them has a bug. Say which one; don't pick
  without saying so.
- [ROADMAP.md](ROADMAP.md) — milestones v0.1 → v0.5, shipped in order. Each has a
  "Done when" line that serves as its acceptance test.
- [docs/lld.md](docs/lld.md) — the low-level design: packages, the interfaces between
  them, and one question traced function by function. Start here before reading code.
- [docs/coding-notes/](docs/coding-notes/) — plain-English explainers of the code.

## Status

Pre-alpha, with releases up to **v0.4.4**. ROADMAP.md ticks every item from v0.1
to v0.4:

- **v0.1:** `merud` and `meru` answer with local models, stream the answer, keep
  JSONL session transcripts and route each question with the one-token router.
- **v0.2:** the SQLite store, the folder indexer with its watcher, hybrid
  retrieval, and answers that cite their sources.
- **v0.3:** tools, each call through `dispatch`: MCP servers, A2A agents,
  `[[commands]]` entries, `web_search` and `web_fetch` through a SearXNG the user
  runs, approvals in both clients, the `tool_calls` log, the server catalog and
  secrets in `~/.meru/secrets.toml`.
- **v0.4:** memories, with your profile in every prompt and recall on each turn,
  session summaries, skills with progressive disclosure, `write_file` into
  `~/meru-output/`, and a context budget for the prompt.

The desktop app, `meru-desktop` (Wails v3, macOS first), came ahead of v0.5 at the
owner's request. It has the chat screen, Settings and the Setup screen, takes
files by the attach button or drag and drop, and asks `merud` to make every
change. `meru chat` does the same things in a terminal. The headless mode,
`meru run --json`, also came ahead of v0.5 (#52): it lets a script drive `merud`
as an agent harness, one JSON event per line. Outside the milestones,
Meru switches between named model sets at run time, searches the web first when a
question needs it, warms the answer model in the background, and ships
through `make release`, a one-line installer, and a Mac installer on a disk image
(`cmd/meru-installer`), which sets up Ollama, web search, folders and `merud` in
a window. Work goes milestone by milestone
([ROADMAP.md](ROADMAP.md)). Don't build a later milestone's features (the
scheduler) ahead of the milestone that owns them.
[docs/running.md](docs/running.md) shows how to build and run Meru.

Decided (details in ARCHITECTURE.md):

- **Agent loop:** hand-written. No agent framework.
- **Models:** Ollama on loopback. `lite` profile by default (MiniCPM5-2B +
  `nomic-embed-text`); `full` profile uses `qwen3.6:35b-a3b-mxfp8` + `qwen3-embedding:0.6b`.
- **Storage:** JSONL session transcripts are the source of truth; SQLite via
  `ncruces/go-sqlite3` (no cgo) is the rebuildable index. Vectors sit in a plain
  table and vec1, bundled with the driver, supplies the distance function.
- **Indexing:** only the folders in `[index] folders`, nothing by default. The
  indexer skips secrets, hidden files, build folders and ignored files, and never
  follows a symlink.
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
no stale phrases, acronyms spelled out on first use, and none of the sentence-shape
tells it lists.

Examples come from the catalog servers (`google` for mail and calendar, `obsidian`
for notes) and the built-in `web_search`, and the repository carries no trading or
personal-finance examples.

The skills `writing`, `explainer` and `poster-making` come from the owner's
`my-ai-assets` repo; don't rewrite them here. Meru also ships copies of `writing` and
`explainer` as built-in skills under `internal/skills/builtin/`; `poster-making` is a
repo tool only and doesn't ship. Update those two built-ins by copying from
`my-ai-assets`, never by editing them in place. The other two built-ins,
`web-research` and `file-research`, are Meru's own; their only copies live in
`internal/skills/builtin/`.

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
   except to A2A agents and Streamable HTTP MCP servers marked `remote = true`, and
   to public web pages through `web_fetch`, which is on by default and fetches only
   when the model asks; taking it out of `[builtin] tools` turns it off. That tool refuses
   loopback and private addresses at connect time, and asks the user before it
   fetches a URL that no search result or question of the user's gave in the same
   session, and before any download.
4. **Every tool call goes through `dispatch`**, which logs it to `tool_calls` and the
   session transcript. That covers MCP tools, A2A agents, local commands and
   built-in tools such as `configure` and `remember`. Never add a second path.
5. **Nothing for sale, anywhere.** Meru comes under the Apache License 2.0 and
   offers nothing for sale: no offer of a paid licence, no pricing, no invitation
   to buy, no sponsor link. That holds for the README, `CONTRIBUTING.md`, the
   desktop app's About panel, the landing page, the posters and issue templates.

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

The repo as it stands. Each `internal/` package has a `doc.go`; each command puts
its package comment at the top of `main.go` instead. Each package has a note in
`docs/coding-notes/`, with a few sharing one: `merud.md` covers `cmd/merud`,
`testing.md` covers `policy`, `testutil` and `cmd/fakeollama`, `e2e.md` covers
`test/e2e` and `cmd/fakemcp`, and `engine.md` covers `loopback`. Add a package
only when its milestone needs it.

```text
AGENTS.md            this guide; CLAUDE.md only points here
ARCHITECTURE.md      the design contract, level 300
ROADMAP.md           milestones and their "Done when" lines
README.md            what Meru is, how to try it, the status line
CONTRIBUTING.md      how to contribute, and the contributor agreement
LICENSE              Apache License 2.0
NOTICE               copyright line and the personal-project disclaimer
config.example.toml  every config key with its default; a copy of internal/config/template.toml
Makefile             `make check` runs everything CI runs
go.mod, go.sum       one module, github.com/aarora79/meru

cmd/
  merud/             the daemon: main.go wires config, engine, router, store, indexer, tools,
                     socket and agent loop; one file per group of socket ops (history, memory,
                     skills, tools, connections, folders, models, save, index); sessions.go
                     replays transcripts and runs the summarizer;
                     backends.go joins the MCP pool to dispatch; runtime.go checks Ollama and
                     warms the models; machine.go describes the computer for the system prompt
  meru/              the thin client, one file per subcommand: one question and `chat`
                     (main.go), `ping`, `index [-status]`, `tools`, `log`, `usage`, `setup`,
                     `setup user` (user.go), `config template`, `memory list|add|forget`,
                     `skills list|show|reset`, `mcp list|status|add|remove` (mcp.go, probe.go),
                     `check` and `check report` (check.go, checkfile.go, checkreport.go), `run --json`
                     (run.go), the approval prompt (approve.go) and the terminal styles (look.go)
  meru-desktop/      the desktop app's window (Wails v3, build tag `desktop`, needs cgo), with
                     Info.plist and Meru.icns for Meru.app
  meru-installer/    the Mac installer's window, "Install Meru.app" (Wails v3, build tag
                     `desktop`), with its Info.plist
  fakeollama/        a fake Ollama server for end-to-end tests
  fakemcp/           a small MCP server over stdio for end-to-end tests
internal/
  config/            config.toml: defaults, profiles, validation; template.toml is the source of
                     config.example.toml and `meru config template`
  engine/            the Engine interface and OllamaEngine (chat, stream, embed, info)
  router/            the one-token route classifier (docs/fast-router.md); labelled questions in testdata/
  store/             meru.db: documents, chunks, vectors, the keyword index, memories, messages
                     and session turns, tool_calls
  retrieve/          hybrid search over files, memories and past sessions: vector + keyword,
                     merged by reciprocal-rank fusion
  index/             reads [index] folders and the memory folder into the store: skip rules,
                     .gitignore, Markdown, HTML, PDF and code, chunking, watching
  dispatch/          the one path for every tool call: allowlist, approval, audit, metrics
  mcp/               the MCP client pool: stdio and Streamable HTTP, allowlists, the probe
                     `meru mcp add` runs before it saves a server
  a2a/               the A2A client: agent cards, skills as tools, streaming calls
  builtin/           the eleven tools inside merud: `configure`, `datetime`, `about_meru`,
                     `remember`, `write_file`, the read-only `read_file`, `list_folder`,
                     `grep` and `search_files`, and `web_search` and `web_fetch` (web.go,
                     webguard.go, webdownload.go); chats.go lets the first three read
                     the past chats in ~/.meru/sessions
  commands/          the [[commands]] entries: local programs run with no shell, typed parameters
  catalog/           the starter MCP servers and SearXNG, the safe append to config.toml, and
                     one-list edits and removals in it
  secrets/           ~/.meru/secrets.toml: secret:<name> references and redaction
  skills/            loads SKILL.md folders; builtin/ ships writing, explainer, web-research and
                     file-research
  memory/            one Markdown file per memory under memory/<kind>/; the profile kinds go in every prompt
  summarize/         session summaries: the fast model summarizes quiet sessions, for recall by episode
  agent/             one turn: route, build the prompt within the budget (budget.go), pick a
                     skill, recall memories and earlier chats, run tool rounds, stream the answer
  transcript/        append-only JSONL session files, and listing them
  rpc/               newline-delimited JSON over the Unix socket: client, server, and the types
                     of every op and event
  obs/               OpenTelemetry metrics and traces, loopback only, and the slog handler
  loopback/          the one rule for "this address is on this machine"
  tui/               the Bubble Tea UI behind `meru chat`, with the desktop app's features as
                     slash commands and boxes
  about/             the tagline, the version and the project's links, for both clients
  desktop/           the desktop app minus the window: the Bridge, its views, and the page in
                     web/ (index.html, app.css, js/, vendored marked and DOMPurify in vendor/,
                     fonts/)
  opener/            opens a clicked http, https or file link with the system opener, no shell
  installer/         the Mac installer minus the window: its nine steps, the Bridge, the
                     allowlist of programs it runs (run.go), and the page in web/
  policy/            tests that enforce the non-negotiables and the thin client; deny-lists in
                     testdata/, allowed URLs in allowed_urls.txt
  testutil/fakeollama/  the fake Ollama used by unit and e2e tests
test/e2e/            end-to-end tests: real binaries against the fake Ollama and fake MCP
deploy/              launchd/ and systemd/ service files; observability/ holds the local
                     Grafana stack (compose.yaml), its provisioning and the dashboards
scripts/             release.sh, which `make release` runs; install.sh, the one-line
                     installer; dmg-readme.txt, the read-me on the installer's disk image;
                     bench.sh, which `make bench` runs
dist/                git-ignored; `make release` packs a release here
bench/               git-ignored; the private benchmark: tasks.jsonl, results/ and home/
docs/
  architecture/      100.md, 200.md and the HTML pages 100.html, 200.html and 300.html;
                     index.html sends old links to 100.html; img/ holds the figures
                     as PNG files and render.sh, which draws them
  coding-notes/      one note per package, and go-basics/ for Go concepts
  lld.md             the low-level design
  running.md         how to build and run Meru
  faq/               one page per "how do I…" question, listed in index.md
  benchmarks/        the benchmark: README.md (how it works, how to build your own dataset) and
                     results.md, which `make bench-report` writes; the dataset stays in bench/
  observability.md   the local Grafana stack: setup, dashboards, command-line queries
  ci.md              what each check in CI does
  fast-router.md     how the one-token router works
  google-setup.md    setting up Gmail, Calendar and Drive for the `google` server
  releasing.md       how the owner builds and publishes a release
  release-notes/     one file of release notes per version, from v0.4.7; the release-notes
                     skill writes them and `make release` publishes each as the release's text
  examples/          a starter check file for `meru check`, and vault-digest.sh, a second
                     agent that drives `meru run --json`
  img/               the logo and the social preview image
  posters/           the one-page poster: HTML, PNGs and PDF
  index.html         the landing page GitHub Pages serves at aarora79.github.io/meru
.claude/skills/      writing, explainer, poster-making, new-feature-design, pr-review,
                     meru-install, release-notes
.github/             CI and security workflows, Dependabot, the pull-request template
.scratchpad/         git-ignored; the design and review skills write here
```

**Dependency rule.** `cmd/meru` stays thin: it may import `rpc`, `config`, `tui`,
`about` and `loopback`, plus `catalog` and `secrets`, which `meru setup` and `meru mcp add`
use to write `config.toml` and `secrets.toml`. It never imports `engine`,
`transcript`, `agent`, `store`, `retrieve`, `index`, `memory`, `summarize`, `mcp`,
`dispatch`, `a2a`, `builtin`, `commands` or anything else that talks to a model, stores data
or runs a program.
The desktop app (`cmd/meru-desktop` and `internal/desktop`) is thinner still: it
may import `rpc`, `config`, `loopback`, `opener` and `about`, plus Wails in the command,
and neither `catalog`, `secrets` nor `tui`; it never touches Wails' updater.
The Mac installer (`cmd/meru-installer` and `internal/installer`) runs before
`merud` exists, so it may write config through `catalog`, the profile through
`memory` and ask `merud` over `rpc`; it never imports `engine`, `agent`, `store`,
`index`, `dispatch`, `mcp`, `a2a`, `builtin` or `commands`. It starts programs
only in `internal/installer/run.go`, from a fixed allowlist of absolute paths,
with no shell.
`internal/policy` fails the build if any of this changes, directly or through
another package. `loopback` and `about` import only the standard library, so any package
can use them.

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
description. Expected ones: `modelcontextprotocol/go-sdk`, the A2A Go SDK
(`a2aproject/a2a-go/v2`),
`ncruces/go-sqlite3` (with its bundled vec1 and FTS5), OpenTelemetry Go, a TOML
parser, `golang.org/x/sync` (for `errgroup`, which runs `merud`'s server, startup
scan and watcher side by side), Bubble Tea with Bubbles for the `meru chat`
terminal UI, with Glamour to render an answer's Markdown, goldmark to find its
links and code blocks, and Lip Gloss, termenv and `charmbracelet/x/ansi` for
styles and widths, `golang.org/x/term` so `meru setup` reads an API key without echo,
and for the indexer `fsnotify`, `golang.org/x/net/html` and `ledongthuc/pdf`.
The desktop app adds Wails v3 (`wailsapp/wails/v3`, pinned to a beta), and its
page vendors marked, DOMPurify and its fonts as files, with no Node or npm.

## Writing comments

Comments are for a reader who knows programming but not Go. They should be able to
read a file top to bottom and understand what it does and why.

- **Every package** has a package comment: what the package is for, where it sits
  in ARCHITECTURE.md, and what it chooses not to do. An `internal/` package keeps
  it in `doc.go`; a command keeps it at the top of `main.go`.
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
- **`meru-install`** — walk a user through installing, updating or removing Meru
  on a Mac from a GitHub release, asking before each change.
- **`release-notes`** — write `docs/release-notes/vX.Y.Z.md` for a new version
  and open its pull request; `make release` then publishes it. Adapted from the
  skill of the same name in agentic-community/mcp-gateway-registry.

## Commands

```sh
make check            # everything CI runs, in order; run it before every PR
make test             # go test -race over every package
make e2e              # end-to-end tests: real binaries against the fake Ollama
make lint             # staticcheck
make vuln             # govulncheck
make sec              # gosec
make secrets          # scan the working tree for committed secrets
make fmt              # rewrite Go files in gofmt style
make cover            # tests with coverage, and the total
make build            # binaries for five platforms in bin/
make desktop          # the desktop app for this machine (cgo); make desktop-check vets it
make desktop-app      # wrap the desktop app in bin/Meru.app (macOS)
make installer-app    # the Mac installer, with its payload, in "bin/Install Meru.app"
make dmg              # pack the installer in dist/Meru-dev-macos-arm64.dmg (Apple silicon)
make release VERSION=v0.4.1 DRY_RUN=1  # build and pack a release in dist/; drop DRY_RUN to publish
make router-eval      # score the router on labelled questions against local Ollama
make bench            # the private benchmark in bench/, each model set; make bench-report writes the page
make pick-eval        # score the skill pick on labelled questions against local Ollama
make figures          # redraw the figures in ARCHITECTURE.md, 100.md and 200.md from the HTML (needs Chrome)
go run ./cmd/merud    # run the daemon from source
go run ./cmd/meru "..."
```

Tools run through `go run` at pinned versions, so nothing needs installing. The
integration tests that talk to a real local Ollama are opt-in:
`go test -tags integration ./...` and `go test -tags 'e2e integration' ./test/e2e/...`.
[docs/ci.md](docs/ci.md) explains each check.

## Attribution

Do not add AI or assistant attribution to commits, PRs, code comments, or docs.
