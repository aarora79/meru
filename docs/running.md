# Running Meru

This guide takes you from nothing to asking Meru a question, then covers settings,
indexing your files, running it as a service, the dashboard, and fixing common
problems. It describes v0.2: questions, streamed answers, session transcripts,
routing, and answers from your own files with citations. Tools and memory arrive in
later milestones ([ROADMAP.md](../ROADMAP.md)).

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
ollama pull nomic-embed-text                         # embeddings, for searching your files
```

`meru setup` (step 8) can run these downloads for you once Meru is built.

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

`go install` prints nothing when it succeeds, and it reuses packages it compiled
before, so a second build often finishes in a second or two. To see what it does:

```sh
go install -v ./cmd/merud ./cmd/meru      # list each package as it compiles
go install -a -x ./cmd/merud ./cmd/meru   # rebuild every package and print each command
```

`-v` names each package it compiles; with everything cached it may print little.
`-a` ignores the cache and rebuilds every package, and `-x` prints every command
Go runs, which is long. To check the install, `ls -la ~/go/bin/merud` shows when
the file was written, and `go version -m ~/go/bin/merud` shows the Go version and
module it was built from.

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
| `~/.meru/meru.db` | the search index over your files, readable only by you (see step 7) |
| `~/.meru/secrets.toml` | API keys for MCP servers, readable only by you (see step 8) |

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

Quotes are optional unless the question starts with the word `ping`, `chat`,
`index`, `tools`, `log`, `setup` or `mcp`. Without quotes, `meru` reads that word as
a command: write `meru "index cards or a notebook?"`, not `meru index cards or a
notebook?`.

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

### When Meru wants to run a tool

While it answers, the model may call a tool, such as a search of your notes. `meru`
shows each call on standard error, dimmed, as it starts and ends:

```text
→ notes.search {"query":"garden budget"}
✓ notes.search 120 ms
```

A tool in a `confirm` list asks you first. `meru` shows the tool's name and
arguments and waits:

```text
Meru wants to run mail.send (mcp) with:
  {
    "to": "sam@example.com",
    "subject": "Garden budget"
  }
Run mail.send? [o]nce  [s]ession  [d]eny:
```

Type `o` to run this call, `s` to run it and every later call to the tool in this
session, or `d` to refuse; the whole word works too. A one-shot session ends with
the answer, so `s` covers only this question. A refused call shows as
`✗ mail.send declined`, and the model answers without it. Ctrl-C stops the question.

When standard input isn't a terminal, as in a script or a pipe, nobody can answer, so
`meru` denies the call and prints one line saying so. Tool lines and the prompt go to
standard error, so `meru "..." > answer.txt` saves only the answer.

`meru chat` shows the same calls as dim lines inside Meru's reply, and asks in a box
in the conversation. Press `o`, `s` or `d`, or pick with ← and → and press Enter.
The box opens with deny selected, so a stray Enter can't approve a call. While the
box is open, the input box takes no typing.

### See which tools the model may use

```sh
meru tools          # meru tools list does the same
```

```text
notes  mcp · stdio · connected
  notes.search  asks first
  notes.read
  2 of 5 tools allowed
  warning: allow lists "serch", but notes offers no such tool

meru  builtin · connected
  remember
  write_file    asks first
  configure     always asks
  3 of 3 tools allowed
