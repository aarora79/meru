# How do I let Meru run more commands on my machine?

Declare each command in `~/.meru/config.toml` as a `[[commands]]` entry, then
restart `merud`. Each entry becomes one tool, `cmd.<name>`, that the model may
call. Meru runs nothing else: this list is the allow list.

## What an entry looks like

```toml
[[commands]]
name        = "top-processes"
description = "The 15 processes using the most CPU or memory. Read the second table: the first table's CPU figures are wrong."
argv        = ["top", "-l", "2", "-s", "1", "-n", "15", "-o", "{sort}", "-stats", "pid,command,cpu,mem,time"]
timeout     = "10s"

  [commands.params.sort]
  type   = "enum"
  values = ["cpu", "mem"]
```

- `name` becomes the tool `cmd.top-processes`.
- `description` is what the model reads when it picks a tool, so say what the
  command tells you, in the words a question would use.
- `argv` is the whole command, one argument per string. You write the program
  and every flag. The model fills in only the `{sort}` placeholder.
- `[commands.params.sort]` says what the model may put in `{sort}`: here `cpu`
  or `mem` and nothing else.

`merud` runs the program itself, with no shell. A value with spaces, quotes or
a `;` stays one argument, so the model can't chain a second program or add a
flag.

## Step by step

1. **Try the command in a terminal.** It must print its answer and exit.
   Programs that redraw the screen until you quit, such as `htop`, `watch`,
   `less` or `top` without `-l`, never finish, so Meru can't use them. Most have
   a one-shot form: `top -l 1` on macOS, `top -b -n 1` on Linux.
2. **Find the program's full path** with `command -v <program>`. `merud`
   started from your shell has your `PATH`. Started by `launchd`, it has only
   `/usr/bin:/bin:/usr/sbin:/sbin`, so a program from Homebrew
   (`/opt/homebrew/bin`) or `~/.local/bin` needs its full path as the first
   element of `argv`.
3. **Write the entry** in `~/.meru/config.toml`. Start from a sample:
   `meru config template` prints 25 of them, commented out. Copy the ones you
   want and delete the `# ` at the start of each line.
4. **Restart `merud`.** It reads `[[commands]]` only when it starts:

   ```sh
   pkill merud; merud &
   ```

   Under `launchd`, run `launchctl kickstart -k gui/$(id -u)/com.meru.merud`
   instead. `merud` refuses to start on a bad entry and names it, such as a
   placeholder with no parameter or an `under` folder that doesn't exist.
5. **Check what the model gets:**

   ```sh
   meru tools
   ```

   ```text
   commands  command · connected
     cmd.top-processes
       runs: top -l 2 -s 1 -n 15 -o {sort} -stats pid,command,cpu,mem,time
   ```

6. **Ask, then see what ran:**

   ```sh
   meru "what is using the most memory right now?"
   meru log -n 1
   ```

   `meru log` shows the exact program and arguments that ran.

## Parameter types

Pick the narrowest type that works. Each one is required.

| Type | The model may give | Keys |
| --- | --- | --- |
| `enum` | one of a fixed list | `values` |
| `int` | a whole number | `min`, `max` |
| `path` | a file or folder that exists inside `under`, links followed | `under` (required) |
| `string` | text; it can't start with `-` when it fills a whole argument | `pattern`, `max_len` (default 4096) |

Give a `string` a `pattern` whenever you can. `pattern = "[A-Za-z0-9 ._-]+"`
takes a process name and refuses `;`, `/` and quotes. The whole value must
match.

Other keys on an entry: `timeout` (default `"30s"`, at most `"300s"`), `cwd`
(where the program starts; your home folder unless set), `confirm = true` (ask
you before each run) and `env_allowlist` (variables passed besides `PATH`,
`HOME` and `LANG`).

## Keep it safe

- **Only read, or ask first.** A command that changes something, such as
  sending, deleting or commenting, gets `confirm = true`. Approve each call
  once; a session approval lets every later call through.
- **Never let the model write code.** `merud` refuses a shell or interpreter as
  the program: `sh`, `bash`, `zsh`, `python`, `perl`, `ruby`, `node`, `env`,
  `osascript` and the like. With `sed` and `awk`, write the script yourself in
  `argv` and let the model fill in only numbers and a file. A pattern of the
  model's own inside a `sed` script could end the address and add a `w` command
  that writes a file; inside `awk`, it could call `system()`.
- **Watch for flags that run programs.** `find -exec`, `git -c core.pager=…`,
  `tar --checkpoint-action` and `rsync -e` all start other programs. Fixed flags
  you wrote are fine. Never put a placeholder where one of those could land.
- **Point `under` at the folders the command needs.** `read_file` skips secret
  files such as `.env`, `id_rsa` and `~/.meru/secrets.toml`; a command doesn't.
  `under = "~/Documents"` is safer than `under = "~"`.
- **Keep keys out.** A command gets nothing from `secrets.toml`. Tools such as
  `gh` keep their own login in your keychain.

## Samples you can copy

`meru config template` holds these, all read-only:

| Group | Tools |
| --- | --- |
| Files and repositories | `git-log`, `git-status`, `search-notes`, `disk-free` |
| Machine facts (macOS) | `kernel`, `macos-version`, `hardware-summary`, `system-report`, `battery`, `disk-list`, `uptime` |
| Snapshots (macOS) | `system-load`, `top-processes`, `find-process`, `memory-free` |
| `sed` and `awk` | `file-lines`, `count-lines`, `csv-column`, `csv-sum` |
| GitHub | `gh-prs`, `gh-pr`, `gh-issues`, `gh-issue`, `gh-runs`, `gh-repos` (see [github.md](github.md)) |

With these in place, ask in plain words rather than naming a program. "What's
using the most CPU?" picks `top-processes`; "run htop" may not, since no tool
by that name exists.

More detail: [running.md, Local commands](../running.md#local-commands) and
[ARCHITECTURE.md, Local commands](../../ARCHITECTURE.md#local-commands).
