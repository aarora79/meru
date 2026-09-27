<p align="center"><img src="docs/img/meru-social-preview.png" width="720" alt="Meru मेरु: a personal AI assistant that runs on a machine you control. Local models only."></p>

# Meru

**A personal AI assistant that runs entirely on your own machine.**

Meru runs local models only. Its code has no path to a cloud AI model, not even a
disabled one. It loads open-weight models into your computer's memory and keeps them
there, so a question needs no API key and costs only electricity.

Two kinds of program can reach beyond your machine, and only once you add them to
config: MCP (Model Context Protocol) servers, such as Gmail and Calendar or your
Obsidian notes, and other agents over A2A (Agent2Agent). Meru allows none of their
tools until you name them, and logs every call. Web search goes through SearXNG, a
search engine you run on your machine, so only the search words leave it. When the
model needs what a page says, `merud` fetches that public page itself, and asks you
first for any address that no search or question of yours gave.

> **Status: pre-alpha, v0.3.** `merud` and `meru` answer questions with local models,
> stream the answer, keep session transcripts and pick a route with the one-token
> router. `merud` indexes the folders you list, searches them by keyword and by
> meaning, and the answer cites the files it used. v0.3 adds tools: MCP servers and
> A2A agents you allow, one `dispatch` path that asks you before risky calls and logs
> every call, `meru setup`, and a catalog of starter servers. In the v0.3 acceptance
> test, Meru on the 2B `lite` model answered a question from an Obsidian vault by
> calling the vault's own MCP server twice over three rounds, in 8.3 s, and
> `meru log` showed both calls. Every v0.4 item has landed too: Meru keeps what you
> tell it about yourself as memory files, recalls them and past conversations when
> a question needs them, and loads skills when a turn calls for one. Scheduled jobs
> come in v0.5. [ROADMAP.md](ROADMAP.md) lists the milestones in order.

---

