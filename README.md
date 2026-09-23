# Meru

**A personal AI assistant that runs entirely on your own machine.**

No API keys. No tokens billed. No prompt leaves the device unless you explicitly
allow it to. Meru loads open-weight models into your Mac's unified memory and keeps
them there, so asking it something costs you electricity and nothing else.

> **Meru** (मेरु) — the cosmic mountain of Hindu, Buddhist and Jain cosmology: the
> axis around which the sun, moon and stars revolve. The fixed point you orient by.

---

## What it is

Meru is a resident daemon plus a thin CLI. The daemon holds the model in memory,
owns your index and your memory store, talks to MCP servers, and runs scheduled
work. The CLI is a socket client that starts in milliseconds.

```
$ meru "what did I change in the portfolio repo this week?"
$ meru chat                        # interactive TUI
$ meru index ~/notes ~/repos       # build the local knowledge index
$ meru memory list                 # what it knows about you, in plain text
$ meru skills list                 # what it knows how to do
$ meru brief                       # today's proactive digest
```

## What it does

| Capability | What it means |
|---|---|
| **Local inference** | MLX on Apple silicon, Ollama as fallback. Pluggable engine layer |
| **RAG over your files** | Hybrid retrieval (vector + BM25) over notes, docs, PDFs, repos — with citations |
| **MCP tool calling** | Meru is an MCP *client*. Point it at servers you already run and it inherits them |
| **Persistent memory** | Facts it learns about you, stored as readable rows you can inspect and edit |
| **Skills** | Markdown + frontmatter, progressively disclosed. Portable, greppable, yours |
| **Proactive daemon** | Scheduled jobs and briefs — it can surface things you didn't ask for |

## Why on-device

Three reasons, in order of how much they actually matter:

1. **Privacy is structural, not promised.** An assistant worth having reads your
   email, your notes, your finances, your calendar. On-device means that data
   physically cannot be someone else's training corpus.
2. **No marginal cost.** Indexing a decade of notes, re-running a brief every
   morning, letting a daemon think in the background — all free once the hardware
   is bought. That changes what you're willing to build.
3. **It keeps working.** No deprecation notices, no rate limits, no outage, no
   pricing change. The model on your disk in 2026 still runs in 2031.

## Hardware

Developed on a **Mac Studio, M4 Max, 64 GB**. That number drives the model choices —
see [ARCHITECTURE.md](ARCHITECTURE.md#model-tiers). The short version: at 64 GB,
**Mixture-of-Experts models are the unlock**, not larger dense ones. A ~30B MoE with
~3B active parameters gives you near-70B quality at near-8B speed for ~17 GB.

Meru should run on any Apple silicon Mac with 32 GB or more; smaller machines work
with smaller model tiers. Linux + CUDA is not supported today but the engine layer
is deliberately shaped to allow it.

## Status

**Pre-alpha. Design phase.** The architecture is written down; the code is being
built against it. See [ROADMAP.md](ROADMAP.md) for what exists and what's next.

## Docs

- [ARCHITECTURE.md](ARCHITECTURE.md) — how it's put together and why
- [ROADMAP.md](ROADMAP.md) — milestones, in shipping order
- [CLAUDE.md](CLAUDE.md) — repo conventions for AI coding agents

## License

MIT — see [LICENSE](LICENSE).
