# Running Meru

This guide takes you from nothing to asking Meru a question, then covers settings,
indexing your files, running it as a service, the dashboard, and fixing common
problems. It describes v0.3: questions, streamed answers, session transcripts,
routing, answers from your own files with citations, web search through a
SearXNG you run, and tools from MCP servers and A2A agents you allow. Memory and scheduled jobs arrive in later milestones
([ROADMAP.md](../ROADMAP.md)).

## 1. Install the prerequisites

You need two programs on the machine that will run Meru, and a third for web
search.

- **Go 1.26 or later**, to build Meru. Download it from <https://go.dev/dl/>, or on
  macOS run `brew install go`. Check with `go version`. The repo pins Go 1.26.6;
  if yours is older, `go` downloads 1.26.6 by itself the first time you build.
- **Ollama 0.12.11 or later**, to run the models. Download it from
  <https://ollama.com/download>. It runs in the background and listens on
  `http://127.0.0.1:11434`. Check with `curl http://127.0.0.1:11434/api/version`.
  `merud` refuses to start with an older Ollama, because the router needs log
  probabilities, which Ollama added in 0.12.11.
- **Docker**, only for web search. Meru searches the web through SearXNG, which
  runs in a container (see [Web search](#web-search)). Meru runs without Docker;
  only the `web_search` tool stops working, and it tells the model why. On macOS,
  Docker Desktop or Colima provides `docker compose`; check with
  `docker compose version`.

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
`index`, `tools`, `log`, `usage`, `setup`, `memory`, `skills` or `mcp`. Without quotes, `meru` reads that word as
a command: write `meru "index cards or a notebook?"`, not `meru index cards or a
notebook?`.

In `meru chat`:

| Key | What it does |
| --- | --- |
| Enter | send the question |
| Enter while an answer streams | queue the question, marked `queued`; it goes when the turns before it end, and up to five wait |
| Ctrl-C | stop the answer that is streaming and drop any queued questions; press again when idle to quit |
| Ctrl-D | quit |
| Up arrow | bring back your last question |
| PgUp, PgDn | scroll |
| Ctrl-Y | copy the last code block of the newest answer |
| `/copy N`, then Enter | copy code block N; `/copy` alone works like Ctrl-Y |
| `/usage`, then Enter | show how much you use Meru; Esc or q closes it |
| `/new`, then Enter | start a new conversation: the screen clears, queued questions go, and the next question carries none of the earlier ones |
| `/me`, then Enter | show what Meru knows about you; Esc or q closes it |
| `/mcp`, then Enter | show each MCP server's state, the table `meru mcp` prints; Esc or q closes it |
| `/exit`, then Enter | quit, like Ctrl-D |

The line at the top of `meru chat` shows the profile, the main model, the search
index, the memories and the session on the left:

```text
Meru मेरु lite · minicpm5:2b · 2637 docs (11698 vectors, 84 MB) · 7 memories · session 101500-ab12
```

The index part counts the files Meru searches, the vectors it holds for them, and
the size of `meru.db` on disk; `· indexing` follows while a scan runs. Then comes
the number of memories, and `· no profile` while Meru knows nothing about you. On
the right, a wide terminal shows the last hour's use, such as
`1h: 4 questions · 18k in · 2.1k out`, then whether `merud` is running. A
narrow one drops the last hour first, then the vectors, size and memory count.

Any other line that starts with `/` stays in the input box, and the bottom line
lists the commands `meru chat` knows.

Each `meru "..."` starts a new conversation. `meru chat` keeps one conversation
going until you quit or type `/new`, so later questions see the earlier ones. That
cuts both ways: after the model says "I don't know" a couple of times, it tends to
keep saying it. Type `/new` and ask again.

Each line under "Sources:" links to its file. In a terminal that supports links
(iTerm2, Ghostty, WezTerm, kitty, VS Code's terminal, Windows Terminal), Cmd-click
or Ctrl-click the line to open the file. macOS Terminal shows the same lines
without the link. A pipe or a file gets plain text.

In `meru chat`, a web or file link in an answer is a link too. The chat shows the
URL without `https://`, cut with `…` to fit the line, such as
`mail.google.com/mail/u/0/#inbox/18f2…`. A plain click on it, or on a Sources line,
opens the full URL in your browser, and the bottom line says
`opened mail.google.com/…`. The chat does the opening itself, because with
`[chat] mouse_copy` on, the default, it takes every click from the terminal (see
[Copying code](#copying-code)). It opens `http`, `https` and `file` links only.
With `mouse_copy = false` the terminal gets the click, and its own link click,
often Cmd-click or Ctrl-click, opens the link. With `NO_COLOR=1` the chat shows the
full URL as plain text instead.

`meru` exits with 0 on success, 1 on an error, and 130 when you press Ctrl-C, so
scripts can check what happened.

### Copying code

When an answer holds code, such as the commands to install and run `btop`, each
block gets a dim label on the line under it:

```text
    brew install btop
    ⧉ copy 1
```

Type `/copy 1` and Enter to put that block on the clipboard, or press Ctrl-Y for
the last block of the newest answer. The numbers run on through the conversation
and start again after `/new`. The clipboard gets the block's text as the model
wrote it, with no colours and no newline at the end, so a pasted command waits
for you to press Enter. The bottom line says what happened, such as
`copied block 1 (1 line)`.

`meru chat` copies with `pbcopy` on macOS, `clip.exe` on Windows, and on Linux
with the first of `wl-copy`, `xclip` and `xsel` it finds. With none installed it
asks the terminal to copy through OSC 52, an escape code that many terminals
accept, and says so. In tmux, OSC 52 needs `set -g set-clipboard on`.

A click on the label copies the block too, and a click on a link opens it. For
that the chat takes the mouse, so the wheel scrolls the conversation and a plain
drag no longer selects text. To
select text yourself, hold Option while you drag in iTerm2, or Shift in most other
terminals. To give plain selection back, set this in `~/.meru/config.toml` and
restart `meru chat`:

```toml
[chat]
mouse_copy = false
```

One-shot `meru "..."` prints no labels: its output stays plain for pipes and
scripts.

### Tell Meru about you

Meru puts what it knows about you into every prompt: your name, your work, where
you live and how you like answers. Without it, the model can't tell whether "Sam"
in a letter is you or someone you know. Tell it once:

```sh
meru setup user
```

It asks one short question at a time, and Enter skips any of them:

```text
Answer a few questions about you. Press Enter to skip one.
Your name: Dana Reyes
Your email address, the one your Google or other accounts use: dana@example.com
What you do, your role and where you work: staff engineer at Acme
Where you live (a city is enough): Boston
Anything else Meru should always know about you? One fact per line; an empty line ends.
> I have two kids
>
How you like answers, for example "short, with bullet points": short, with bullet points

Saved:
  me/name-dana-reyes.md  Name: Dana Reyes
  ...
```

Each answer becomes one memory, a Markdown file under `~/.meru/memory/me/` or
`~/.meru/memory/preferences/` that you can read and edit. Run it again to add
more; when Meru already knows something, it asks whether to keep that or start
over. `meru setup` offers this step too, when `merud` is running. Tools such as
Gmail and Calendar take your address on every call, so the email answer saves
the model from searching your files for it.

To see, add or delete memories by hand:

```sh
meru memory list                        # every memory, grouped by kind
meru memory list me                     # one kind
meru memory add me I have two kids      # save one; prints its ID
meru memory forget me/i-have-two-kids.md
```

You can also tell Meru in chat: "remember that I work on the registry team".
The model saves it with its `remember` tool, which shows as a tool line like any
other.

In `meru chat`, `/me` shows what Meru knows about you. While it knows nothing,
the empty chat says so and the header shows `no profile`; both go away within
half a minute of the first memory.

### Skills

A skill is a Markdown file of instructions for one kind of task. Meru ships
four: `writing`, plain-English rules for emails, summaries and reports;
`explainer`, which builds a one-page HTML explainer on a topic;
`web-research`, which tells the model how to search and read pages for a
question about how to use a program or about current facts; and
`file-research`, which tells it how to find and read things in your files
with the file tools. When it starts, `merud` copies each one it
doesn't find to `~/.meru/skills/<name>/SKILL.md`.

Every prompt lists each skill's name and description. For each question, a short
call to the fast model picks the skills it needs, at most two, and only their
instructions join the prompt. Ask "write a short email to my landlord" and the
route badge in `meru chat` reads `direct · 0.91 · writing`.

```sh
meru skills list              # each skill, with [built-in] and [edited] marks
meru skills show writing      # print its SKILL.md
meru skills reset writing     # put the shipped copy back
```

`reset` replaces your edits, so on a terminal it asks first; in a script, add
`--yes`. It works only on the four built-ins.

**Edit a skill** by opening its `SKILL.md` in any editor. `merud` notices the
change on the next question; no restart. `merud` never overwrites your copy, even
after an upgrade, so run `meru skills reset` to take a newer shipped version.

**A skill brings its tools.** A skill can list the tools its steps use in an
`allowed-tools` line, as Claude's skills do. `web-research` lists `web_search,
web_fetch`, and `file-research` its four file tools. When the pick chooses a
skill and the question's route lacks those tools, the turn gets them and the
route widens: `direct` becomes `tools`, `search` becomes `search+tools`. The
route badge shows the wider route. A skill only brings tools your config
allows. With `web_search` off (no SearXNG) and `web_fetch` taken out of
`[builtin] tools`, `web-research` has nothing to use, so the turn leaves its
instructions out. If your `~/.meru/skills/web-research/SKILL.md` came from an
older Meru, it lacks the line: run `meru skills reset web-research` and
`meru skills reset file-research` to take the new copies.

**After an upgrade, reset the research skills.** Newer copies of
`web-research` and `file-research` carry sharper descriptions: `web-research`
names how to use a program or command, and `file-research` rules that out.
With the old wording the pick sent "help me understand btop with some simple
commands" to `file-research`, and the model grepped your folders instead of
searching the web. `merud` never overwrites your copies, so take the new ones
with one command per skill:

```sh
meru skills reset --yes web-research
meru skills reset --yes file-research
```

**Add your own** by making a folder under `~/.meru/skills/` whose name matches the
skill's `name`, holding a `SKILL.md`:

```markdown
---
name: meeting-notes
description: Turn rough meeting notes into decisions and action items. Use when
  asked to tidy up or summarize notes from a meeting.
---

List the decisions first, then each action item with its owner and date.
```

The name takes lowercase letters and digits joined by `-`. The description is what
the fast model reads when it picks, so say when to use the skill. `merud` skips a
folder that breaks a rule, and `meru skills list` shows why under `Skipped:`.

**Turn a skill off** by naming it in `[skills] disabled`, then restart `merud`:

```toml
[skills]
disabled = ["explainer"]
```

`merud` doesn't load a disabled skill, and doesn't copy a disabled built-in back,
so you can delete `~/.meru/skills/explainer/` and it stays gone. A name that
matches no skill is fine; `merud.log` notes it. A folder you add loads with no
change here.

**Files skills make.** The built-in `write_file` tool saves a file, such as an
explainer page, in `~/meru-output/`. It creates the folder the first time, writes
nowhere else (no absolute paths, no `..`, no symbolic links), caps a file at
1 MiB, and won't replace a file unless you agree. It asks before every file, and
the answer names the full path. To change the folder, or to let it write without
asking, edit `config.toml`:

```toml
[skills]
output_dir = "~/meru-output"

[builtin]
confirm = ["write_file"]   # take write_file out to stop the prompt
```

The model sees `write_file` only on turns that offer tools, so ask for the file
in so many words. The router sent "make an explainer page about how DNS works and
save it" to a route with tools, and "write an explainer on TCP" to `direct`, where
the page comes back in the answer instead. Skills work best on the `full` profile;
the 2B model follows long instructions less well.

### When Meru wants to run a tool

While it answers, the model may call a tool, such as a search of your notes. `meru`
shows each call on standard error, dimmed, as it starts and ends:

```text
→ notes.search {"query":"garden plan"}
✓ notes.search 120 ms
```

A tool in a `confirm` list asks you first. `meru` shows the tool's name and
arguments and waits:

```text
Meru wants to run mail.send (mcp) with:
  {
    "to": "sam@example.com",
    "subject": "Garden plan"
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

Each block is one tool source: an MCP server, another agent, Meru's built-in
tools, or your local commands, each with the program it runs. "asks first" marks a tool in a `confirm` list; "always asks" marks one that
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
and its arguments, cut to fit one line. For a local command the last column is the
program and arguments it ran, such as `git -C /Users/you/repos/meru log --oneline`.

### See how much you use Meru

```sh
meru usage
```

```text
                  1h   today     week   month     30d     all
sessions           1       2        5      12      14      30
questions          4       9       31      88      97     212
tokens in        18k     41k     150k    420k    468k    1.4M
tokens out      2.1k    5.3k      19k     61k     66k    180k
active time   2m 14s  5m 01s  20m 10s  1h 01m  1h 07m  3h 05m
docs touched       3       7       22      51      55     140
tool calls         1       2        6      14      15      40

Today, week and month follow the local calendar.
```

Each column is a window of time: the last hour, today since midnight, this week
since Monday, this month since the 1st, the last 30 days, and all time. The rows
count the sessions you asked in, the questions Meru answered, the tokens the main
model read and wrote, how long `merud` spent answering, the files whose excerpts went
into a prompt, and the tool calls. A question that failed or that you stopped
doesn't count. `k` means thousands and `M` millions. In `meru chat`, type `/usage`
to see the same table.

## 6. Change settings

Without a config file, `merud` uses the `lite` profile and the defaults.
`meru setup` writes `~/.meru/config.toml` from the config template, which holds
every key. What is on by default is uncommented, with its default value, so you
see it and can change it. What is off, such as the MCP servers in the catalog,
the example local commands and an A2A agent, sits in comments; delete the `# `
in front of a block's lines to turn it on. To see the template at any time, or
to start over from it:

```sh
meru config template                      # print it
meru config template > ~/.meru/config.toml   # start over; this replaces your file
```

[config.example.toml](../config.example.toml) in the repo is the same file. A
config with only the keys you change works too; any key you leave out keeps its
default. For example, to switch to the `full` profile:

```toml
profile = "full"
```

**Turn a built-in tool off** by taking its name out of `[builtin] tools`. The
model then never sees it, and `meru tools` doesn't list it:

```toml
[builtin]
tools   = ["datetime", "remember", "write_file", "read_file", "list_folder", "grep", "web_search"]
confirm = ["write_file"]   # each name here must also be in tools
```

That list leaves out `configure` and `web_fetch`. A tool you list still needs
what it works on: the file tools need `[index] folders`, and `web_search` needs
`[web] searxng_url`. Without it the tool stays off, and `merud.log` has a
`built-in tool off` line that says why.

**Limits on a question.** Two keys in `[agent]` keep a question from running
forever:

```toml
[agent]
max_output_tokens = 8192   # most tokens one model call may write, thinking included
turn_timeout      = "5m"   # longest one question may take, approvals included
```

A thinking model such as MiniCPM5 reasons out of sight before it writes, and
Ollama counts those tokens against `max_output_tokens`. Without a cap, a model
once sat on "thinking…" for many minutes. When a question hits either limit, or
the model uses up `max_rounds` calling tools and never writes an answer, Meru
says so instead of going quiet:

```text
Sorry, I couldn't answer that. Try asking again, or rephrase the question.
```

If part of the answer had already appeared, it stays, followed by a note that
Meru stopped it. The `turn` line in `merud.log` then shows `outcome=timeout`,
`outcome=cut_off` or `outcome=gave_up`. On the `full` profile a long answer
can need more time; raise `turn_timeout` if answers stop short.

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
the embedding model; later scans re-read only files whose content changed. To stop
indexing a folder, take it out of `[index] folders` and restart `merud`: the
startup scan drops its files, and `meru index -status` shows the smaller count.
While
`merud` runs it watches the folders and re-indexes a file about half a second after
you save it.

Then ask about your notes. When the router sends a question to search, the answer
cites the excerpts it used by number, and `meru` lists them after it:

```text
$ meru "when does the garden project sow tomatoes?"
The garden project sows tomatoes on 12 April [1].

Sources:
[1] ~/notes/garden.md, "Planting", lines 3–5
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

### Dates and times

Each question's prompt carries today's date. For the time, a weekday, days until a
date or the time somewhere else, the model calls the built-in `datetime` tool, which
reads your computer's clock. It needs no setup and is offered on every question:
`meru "how many days until 25 December?"` shows `→ datetime` before the answer.

### Reading whole files

Search puts the ten best excerpts in the prompt, about 500 tokens each. When a
question needs more, such as "summarize everything in ~/notes/work", the model
can use four read-only tools on the same folders:

- `read_file` reads one file whole, 12,000 characters per call, PDFs page by page;
- `list_folder` lists a folder, 1 to 3 levels deep;
- `grep` finds every line that holds a word or a pattern;
- `search_files` runs the same search by meaning and keyword that puts the ten
  excerpts in the prompt, for the words the model picks, and returns numbered
  excerpts it can cite.

`read_file`, `list_folder` and `grep` reach your `[index] folders` and
`~/meru-output`, where `write_file` writes, `web_fetch` saves downloads and the
`google` server saves mail attachments; `search_files` searches only the index.
All four skip what the indexer skips. They run without asking; to approve each call, add
them to `[builtin] confirm`. `meru` shows each call as it runs:

```text
$ meru "grep my notes for tomatoes and tell me which files mention it"
→ grep {"pattern":"tomatoes"}
✓ grep 40 ms
Two files mention tomatoes: ~/notes/garden.md and ~/notes/2026/may.md.
```

### Search first, or let the model look

By default Meru searches your files before the model answers and puts the ten
best excerpts in the prompt. The model can still call `search_files`, `grep` and
`read_file` when those fall short. That is `retrieval = "auto"`.

Meru skips that search when a question needs no answer from your files. It
never searches on the `direct` route. On the `tools` route it skips the search
when the question names a connected server ("ask obsidian"), says "remember",
names the web ("search the web for …"), or names what a server's tools handle
("my last email", "my calendar"). Those turns still offer the file tools, but
their prompt drops the note on using them, so a web answer doesn't list your
files as its sources. The `search` and `search+tools` routes always search.
With `merud -v`, the log says `no search first` and why.

With `retrieval = "agentic"`, Meru runs no search first and leaves out earlier
conversations. The model looks for itself, the way a coding agent uses `ls`,
`grep` and a file reader: `search_files` or `grep` first, then `read_file` on what
matters, then the answer. Your profile and recalled memories still join every
prompt.

```toml
[index]
retrieval = "agentic"   # "auto" (the default) or "agentic"
```

`agentic` needs `search_files` in `[builtin] tools`; `merud` refuses to start
without it. On the `lite` model it answered the owner's check questions less
often than `auto` did: the 2B model sometimes kept searching until it ran out of
rounds. [ARCHITECTURE.md](../ARCHITECTURE.md#retrieval) has the numbers. Run
`meru check` with each setting on your own files before you switch.

## 8. Set up and connect tools

### meru setup

`meru setup` walks through a first run in seven short steps:

1. **Ollama.** It checks that Ollama answers at `base_url`. If not, it prints the
   install command for your system and waits while you start it.
2. **Models.** You pick `lite` or `full` (or it uses the profile already in
   `config.toml`), and it runs `ollama pull` for each model, with Ollama's own
   progress bar.
3. **Your files.** With no `config.toml` yet, it asks which folders to index and
   writes the config template with your profile and folders filled in. With one
   already there, it leaves the file alone and tells you
   where to add folders, so your comments and settings stay as you wrote them.
4. **Web search.** It checks that SearXNG answers JSON at `[web] searxng_url`. If
   nothing answers, it prints the container commands from
   [Web search](#web-search); if SearXNG answers a web page, it names the
   `formats` setting. Press Enter to check again, or type `s` to skip.
5. **Tools.** It offers each server in the catalog, one at a time (see below).
6. **About you.** If `merud` is running, it offers `meru setup user` (see
   [Tell Meru about you](#tell-meru-about-you)).
7. **A test question.** If `merud` is running, it asks one question and prints
   the answer. If not, it tells you how to start `merud`.

### Web search

Meru searches the web through SearXNG, a search engine you run yourself. It holds
no index: it passes your query to Google, Bing, DuckDuckGo and others, drops the
parts that identify you, and merges the results. No account, no API key.

You run it once, in Docker, and Meru uses it from then on.

```sh
mkdir -p ~/srv/searxng/core-config && cd ~/srv/searxng
curl -fsSL -O https://raw.githubusercontent.com/searxng/searxng/master/container/docker-compose.yml \
     -O https://raw.githubusercontent.com/searxng/searxng/master/container/.env.example
cp -i .env.example .env && printf 'SEARXNG_HOST=127.0.0.1\nSEARXNG_PORT=8888\n' >> .env
docker compose up -d
```

The two lines added to `.env` matter. Upstream's compose file listens on port
8080 on every network interface, which would let other machines on your network
use your SearXNG. With them, it listens on `127.0.0.1:8888` only, where Meru looks.

SearXNG answers on `http://127.0.0.1:8888` and returns web pages. Meru needs JSON,
which is off by default: SearXNG answers a JSON request with `403 Forbidden`. The
first start writes `~/srv/searxng/core-config/settings.yml`; add JSON to the end of
it:

```sh
cat >> ~/srv/searxng/core-config/settings.yml <<'EOF'

search:
  formats:
    - html
    - json
EOF
```

Then restart it and check that JSON comes back:

```sh
docker compose restart
curl -s 'http://127.0.0.1:8888/search?q=test&format=json' | head -c 200
```

A line starting `{"query":` means it works. A line of HTML means the `formats`
change did not take; check the file path and restart again.

Meru's default config already points at `http://127.0.0.1:8888`, so there is
nothing else to do. `merud` logs `web search ready` when it starts, `meru tools`
lists `web_search` under `meru`, and a question such as
`meru "search the web for the latest Go release"` uses it. To turn web search off,
set `searxng_url = ""` under `[web]` in `~/.meru/config.toml`.

To stop it: `cd ~/srv/searxng && docker compose down`. To update it:
`docker compose pull && docker compose up -d`.

**What leaves your machine.** Your search words go to the engines SearXNG asks;
that is what web search is. They go without an account and without cookies,
spread across engines rather than building a profile with one company. Your
question, your files and the model's answer never leave; only the search words do.
The model writes those words, so they can hold words from your question.

**If searches stop returning anything**, an engine is rate-limiting your address.
SearXNG spreads queries across engines, which softens this rather than curing it.
Wait, or turn off the offending engine in `settings.yml`.

#### Reading web pages

`web_search` returns titles, URLs and snippets. A snippet is short and often
months old, so Meru also offers `web_fetch`, which reads a whole public page. It
is on by default. It fetches only when the model asks, and it does one of three
things:

- **Read a page.** With no prompt, the model gets the page's text, HTML, PDF or
  plain text, up to 5 MB, 12,000 characters at a time, as `read_file` does for
  your files.
- **Answer from a page.** With a prompt, such as "what is the latest stable
  release, and when did it come out?", the fast model reads up to 48,000
  characters of the page and answers from the page alone. The model sees one
  line such as `From https://go.dev/doc/devel/release (fetched 2026-09-24): ...`
  instead of the whole page.
- **Download a file.** With `save`, the file goes to
  `~/meru-output/downloads/` (under `[skills] output_dir`), up to 50 MB and 2
  minutes. Meru never overwrites: a second `report.pdf` becomes `report-2.pdf`.

A fetch brings the page's text into the conversation and keeps nothing on disk.
A download writes the file to your disk and keeps it there after the chat ends;
the model sees only the path, the size, the type and the first 2,000 characters.
`read_file` and `grep` can then read the file, as long as you index at least one
folder, which turns those tools on. Search never indexes the output folder.

The built-in `web-research` skill tells the model how to use the two tools:
search first, read the one or two best pages with a prompt, prefer the project's
own site, check dates against today, and cite each URL.

**When it asks you.** A web address can carry your data out, as in
`https://example.com/?notes=my+tax+return`. A page the model reads could ask it
to build one. So `web_fetch` runs without asking only for an address that a
search result or your own question showed earlier in the same chat, or for another
page on the same site when the address has no `?` part. For any other address it
asks, offering once or deny, every time. Every download asks
too, even from a search result, because the file stays on your disk; there you
may approve it for the rest of the chat. A scheduled job has nobody to ask, so it
skips those calls.

```text
$ meru "search the web for the latest Go release and tell me its version, with sources"
→ web_search {"query":"latest Go release version"}
✓ web_search 2.2s
→ web_fetch {"url":"https://go.dev/doc/devel/release","prompt":"List the newest releases with their dates."}
✓ web_fetch 1.1s
...
```

**What it costs you.** `merud` itself connects to web sites: the one case where
it connects off this machine without a `remote = true` entry. Each site you read
sees your IP address and a User-Agent that names Meru, and can log that you read
the page. The tool keeps no cookies and uses no proxy. It refuses any address on
this machine or your local network (127.0.0.1, 192.168.x.x, 10.x.x.x, cloud
metadata at 169.254.169.254 and the like), checked after DNS as it connects, and
it follows at most 5 redirects, each checked the same way. So a page can't steer
it at your router or another service on your network.

**To turn it off**, take `web_fetch` out of `[builtin] tools` and restart
`merud` (see [Change settings](#6-change-settings)). `[builtin] tools` is its
only switch. A config that still says `fetch` or `read_pages` under `[web]`
stops `merud` with "web.fetch moved: list web_fetch in [builtin] tools, or
remove it to turn page fetching off"; delete the line and keep or drop
`web_fetch` in `[builtin] tools`. To approve every fetch, even of
a search result, add it to `[builtin] confirm`:

```toml
[builtin]
confirm = ["write_file", "web_fetch"]
```

### meru mcp add

An MCP server gives the model tools. Meru knows two, and `meru mcp list` shows
them:

```sh
meru mcp list
```

| Name | What the model gets | What you need |
| --- | --- | --- |
| `google` | search and read mail and threads, send mail, list and change calendar events, search Drive, read a doc; sending mail and changing an event ask first | a Google OAuth client, `uv`, and the server running, which you start |
| `obsidian` | list, read and search notes; appending asks first | Obsidian running with the Local REST API plugin, and `uv` |

To add one, name it:

```sh
meru mcp add obsidian
```

Meru shows what the server does and offers two paths:

- **d) Do it for me.** Meru asks for each thing the server needs, one at a time.
  It reads an API key without showing it on screen and saves it to
  `~/.meru/secrets.toml`, never to `config.toml`. Then it tries the server (see
  below), shows the exact block it will add to `config.toml`, and writes it only
  after you say yes.
- **s) Show me how.** Meru prints the install step, the block, the file to paste
  it into, and the lines to add to `secrets.toml`. It writes nothing.

`obsidian` is a stdio server: `merud` starts it as a child process. `google` is a server you start yourself (see
[The google entry](#the-google-entry)).

A server outside the catalog takes one command too:

```sh
meru mcp add stdio notes -- /usr/local/bin/notes-mcp --vault ~/notes   # merud starts it
meru mcp add http tasks http://127.0.0.1:8123/mcp                      # you start it
meru mcp add http team https://mcp.example.com/mcp --remote            # on another machine
```

A URL off this machine needs `--remote`. Every call to one of its tools sends
your data to that machine, and Meru says so before it writes anything. `remote`
covers only where `merud` connects. It says nothing about what the server itself
reaches: `google` runs on this machine with `remote = false` and talks to Google.
The older forms, `meru mcp add notes -- <command>` and `meru mcp add tasks --url
<url>`, still work.

An `env` table goes only on a stdio server. `merud` starts no process for a `url`
server, so it has no environment to set, and `merud` refuses to start with this
message:

```text
mcp server "tasks": env does nothing on a url server, because merud doesn't start it. Set the variables where you start the server, or send a key with headers
```

A config written before `network` became `remote` fails to load with "network was
renamed remote". Change the key and restart `merud`.

#### Meru tries the server first

When `merud` runs, Meru asks it to start the server for a moment and list its
tools. `merud` calls none of them. Many servers mark each tool as read-only or as
one that may delete. From those marks Meru proposes which tools the model may use:

```text
Starting notes to see what it offers. The first run of an npx or uvx server downloads it, which can take a minute.
notes-mcp 1.2.0 offers 4 tools.
  allow  search       Searches the notes.  read-only
  ask    write_note   Writes a note.       changes things
  ask    delete_note  Deletes a note.      may delete
  ask    mystery      Does something.      no hint
Enter accepts. Or type changes: -name leaves a tool out, +name allows it without asking, ?name makes it ask.
> -delete_note
```

- `allow` runs without asking. Meru proposes it for a tool the server marks
  read-only.
- `ask` is allowed, and asks you before each call. Meru proposes it for every
  other tool, including one with no mark.
- `off` leaves the tool out: the model never sees it.

For a catalog entry, the catalog's own lists win over the marks, and a tool the
catalog doesn't name starts `off`. A server can mark a tool wrong, so read the list
before you press Enter. Type `-name`, `+name` or `?name` (several at once work)
and Meru shows the table again.

After you say yes to the block, Meru writes it and asks `merud` to reload its
servers, so the new tools work at once:

```text
merud reloaded. No restart needed:
notes  mcp · stdio · connected
  notes.search
  notes.write_note  asks first
  2 of 4 tools allowed
```

When the server doesn't start (a wrong command, or a slow first download), Meru
prints why and offers `r` to try again, `w` to write the entry anyway, or `c` to
cancel. Written anyway, a catalog entry keeps the catalog's lists and a server of
your own allows no tools yet: run `meru tools` once it works, and name the tools
in `config.toml`.

When `merud` isn't running, Meru can't try the server. It writes the catalog's
lists, or an empty `allow` for a server of your own, and the server loads when
`merud` starts.

For a server you start, such as `google`, Meru first checks for one second whether
anything answers at the URL. When nothing does, it skips the try, writes the
catalog's lists, and says `merud` connects on your next question after you start
the server.

#### The google entry

`google` runs [workspace-mcp](https://github.com/taylorwilsdon/google_workspace_mcp)
for Gmail, Calendar, Drive and Docs. You start it and keep it running; `merud`
connects to it at `http://127.0.0.1:8000/mcp` and never starts, restarts or
watches it. `meru mcp add google` prints the command:

```sh
USER_GOOGLE_EMAIL=<your Google address> WORKSPACE_ATTACHMENT_DIR=~/meru-output/attachments \
GOOGLE_OAUTH_CLIENT_ID=<your client ID> GOOGLE_OAUTH_CLIENT_SECRET=<your client secret> \
  uvx workspace-mcp --transport streamable-http --tools gmail calendar drive docs --tool-tier extended
```

`--tool-tier extended` loads 45 tools from the four services. Reading a mail
thread and saving an attachment sit in workspace-mcp's extended tier, so a server
started with `--tool-tier core` lacks them, and `meru tools` warns that the allow
list names a tool google doesn't offer. Restart the server with the right tier and
ask a question that uses Google: that turn lists the server's tools again, the
warning goes, and `merud` needs no restart.

Every Google tool takes your account's address. `USER_GOOGLE_EMAIL` gives the
server a default, so the model never has to supply it. `meru setup user` also asks
for your email and puts it in your profile, which covers a server started without
it.

Before the first run, turn on the Gmail, Calendar, Drive and Docs APIs in Google
Cloud Console and create an OAuth client of type "Desktop app".
[google-setup.md](google-setup.md) walks through every step, from a new Google
Cloud project to the first sign-in. The client ID
and secret go in the server's environment when you start it. They never pass
through Meru, and `secrets.toml` doesn't hold them. The first time the model uses
a Google tool, the server gives you a link to sign in to Google.

Run the command in a terminal, or from `launchd` or `systemd` the way you run
Ollama. The server offers more than 120 tools. `--tools` limits it to four Google
services, and the catalog's `allow` list gives the model nine tools:

| Tool | Asks first |
| --- | --- |
| `search_gmail_messages`, `get_gmail_message_content`, `get_gmail_thread_content` | no |
| `get_gmail_attachment_content` | no |
| `send_gmail_message` | yes |
| `get_events` | no |
| `manage_event` | yes |
| `search_drive_files`, `get_doc_content` | no |

#### Read a mail's attachment

Ask for a file a mail carries, such as "find the hotel folio Dana Reyes sent and
read the PDF". The model finds the mail, then calls
`get_gmail_attachment_content`. The server saves the attachment in the folder
`WORKSPACE_ATTACHMENT_DIR` names, `~/meru-output/attachments`, and reports a
name such as `folio_3f2a9c1e-7b4d-4e8a-9c0f-1a2b3c4d5e6f.pdf`. The model passes
that name to `read_file`, which reads the PDF page by page. The server deletes
each saved file after an hour. `read_file` needs at least one `[index]` folder,
which turns the file tools on. If you changed `[skills] output_dir`, point
`WORKSPACE_ATTACHMENT_DIR` at the `attachments` folder inside it.

If you added `google` before this, do two things:

1. Stop the server, with Ctrl-C in its terminal or by stopping its `launchd` or
   `systemd` job, and start it again with `WORKSPACE_ATTACHMENT_DIR` set, as in
   the command above. For a `launchd` or `systemd` job, add the variable to the
   job's environment; the server expands the `~` itself. Without the variable,
   the server saves attachments in `~/.workspace-mcp/attachments`, where Meru's
   file tools can't read.
2. Add the tool to the end of the server's `allow` list in `~/.meru/config.toml`:

   ```toml
   [[mcp.servers]]
   name  = "google"
   url   = "http://127.0.0.1:8000/mcp"
   allow = [..., "get_doc_content", "get_gmail_attachment_content"]
   ```

   Then restart `merud` (`pkill merud; merud &`). `meru mcp` shows one more
   tool under ALLOWED.

The catalog has no shell server; to let the model run a program, declare it in
`[[commands]]` (see [Local commands](#local-commands)).

#### See each server's state

`meru mcp`, or `meru mcp status`, prints one row per server in `config.toml`:

```text
$ meru mcp
SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM
google     http       connected       124        8        2   127.0.0.1:8000/mcp
obsidian   stdio      not connected     —        5        1   exec: "uvx": executable file not found in $PATH
```

| Column | What it shows |
| --- | --- |
| `SERVER` | the `name` from config |
| `TRANSPORT` | `stdio` or `http` |
| `STATE` | `connected` or `not connected` |
| `TOOLS` | how many tools the server offers; `—` when it isn't connected |
| `ALLOWED` | how many tools your `allow` list names |
| `CONFIRM` | how many allowed tools ask first, from `confirm` and `always_confirm` |
| last field | the URL of an HTTP server, or why the server isn't connected |

`ALLOWED` and `CONFIRM` come from `config.toml`, so they show while a server is
down. `merud` answers from what it already holds and sends nothing to any server,
so the table comes back at once. `meru mcp --json` prints the same rows as JSON,
and `/mcp` in `meru chat` shows the table in a box.

`merud` connects to each server once when it starts. A server that isn't connected
gets one more try at the start of each turn on a tools route: 5 seconds for an
HTTP server, 30 for a stdio server. Nothing retries between turns. So when `google`
shows `not connected`, start it, and ask your question: `merud` connects on that
turn, with no restart. A stdio server that crashed comes back the same way. A
server that fails the try leaves its tools out of that turn, and the model answers
without them.

The same turn asks each connected server for its tools again, so `TOOLS` and the
warnings in `meru tools` follow the server. When you restart `google` with more
tools, `meru mcp` still shows the old count until your next question that uses
tools; after that turn it shows the new count, and the model can call the new
tools. If the server lost `merud`'s session in the restart, that turn connects
again first. The log shows `mcp server tool list changed` with the counts, or
`mcp server failed to list its tools; reconnecting`.

#### See and remove servers

`meru mcp list` ends with the servers in your `config.toml` and what `merud` says
about each:

```text
Your servers:
  obsidian   stdio · uvx · connected · offers 13, 5 allowed
  notes      stdio · notes-mcp · not connected: exit status 1 · 2 allowed in config
```

To take one out:

```sh
meru mcp remove notes          # asks first; --yes skips the question
```

Meru deletes that server's `[[mcp.servers]]` block and the comment lines right
above it, keeps the rest of `config.toml` as it was, and asks `merud` to reload.
Keys stay in `secrets.toml`, since another server may use them.

In chat you can also ask Meru to "connect my Google mail". The model calls the
built-in `configure` tool, which asks you every time, with only "approve once" and
"deny". `configure` won't add a server that needs an API key you haven't saved yet,
because keys never pass through the model; Meru tells you to run `meru mcp add`
instead.

If you edit `config.toml` by hand, restart `merud` to load the change:

```sh
pkill merud; merud &
meru tools
```

`secrets.toml` holds one `name = "value"` line per key. `merud` refuses the file if
other users can read it; `chmod 600 ~/.meru/secrets.toml` fixes that.

### Local commands

To let the model run a program on your machine, declare the whole command in
`~/.meru/config.toml`. The model picks the command and fills in its parameters;
it can't add a flag, chain a second program or reach a shell. This one answers
"what changed in the meru repo this week?":

```toml
[[commands]]
name        = "git-log"
description = "Commits from the past week in one of the user's git repositories"
argv        = ["git", "-C", "{repo}", "log", "--since=1.week", "--oneline"]
timeout     = "10s"

  [commands.params.repo]
  type        = "path"
  under       = "~/repos"
  description = "The repository's folder, such as meru"
```

Restart `merud`, then check what the model gets:

```sh
pkill merud; merud &
meru tools
```

```text
commands  command · connected
  cmd.git-log
    runs: git -C {repo} log --since=1.week --oneline
  1 of 1 tools allowed
```

Ask, and see what ran:

```sh
meru "what changed in the meru repo this week?"
meru log -n 1
```

```text
→ cmd.git-log {"repo":"meru"}
✓ cmd.git-log 25 ms
…
2026-09-24 16:04:19  200416-1625  command  meru.cmd.git-log  ok  -  25 ms  git -C /Users/you/repos/meru log --since=1.week --oneline
```

The rules:

- **Each `{param}` fills one argument.** A value with spaces, quotes or a `;` stays
  one argument, and `merud` runs the program directly, with no shell. A
  placeholder inside a longer argument, such as `"--grep={text}"`, works too.
  Write `{{` and `}}` for a literal brace.
- **Parameters have types.** `string` takes text up to `max_len` bytes (default
  4096), and can't start with `-` when it fills a whole argument, so it can't
  become a flag. `int` takes a whole number, within `min` and `max` if you set
  them. `enum` takes one of `values`. `path` must exist and, with every link
  followed, lie inside `under`; `~` works, and a relative path such as `meru`
  starts at `under`. Every parameter is required.
- **No shells or interpreters.** `merud` refuses to start when `argv[0]` is `sh`,
  `bash`, `zsh`, `fish`, `python`, `perl`, `ruby`, `node`, `env`, `pwsh`,
  `powershell`, `cmd`, `osascript` or the like. A script of your own, named by its
  path, is fine.
- **A short environment.** The program gets `PATH`, `HOME` and `LANG`, plus any
  names in `env_allowlist = ["NAME"]`. It starts in `cwd`, your home folder
  unless set.
- **Limits.** `timeout` defaults to `"30s"`, at most `"300s"`; when it passes,
  `merud` kills the program and everything it started. `merud` keeps the first
  1 MiB of the output and of the errors.
- **Asking first.** `confirm = true` makes each run ask, as a tool in a
  `confirm` list does. Read-only commands can run freely; give anything that
  changes something `confirm = true`. A scheduled job can't ask, so it skips such
  a command and its log shows the call as declined.
- **Routes.** Questions that go to search get the commands without
  `confirm = true`, so a question about an indexed folder such as `meru` can still
  run `git log`. The tools routes get every command.

`merud` checks every entry at startup and refuses to start on a bad one, naming
it: a duplicate name, a placeholder with no parameter, a parameter no placeholder
uses, a `path` with no `under`, an `under` folder that doesn't exist, a timeout
over `"300s"`, and so on. A program missing from `PATH` only gets a warning in
`merud.log`. The config template (`meru config template`) has four starters,
commented out: `git-log`, `git-status`, `search-notes` and `disk-free`.

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

### Check answers on your own files

Unit and end-to-end tests run against fake models and made-up files. They can't
tell whether Meru answers well from *your* files with *your* model. `meru check`
can: it asks a fixed set of questions, grades each answer against what you
expect, and prints PASS or FAIL. Run it after each update and compare with last
time.

Your questions live in `~/.meru/checks.jsonl`, outside the repo, since they
name your own files and work. To start, copy the example and change it:

```sh
cp docs/examples/checks.example.jsonl ~/.meru/checks.jsonl
meru check
```

```text
PASS  direct-capital  direct     direct          1.0s  -
PASS  files-grep      files      search          8.3s  grep
FAIL  web-go          web        tools          31.0s  web_search, web_fetch
      answer lacks all of: 1.27.1
PASS  mcp-vaults      mcp        search+tools    3.7s  obsidian_list_vaults

direct     1/1
files      1/1
web        0/1
mcp        1/1

3 of 4 passed in 44s
```

Each line shows the verdict, the id, the category, the route, the seconds the
turn took and the tools the model asked for. Under a FAIL, one line per reason
says what went wrong. `meru check` exits 0 when every question passes and 1
otherwise.

Nobody watches a check, so `meru check` denies every tool call that asks
first, and prints `approval denied: <tool>` under the line. A question that
needs such a tool fails, since its answer came without it.

**The file.** One question per line, as JSON. `meru check` skips blank lines
and lines that start with `#`. Each line has an `id`, a `category`, the
`question` and a `want`:

```json
{"id": "direct-capital", "category": "direct", "question": "What is the capital of Australia?", "want": {"route": ["direct"], "answer_any": ["Canberra"]}}
```

Questions with the same `"session"` value run in one session, in file order,
so the second can follow up on the first. Every other question starts a new
session:

```json
{"id": "trip", "category": "session", "session": "topics", "question": "When did I visit Lisbon?", "want": {"sources_any": ["lisbon"]}}
{"id": "work", "category": "session", "session": "topics", "question": "What work did I do on the bakery site?", "want": {"sources_none": ["lisbon"]}}
```

Every field in `want` is optional. A question passes when each field it sets
passes. Text matches ignore case.

| Field | Passes when | Example |
| --- | --- | --- |
| `route` | the router picked one of these routes | `"route": ["search", "search+tools"]` |
| `tools` | the turn ran each of these tools | `"tools": ["obsidian", "read_file"]` |
| `no_tools` | the model asked for no tool at all | `"no_tools": true` |
| `answer_any` | the answer holds at least one of these | `"answer_any": ["Canberra"]` |
| `answer_all` | the answer holds every one of these | `"answer_all": ["go.dev", "1.27"]` |
| `sources_any` | a file the search or `search_files` found has one of these in its path | `"sources_any": ["coase", "firm"]` |
| `sources_none` | no file the search or `search_files` found has any of these in its path | `"sources_none": ["lisbon-trip"]` |
| `max_seconds` | the turn took less than this | `"max_seconds": 30` |

A name in `tools` matches a tool the turn ran in one of three ways: the full
name as `meru tools` lists it (`obsidian.obsidian_search_vault`), a server
prefix (`obsidian` matches every obsidian tool), or the name without its prefix
(`git-log` matches `cmd.git-log`). The sources are every file the search put in
the prompt, whether or not the answer cites it.

`meru check` stops before asking anything when a line is bad: JSON that doesn't
parse, a field the format doesn't have, a missing `id`, `category` or
`question`, or an `id` used twice. The message gives the line number.

**Flags.**

```sh
meru check                         # every question in ~/.meru/checks.jsonl
meru check other.jsonl             # another file
meru check --only direct,web-go    # only these categories or ids
meru check --json                  # one JSON record per question, no table
meru check --save                  # also save the results
```

`--save` appends one JSON record per question to
`~/.meru/checks-results/<date>.jsonl` and says so at the end. Each record holds
the run's start time, the question, PASS or FAIL with the reasons, the route,
the tools, the sources, the seconds and the whole answer. To compare two runs,
pick them out by their `run` field; comparing them inside `meru check` is for
later.

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
| `web_search` answers that JSON is off, or `curl` on SearXNG prints HTML | SearXNG answers web pages only. Add `json` under `search: formats:` in `~/srv/searxng/core-config/settings.yml`, then `docker compose restart` (see [Web search](#web-search)). |
| `web_search` says `SearXNG isn't answering on http://127.0.0.1:8888` | The container isn't running. Run `cd ~/srv/searxng && docker compose up -d`, and check that Docker itself runs. `docker compose ps` should show `127.0.0.1:8888->8888/tcp`. |
| `merud` refuses a config value | The message names the key. Fix it in `~/.meru/config.toml`; `meru config template` shows every key, its default and the allowed values. |
| The first answer is slow | Ollama was loading the model. Later answers are fast while `merud` runs, because it keeps the models loaded. |
| Answers are slow and you can't tell why | Stop `merud`, run `merud -v`, ask again and read `~/.meru/merud.log`. The debug lines show the time each stage took; a large `thinking_chunks` count means the model spent the wait reasoning before its first word. |
| "Sorry, I couldn't answer that." | The question hit a limit. `outcome=` on the `turn` line in `~/.meru/merud.log` says which: `timeout` (raise `[agent] turn_timeout`), `cut_off` (raise `[agent] max_output_tokens`) or `gave_up` (the model only called tools; ask again in other words). |
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
