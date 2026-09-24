# Running Meru

This guide takes you from nothing to asking Meru a question, then covers settings,
running it as a service, the dashboard, and fixing common problems. It describes
v0.1: questions, streamed answers, session transcripts and routing. File search,
tools and memory arrive in later milestones ([ROADMAP.md](../ROADMAP.md)).

## 1. Install the prerequisites

You need two programs on the machine that will run Meru.

- **Go 1.26 or later**, to build Meru. Download it from <https://go.dev/dl/>, or on
  macOS run `brew install go`. Check with `go version`. The repo pins Go 1.26.6;
  if yours is older, `go` downloads 1.26.6 by itself the first time you build.
- **Ollama 0.12.11 or later**, to run the models. Download it from
  <https://ollama.com/download>. It runs in the background and listens on
  `http://127.0.0.1:11434`. Check with `curl http://127.0.0.1:11434/api/version`.
  `merud` refuses to start with an older Ollama, because the router needs log
  probabilities, which Ollama added in 0.12.11.

## 2. Download the models

The default `lite` profile uses two small models, about 2 GB in total:

```sh
ollama pull hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M   # answers and routes questions
ollama pull nomic-embed-text                         # embeddings (used from v0.2)
```

For the `full` profile (32 GB of RAM or more, or a GPU with about 24 GB), also pull:

```sh
ollama pull qwen3.8:27b
ollama pull qwen3-embedding:0.6b
```

## 3. Build Meru

```sh
git clone https://github.com/aarora79/meru.git
cd meru
go install ./cmd/merud ./cmd/meru
```

`go install` puts both programs in `~/go/bin`. Make sure that folder is on your
`PATH`: add `export PATH="$HOME/go/bin:$PATH"` to your shell's startup file if
`which merud` finds nothing.

To build for another platform instead, `make build` writes binaries for macOS,
Linux and Windows to `bin/<os>-<arch>/`.

## 4. Start merud

```sh
merud
```

On startup `merud` reads its config, checks the Ollama version, loads each model
into memory, and then listens on its socket. The first start can take several
seconds while Ollama loads the models; later questions skip that wait. Leave it
running in its own terminal, or start it in the background with `merud &`.

`merud` keeps everything in its home folder, `~/.meru/`:

| Path | What it holds |
| --- | --- |
| `~/.meru/config.toml` | your settings (optional; see step 6) |
| `~/.meru/merud.sock` | the socket `meru` connects to (only while `merud` runs) |
| `~/.meru/merud.log` | `merud`'s log |
| `~/.meru/sessions/YYYY/MM/*.jsonl` | one transcript file per conversation |

Stop `merud` with Ctrl-C, or `kill` its process. It finishes cleanly and removes
its socket.

## 5. Ask questions

In another terminal:

```sh
meru ping                                   # prints "merud is up"
meru "what is the capital of France?"       # one question; the answer streams out
meru what is the capital of France          # quotes are optional
meru chat                                   # a conversation in the terminal
```

In `meru chat`:

| Key | What it does |
| --- | --- |
| Enter | send the question |
| Ctrl-C | stop the answer that is streaming; press again when idle to quit |
| Ctrl-D | quit |
| Up arrow | bring back your last question |
| PgUp, PgDn | scroll |

Each `meru "..."` starts a new conversation. `meru chat` keeps one conversation
going until you quit, so later questions see the earlier ones.

`meru` exits with 0 on success, 1 on an error, and 130 when you press Ctrl-C, so
scripts can check what happened.

## 6. Change settings

Without a config file, `merud` uses the `lite` profile and the defaults. To change
anything, create `~/.meru/config.toml` with only the keys you want to change.
[config.example.toml](../config.example.toml) lists every key with its default and
an explanation. For example, to switch to the `full` profile:

```toml
profile = "full"
```

Restart `merud` after editing the file. It checks every value at startup and
refuses to start on a typo, an unknown key or a bad value, naming the key. It also
refuses any Ollama or metrics address that isn't on this machine.

To run a second, separate Meru, for example to try settings, give it its own home:

```sh
merud -config /tmp/meru-test/config.toml          # home is /tmp/meru-test
meru -socket /tmp/meru-test/merud.sock "hello"
```

## 7. Keep merud running

To start `merud` at login and restart it if it stops, install the service file for
your system. [deploy/README.md](../deploy/README.md) has the exact commands:

- **macOS:** a `launchd` agent.
- **Linux:** a `systemd` user unit, including how to start it at boot on a server.
- **Windows:** no service wrapper yet; start `merud.exe` from Task Scheduler.

## 8. Watch it on a dashboard (optional)

With Docker installed, one command starts a local Grafana with a ready-made Meru
dashboard:

```sh
docker compose -f deploy/observability/compose.yaml up -d
```

Then add this to `~/.meru/config.toml` and restart `merud`:

```toml
[observability]
otlp_endpoint = "http://127.0.0.1:4318"
```

Open <http://127.0.0.1:3000> (user `admin`, password `admin`). The dashboard shows
time to first token, turn duration, tokens per second, router decisions and model
loads. Everything stays on this machine. [deploy/README.md](../deploy/README.md)
explains each panel and how to stop the stack.

## 9. Troubleshooting

| What you see | What it means and what to do |
| --- | --- |
| `connect to merud at …: … (is merud running?)` | `merud` isn't running, or it uses a different socket. Start `merud`, or pass `-socket` to `meru`. |
| `merud` says Ollama is too old | Update Ollama to 0.12.11 or later. |
| `merud` can't reach Ollama | Start Ollama (open the app, or run `ollama serve`) and check `curl http://127.0.0.1:11434/api/version`. |
| `model … not found` in the answer or the log | Pull the model named in the error with `ollama pull`. |
| `merud` says another merud is running | One `merud` per socket. Stop the other one, or give this one its own `-config` home. |
| `merud` refuses a config value | The message names the key. Fix it in `~/.meru/config.toml`; `config.example.toml` shows the allowed values. |
| The first answer is slow | Ollama was loading the model. Later answers are fast while `merud` runs, because it keeps the models loaded. |
| Anything else | Read `~/.meru/merud.log`. |

## 10. Uninstall

```sh
rm ~/go/bin/merud ~/go/bin/meru
rm -rf ~/.meru          # deletes your settings and every transcript
```

If you installed the service file, remove it first with the `bootout` (macOS) or
`disable` (Linux) command in [deploy/README.md](../deploy/README.md). To free the
disk space the models use, run `ollama rm` with each model's name.

## For developers

`make check` runs every check CI runs, `make e2e` runs the end-to-end tests against
a fake Ollama, and [docs/ci.md](ci.md) explains each one. [AGENTS.md](../AGENTS.md)
holds the rules for changing the code, and [docs/coding-notes/](coding-notes/)
explains each package for readers new to Go.