```

Each block is one tool source: an MCP server, another agent, or Meru's built-in
tools. "asks first" marks a tool in a `confirm` list; "always asks" marks one that
asks whatever the config says. A source `merud` couldn't reach shows
`not connected` and the reason. A warning names each `allow` entry the source
doesn't offer, most often a typo. With no sources, `meru tools` says how to
add one.

### See what tools ran

```sh
meru log            # the last 20 tool calls, newest first
meru log -n 50      # the last 50
meru log -v         # each call's result under it
```

```text
2026-09-24 10:17:21  101500-ab12  mcp  mail.send     declined  deny  0 ms    {"to":"sam@example.com"}
2026-09-24 10:16:02  101500-ab12  mcp  notes.search  ok        -     120 ms  {"query":"garden"}
```

The columns are the local time, the session, the kind of tool, the tool, how the
call ended, what you chose when asked (`-` when nobody was asked), how long it took,
and its arguments, cut to fit one line.

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

### How much merud logs

`merud` writes to `~/.meru/merud.log`. By default it writes its startup settings,
each model warm-up, one line per question, and a line when it stops:

```text
level=INFO msg=turn session=2026-09-24T020539-1f53 route=tools source=tui outcome=ok ms=796 ttft_ms=594 tokens_in=85 tokens_out=142 trace_id=be9e7312…
```

To see where a question's time went, start `merud` with `-v`, or set the level in
the config file:

```toml
[log]
level = "debug"   # "debug", "info" (the default), "warn" or "error"
```

At `debug`, each stage of a question adds a line: the request, the session and its
history, the route with the probability of each route, the prompt's size, each call
to Ollama (time to headers, time to the first token, how many chunks the model
spent thinking, Ollama's load, prompt and answer times, tokens per second) and the
reply. Every line of one question carries the same `trace_id`, so
`grep be9e7312 ~/.meru/merud.log` shows that question alone. The log never holds
your questions or answers, unless you also set `capture_content = true` under
`[observability]`; then the debug lines add the first 200 characters of each.

To run a second, separate Meru, for example to try settings, give it its own home:

```sh
merud -config /tmp/meru-test/config.toml          # home is /tmp/meru-test
meru -socket /tmp/meru-test/merud.sock "hello"
```

## 7. Index your files

`merud` searches only the folders you name, and none by default. Add them to
`~/.meru/config.toml`:

```toml
[index]
folders = ["~/notes"]              # absolute paths, or paths starting with ~/
```

Restart `merud`: it reads the folder list only when it starts, and `meru index`
can't add a folder that isn't listed. At startup it scans every listed folder, cuts each file into
chunks, embeds them with the `embed` model and stores them in `~/.meru/meru.db`.
The first scan of a large folder takes a while, because every chunk goes through
the embedding model; later scans re-read only files whose content changed. While
`merud` runs it watches the folders and re-indexes a file about half a second after
you save it.

Then ask about your notes. When the router sends a question to search, the answer
cites the excerpts it used by number, and `meru` lists them after it:

```text
$ meru "what is the Q3 budget for the garden project?"
The Q3 budget for the garden project is 4,200 dollars [1].