## Install on a Mac

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
```

The script downloads the latest release and checks it against `SHA256SUMS`. It
puts `meru` and `merud` in `~/.local/bin` and Meru.app in `/Applications`, then
offers Ollama, the models, `meru setup` and starting `merud` at login, asking
before each step. [docs/running.md](docs/running.md) covers Linux, Windows and
building from source.

## What it is

Meru is two Go programs. `merud` is a daemon: it runs in the background, keeps the
models loaded, owns your index and memory, talks to MCP servers and runs scheduled
jobs. `meru` is the command-line client; it connects to the daemon over a local
socket and starts in milliseconds. Go builds each program into one file that runs on
macOS, Linux and Windows ([why Go](ARCHITECTURE.md#why-go)).

Working now:

```
$ meru setup                               # Ollama, models, folders, web search, MCP servers
$ meru "what is the capital of France?"   # one question, answer streamed as text
$ meru "when do I sow the tomatoes?"      # searches your folders, then lists Sources:
$ meru "search my obsidian vault for AI"  # calls the tools you allowed; asks first when config says so
$ meru "search the web for the latest Go release"  # web_search, through the SearXNG you run
$ meru chat                                # interactive terminal UI
$ meru ping                                # is merud running?
$ meru index                               # rescan the folders under [index] folders
$ meru index ~/notes/work                  # rescan one folder or file inside them
$ meru index -status                       # what the index holds
$ meru mcp                                 # each MCP server: connected or not, and its tool counts
$ meru mcp list                            # the server catalog, and your servers with their state
$ meru mcp add obsidian                    # add a catalog server: do it for me, or show me how
$ meru mcp add google                      # a server you run; Meru prints the command that starts it
$ meru mcp add stdio notes -- npx -y some-mcp  # any other server: Meru tries it and proposes its tools
$ meru mcp remove notes                    # take a server out of config.toml
$ meru tools                               # each server, its allowed tools, which ask first
$ meru log -n 20 -v                        # the latest tool calls, with results
$ meru check --save                        # rerun your own questions from ~/.meru/checks.jsonl, grade them
$ meru usage                               # sessions and questions, from the last hour to all time
$ meru config template                     # every config key with its default, as setup writes it
$ meru setup user                          # a few questions about you, saved as memories
$ meru memory list                         # what it knows about you, in plain text
$ meru memory forget <id>                  # delete one memory file
$ meru skills list                         # what it knows how to do
$ meru skills reset writing                # put a built-in skill back as shipped
```

`meru setup` writes `~/.meru/config.toml` from the config template: every key,
with the defaults uncommented and what is off, such as the catalog's MCP
servers, in comments ready to uncomment. `[builtin] tools` lists the built-in
tools the model may use and `[skills] disabled` the skills it skips. `merud`
reads the file when it starts, so restart it after a change. API keys go in
`~/.meru/secrets.toml`, never in config. When a tool asks first, `meru` prompts
`[o]nce [s]ession [d]eny` on the terminal, and denies when it runs in a script or
a pipe. Quote a question that starts with the word `ping`, `chat`, `index`,
`tools`, `log`, `usage`, `setup`, `config`, `memory`, `skills`, `mcp` or `check`,
or `meru` may read that word as a command.
[docs/running.md](docs/running.md#7-index-your-files) shows the setup and the
`Sources:` output.

Planned for v0.5:

```
$ meru brief                       # today's digest, prepared in advance
```

## Quick start

You need [Go](https://go.dev/dl/) 1.26 or later and [Ollama](https://ollama.com)
0.12.11 or later, running on this machine.

```
$ ollama pull hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M   # the lite profile's chat model
$ ollama pull nomic-embed-text                         # the lite profile's embedding model
$ git clone https://github.com/aarora79/meru.git && cd meru
$ go install ./cmd/merud ./cmd/meru                    # into ~/go/bin; silent on success
$ merud &                                              # loads the models and listens
$ meru "what is the capital of France?"
$ meru chat                                            # a conversation in the terminal
```

To update to the latest code, rebuild both programs and restart `merud`, so the
daemon and the client match. `-v` writes a line for each stage of every question
to `~/.meru/merud.log`:

```
$ git pull
$ go install ./cmd/merud ./cmd/meru
$ pkill merud; merud -v &
$ meru chat
```

`go install` prints nothing when it works. Add `-v` to list each package as it
compiles, or `-a -x` to rebuild everything and print each command.

On a Mac, `make desktop && ./bin/meru-desktop` builds and opens the desktop app, a
window onto the same `merud`. It needs the Xcode command line tools.

To skip Go, install a release instead: each one on GitHub holds `meru` and `merud`
for macOS, Linux and Windows, and `Meru.app` for Apple silicon.
[Install from a release](docs/running.md#install-from-a-release) shows how, and
in Claude Code the `meru-install` skill walks you through it.

[docs/running.md](docs/running.md) is the full guide: settings, the `full` profile,
`meru chat`, running `merud` as a service, the local dashboard, troubleshooting and
uninstalling.

## Development

`make check` runs everything CI runs: formatting, `go vet`, staticcheck, the race
detector over every test, builds for five platforms, govulncheck, gosec, gitleaks
and actionlint. `make e2e` runs the end-to-end tests against a fake Ollama.
[docs/ci.md](docs/ci.md) explains each check, and [AGENTS.md](AGENTS.md) holds the
rules for changing the code.

## What it does

| Capability | What it means |
| --- | --- |
| **Local models** | Ollama runs the models on your machine, and `merud` keeps them loaded. |
| **Search over your files** | Meru searches your notes, docs, PDFs and repos by keyword (BM25) and by meaning, and names the file behind each answer. |
| **Web search** | The built-in `web_search` tool searches through SearXNG, which you run in Docker; no account or API key. The built-in `web_fetch` tool reads a whole public page, answers a question from it with the fast model, or downloads a file; it asks you before it opens an address no search or question of yours gave. Taking it out of `[builtin] tools` turns it off. [docs/running.md](docs/running.md#web-search) shows the setup. |
| **MCP tools** | `meru setup` offers two servers: `google` for Gmail, Calendar and Drive, and `obsidian` for notes. It adds each one for you or shows you what to paste, and `meru mcp` shows which ones are connected. |
| **Other agents** | Meru hands tasks to agents you have allowed, over A2A. |
| **Memory** | Meru saves what it learns about you as small Markdown files you can edit or delete. |
| **Skills** | A skill is a Markdown file of instructions that Meru loads when a question needs it. Meru ships with `writing`, `explainer`, `web-research` and `file-research`; `[skills] disabled` turns one off. |
| **Scheduled jobs** | Meru runs briefs and other jobs on a schedule, so it can tell you things before you ask. |
| **Observability** | Meru records the tokens, time and tool calls of every question as OpenTelemetry metrics and traces, and shows them in Grafana on your machine. |

## Why your own machine

Four reasons, most important first:

1. **Privacy you can check.** Meru reads your email, notes and calendar.
   On a machine you control, none of it ends up in anyone's training set.
2. **No cost per question.** Once you own the hardware, indexing ten years of notes
   or running a brief every morning adds nothing to any bill.
3. **It keeps working.** No deprecation notices, rate limits, outages or price
   changes. The model on your disk today still runs a year from now.
4. **You can inspect everything.** Your files are the source of truth, and the
   database is a copy Meru rebuilds from them: delete `meru.db` and Meru re-indexes.
   Memories and skills are Markdown files you can read, edit or delete.

Many assistants offer local models as one option next to cloud ones. Meru supports
local models only.

## Why an enterprise would care

I built Meru for myself, and its design fits a common company problem: staff want
an assistant, and the data they would use it on can't leave the building.

- **The data stays put.** Meru runs on a laptop, a virtual desktop or a server in
  your own account, and reads email, documents, code and notes without sending any of
  it to a model provider. It can work on material that policy or regulation keeps
  in-house.
- **Every action is logged.** Each server's tools stay off until you allow them,
  risky ones ask before they run, and every call goes into an audit table and the
  session transcript. One function, `dispatch`, does all of this, so a reviewer
  checks one code path.
- **You can measure it.** For every question, Meru sends OpenTelemetry metrics and
  traces (tokens, time, which tool ran and which failed) to a collector you run, so
  usage and failures show up on a dashboard.
- **It installs anywhere.** Meru ships as native binaries, with no interpreter to
  install. It runs on employee laptops, virtual desktops, servers on your own network
  and machines with no internet connection.
- **No per-question bill.** You pay for the hardware you already own, so indexing
  ten years of records or running a daily brief for every employee depends on how
  much hardware you have.

Meru serves one person on one machine by design. A company would also want central
control over who may use which tools, and a summary of what the agents did, without
collecting anyone's data. The design leaves room for that; none of it exists yet.

## Hardware

Meru runs wherever Ollama and Go do:

- **macOS on Apple silicon:** supported and tested. Developed on a Mac Studio, M4 Max,
  64 GB.
- **Linux:** supported, including home servers and cloud VMs such as EC2.
- **Windows 10 and later:** should work; not tested at first.

Two model profiles ship with it (see [ARCHITECTURE.md](ARCHITECTURE.md#model-tiers)):

- **`lite` (default):** MiniCPM5-2B and `nomic-embed-text`, about 2 GB of downloads.
  Needs 16 GB of RAM and runs on a CPU, faster with a GPU or Apple silicon.
- **`full`:** Qwen 3.8 27B and `qwen3-embedding:0.6b`. Needs Apple silicon with
  32 GB (64 GB is comfortable), or a GPU with about 24 GB of memory.

For the answer model we tried three: MiniCPM5-2B, `qwen3.6:35b`, the one we use
now, and `gemma3:12b`, which can't call tools. The desktop app's Library, Models
lists them with their `ollama pull` and `ollama run` commands and switches
between them without a restart. [docs/running.md](docs/running.md#models-we-tried-for-answers)
has what we measured.

On a cloud server, Meru still sends no prompt to a model provider, but your data
lives on that server. The promise is "a machine you control"; where it sits is your
call.

## Docs

- Architecture, in three levels ([web pages](https://aarora79.github.io/meru/architecture/100.html)):
  - [100: the big picture](docs/architecture/100.md)
  - [200: how it works](docs/architecture/200.md)
  - [300: the full design](ARCHITECTURE.md), the design contract
- [docs/posters/](docs/posters/) — the Meru poster
- [docs/running.md](docs/running.md) — install, run and troubleshoot Meru
- [docs/releasing.md](docs/releasing.md) — how a release gets built and published
- [docs/lld.md](docs/lld.md) — how the code fits together, for readers new to Go
- [ROADMAP.md](ROADMAP.md) — milestones, in shipping order
- [AGENTS.md](AGENTS.md) — repo rules for AI coding agents
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to report a problem or send a change, and the contributor agreement

## License

Meru is free software under the GNU Affero General Public License, version 3
(AGPL-3.0). [LICENSE](LICENSE) holds the full text. You may use, study, change and
share Meru. If you share a changed copy, or let people use a changed copy over a
network, you must offer them its source code under the same license.

A company that wants to ship Meru inside a closed product, or change it without
publishing the changes, can buy a commercial license instead. Ask through the
author's GitHub profile, [@aarora79](https://github.com/aarora79).

Copies of Meru taken before 26 September 2026 came under the MIT license, and they
keep it. Contributions need the agreement in [CONTRIBUTING.md](CONTRIBUTING.md).

## The name

*Meru* (मेरु) is the cosmic mountain that the sun, moon and stars turn around. This
assistant takes the name because it works the same way: it stays in one place, on
your machine, and your notes, tools and daily routine turn around it.
