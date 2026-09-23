# Meru

**A personal AI assistant that runs entirely on your own machine.**

You need no API key and pay for no tokens, and no prompt leaves your machine unless
you allow it. *Meru* (मेरु) loads open-weight models into your computer's memory and keeps
them there, so a question costs only electricity.

> Meru is the cosmic mountain that the sun, moon and stars turn around.
> This assistant takes the name because it works the same way: it stays in one
> place, on your machine, and your notes, tools and daily routine turn around it.

---

## What it is

Meru is two Go programs, shipped as native binaries. `merud` is a daemon: it runs in
the background, keeps the models loaded, owns your index and memory, talks to MCP
(Model Context Protocol) servers and runs scheduled jobs. `meru` is the command-line
client; it connects to the daemon over a local socket and starts in milliseconds.

```
$ meru setup                       # first run: models, folders, MCP servers
$ meru "what did I change in the portfolio repo this week?"
$ meru chat                        # interactive terminal UI
$ meru index ~/notes ~/repos       # build the local knowledge index
$ meru memory list                 # what it knows about you, in plain text
$ meru skills list                 # what it knows how to do
$ meru brief                       # today's digest, prepared in advance
```

## What it does

| Capability | What it means |
| --- | --- |
| **Local models** | Models run in Ollama on your machine, and the daemon keeps them loaded |
| **Search over your files** | Keyword (BM25) and meaning-based search over notes, docs, PDFs and repos, with citations |
| **MCP tools** | `meru setup` offers web search, Gmail, Calendar, Drive and more, and adds each one for you or shows you what to paste |
| **Other agents** | Meru hands tasks to agents you've allowed, over A2A (Agent2Agent) |
| **Memory** | Meru saves what it learns about you as small Markdown files you can edit or delete |
| **Skills** | Markdown files of instructions, loaded only when a question needs them. Ships with `writing`, `explainer` and `poster-making` |
| **Scheduled jobs** | Briefs and other jobs run on a schedule, so Meru can tell you things before you ask |
| **Observability** | OpenTelemetry metrics and traces for every question: tokens, time taken and tool calls, shown in a local Grafana |

## Why your own machine

Three reasons, most important first:

1. **Privacy you can check.** An assistant worth having reads your email, notes,
   finances and calendar. On a machine you control, that data can't end up in
   anyone's training set.
2. **No cost per question.** Indexing ten years of notes or running a brief every
   morning costs nothing once you own the hardware, which changes what you're
   willing to build.
3. **It keeps working.** No deprecation notices, rate limits, outages or price
   changes. The model on your disk in 2026 still runs in 2031.

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

On a cloud server, Meru still sends no prompt to a model provider, but your data
lives on that server. The promise is "a machine you control"; where it sits is your
call.

## Status

**Pre-alpha, design phase.** We wrote the architecture first, and the code follows it.
[ROADMAP.md](ROADMAP.md) lists what exists and what comes next.

## Docs

- Architecture, in three levels ([web pages](https://aarora79.github.io/meru/architecture/)):
  - [100: the big picture](docs/architecture/100.md)
  - [200: how it works](docs/architecture/200.md)
  - [300: the full design](ARCHITECTURE.md), the design contract
- [docs/posters/](docs/posters/) — the Meru poster
- [ROADMAP.md](ROADMAP.md) — milestones, in shipping order
- [AGENTS.md](AGENTS.md) — repo rules for AI coding agents

## License

MIT — see [LICENSE](LICENSE).