Sources:
[1] ~/notes/garden.md, "Budget", lines 3–5
```

Each source gives the file, the heading it sits under, and the lines (or the page,
for a PDF). `meru` lists only the sources the answer cites. When the answer cites
none, it lists every source the prompt held, which with a small index can be every
file. `meru chat` shows the same list under each answer. A question the router
answers directly, such as "what is the capital of France?", gets no search and no
list, unless it names one of your folders, such as "meru" for `~/repos/meru`.

**What it skips.** `merud` reads Markdown, plain text, HTML, PDF and source code.
It never reads symlinks, hidden files and folders, secret files such as `.env`,
`*.pem` or `id_rsa`, build folders such as `node_modules` or `.venv`, images,
audio, video, archives, binaries, or files over 5 MB (`max_file_mb`). It also
follows each folder's `.gitignore`. To skip more, put a `.meruignore` in any
indexed folder. It uses `.gitignore` syntax, and `!name` brings back a file that
`.gitignore` leaves out:

```text
# .meruignore
drafts/
*.log
!notes.log
```

Patterns under `[index] ignore` in `config.toml` apply to every folder.
[ARCHITECTURE.md](../ARCHITECTURE.md#getting-your-content-in) lists every rule and
the order they run in.

**Rescan or check.** With `merud` running:

```sh
meru index                 # rescan every configured folder now
meru index ~/notes/work    # rescan one folder or file inside an [index] folder
meru index -status         # what the index holds; --status works too
```

`meru index` prints progress to standard error and one summary line to standard
output, such as `3 files indexed (5 chunks), 9 unchanged, 0 removed, 0 failed,
2 skipped, in 1.2s`. A path outside every `[index]` folder gets an error that names
the config file to change; add the folder there and restart `merud`.
`meru index -status` prints the folders, the counts of files, chunks and vectors,
whether a scan is running, and the last scan's summary:

```text
Folders:    ~/notes
Index:      12 files, 87 chunks, 87 vectors
Scanning:   no
Last scan:  2026-09-23T10:15:00-04:00, 12 files indexed (87 chunks), 0 unchanged, 0 removed, 0 failed, 2 skipped, in 3.1s
```

**Why a file is missing.** Start `merud` with `-v` and search the log for the
file's name. Each skipped path gets a debug line with its reason, such as
`secret`, `ignored` or `too-large`.

**On Linux, the watch limit.** Linux lets each user watch a fixed number of folders
(`fs.inotify.max_user_watches`). When a large tree passes it, `merud` logs one
warning, keeps the watches it has, and picks up the other folders' changes at the
next startup. To raise the limit:

```sh
sudo sysctl fs.inotify.max_user_watches=524288
echo fs.inotify.max_user_watches=524288 | sudo tee /etc/sysctl.d/60-meru.conf
```

**Starting over.** `meru.db` holds nothing you can't rebuild. Stop `merud`, delete
`~/.meru/meru.db`, and start `merud` again to re-index from scratch. Changing the
`embed` model has the same effect on the vectors: `merud` drops them, keeps keyword
search working, and re-embeds your files.

## 8. Set up and connect tools

### meru setup

`meru setup` walks through a first run in five short steps:

1. **Ollama.** It checks that Ollama answers at `base_url`. If not, it prints the
   install command for your system and waits while you start it.
2. **Models.** You pick `lite` or `full` (or it uses the profile already in
   `config.toml`), and it runs `ollama pull` for each model, with Ollama's own
   progress bar.
3. **Your files.** With no `config.toml` yet, it asks which folders to index and
   writes the file. With one already there, it leaves the file alone and tells you
   where to add folders, so your comments and settings stay as you wrote them.
4. **Tools.** It offers each server in the catalog, one at a time (see below).
5. **A test question.** If `merud` is running, it asks one question and prints
   the answer. If not, it tells you how to start `merud`.

### meru mcp add

An MCP server gives the model tools. Meru knows six:

```sh
meru mcp list-catalog
```

| Name | What the model gets | What you need |
| --- | --- | --- |
| `brave` | web and news search | a Brave Search API key, and Node.js for `npx` |
| `fetch` | reading a web page | `uv`, which provides `uvx` |
| `gmail` | search and read mail; drafting and sending ask first | a Google OAuth client and `uv`; you sign in to Google on first use |
| `calendar` | calendars and events; changing an event asks first | as for `gmail` |
| `drive` | Drive files and Docs; creating or editing a doc asks first | as for `gmail` |
| `obsidian` | list, read and search notes; appending asks first | Obsidian running with the Local REST API plugin, and `uv` |

To add one:

```sh
meru mcp add brave
```

Meru shows what the server does and offers two paths:

- **d) Do it for me.** Meru asks for each thing the server needs, one at a time.
  It reads an API key without showing it on screen and saves it to
  `~/.meru/secrets.toml`, never to `config.toml`. Then it shows the exact block
  it will add to `config.toml` and writes it only after you say yes.
- **s) Show me how.** Meru prints the install step, the block, the file to paste
  it into, and the lines to add to `secrets.toml`. It writes nothing.

A server outside the catalog works too:

```sh
meru mcp add notes -- /usr/local/bin/notes-mcp --vault ~/notes   # a stdio server
meru mcp add calendar --url http://127.0.0.1:8123/mcp            # a running HTTP server
```

Meru doesn't know such a server's tool names, so its `allow` list starts empty
and the model gets none of its tools. After the restart below, run `meru tools`
to see what the server offers, and name the tools to allow in `config.toml`.

`merud` reads `config.toml` only when it starts. After adding a server, restart it
and check what the model now has:

```sh
pkill merud; merud &
meru tools
```

In chat you can also ask Meru to "connect my Gmail". The model calls the built-in
`configure` tool, which asks you every time, with only "approve once" and "deny".
A server that needs an API key you haven't saved yet isn't added from chat, because
keys never pass through the model; Meru tells you to run `meru mcp add` instead.

`secrets.toml` holds one `name = "value"` line per key. `merud` refuses the file if
other users can read it; `chmod 600 ~/.meru/secrets.toml` fixes that.

## 9. Keep merud running

To start `merud` at login and restart it if it stops, install the service file for
your system. [deploy/README.md](../deploy/README.md) has the exact commands:

- **macOS:** a `launchd` agent.
- **Linux:** a `systemd` user unit, including how to start it at boot on a server.
- **Windows:** no service wrapper yet; start `merud.exe` from Task Scheduler.

## 10. Watch it on a dashboard (optional)

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

### See one question's trace

Each question also sends a trace: one span for each stage, nested so you can see
which stage took the time.

1. In Grafana, open **Explore** and pick the **Tempo** data source.
2. To list recent questions, choose **Search**, set **Service Name** to `merud` and
   **Span Name** to `rpc.request`, then run the query.
3. To find the question behind a log line, copy the line's `trace_id`, choose
   **TraceQL**, paste the ID into the query box and run it.

The trace shows `rpc.request` at the top, `meru.turn` under it, and then the
session, the route and its model call, the prompt, the answer's model call and the
two transcript writes. Each `gen_ai.chat` span carries token counts and Ollama's
load, prompt and answer times; the answer's span has a `first_token` event. Spans
carry no question or answer text unless `capture_content = true`.

## 11. Update to a newer version

From your clone of the repo, pull the latest code, rebuild both programs and
restart `merud`:

```sh
git pull
go install ./cmd/merud ./cmd/meru
pkill merud; merud -v &
meru chat
```

`merud` keeps running the old program until you restart it, and `meru` talks to
whichever `merud` is running, so restart it every time you rebuild. `-v` turns on
the debug log (see [How much merud logs](#how-much-merud-logs)); leave it off for
the shorter log. If `merud` runs as a service, restart it with the service
manager instead of `pkill` ([deploy/README.md](../deploy/README.md)).

## 12. Troubleshooting

| What you see | What it means and what to do |
| --- | --- |
| `connect to merud at …: … (is merud running?)` | `merud` isn't running, or it uses a different socket. Start `merud`, or pass `-socket` to `meru`. |
| `merud` says Ollama is too old | Update Ollama to 0.12.11 or later. |
| `merud` can't reach Ollama | Start Ollama (open the app, or run `ollama serve`) and check `curl http://127.0.0.1:11434/api/version`. |
| `model … not found` in the answer or the log | Pull the model named in the error with `ollama pull`. |
| `secrets … so other users can read it; run chmod 600 …` | Run the `chmod 600` command in the message. `meru mcp add` also fixes the mode when it saves a key. |
| `merud` says another merud is running | One `merud` per socket. Stop the other one, or give this one its own `-config` home. |
| A file never shows up in answers | Check that its folder is in `[index] folders`, then run `merud -v` and search `~/.meru/merud.log` for the file's name; the skip line gives the reason. |
| `merud` warns that the OS watch limit was reached | Linux only: raise `fs.inotify.max_user_watches` (see step 7). Changes still get in at the next startup. |
| `merud` refuses a config value | The message names the key. Fix it in `~/.meru/config.toml`; `config.example.toml` shows the allowed values. |
| The first answer is slow | Ollama was loading the model. Later answers are fast while `merud` runs, because it keeps the models loaded. |
| Answers are slow and you can't tell why | Stop `merud`, run `merud -v`, ask again and read `~/.meru/merud.log`. The debug lines show the time each stage took; a large `thinking_chunks` count means the model spent the wait reasoning before its first word. |
| Anything else | Run `merud -v` and read `~/.meru/merud.log`. |

## 13. Uninstall

```sh
rm ~/go/bin/merud ~/go/bin/meru
rm -rf ~/.meru          # deletes your settings, every transcript and the index
```

If you installed the service file, remove it first with the `bootout` (macOS) or
`disable` (Linux) command in [deploy/README.md](../deploy/README.md). To free the
disk space the models use, run `ollama rm` with each model's name.

## For developers

`make check` runs every check CI runs, `make e2e` runs the end-to-end tests against
a fake Ollama, and [docs/ci.md](ci.md) explains each one. [AGENTS.md](../AGENTS.md)
holds the rules for changing the code, and [docs/coding-notes/](coding-notes/)
explains each package for readers new to Go.
