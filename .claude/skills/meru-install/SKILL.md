---
name: meru-install
description: Use when someone wants to install, update or remove Meru on a Mac from a GitHub release. Walks them step by step through the machine check, Homebrew, gh, Ollama and the models, the meru and merud programs and Meru.app, first setup, starting merud at login, and optional Gmail, Calendar, Drive and GitHub tools. Asks before each step that installs or changes anything.
---

# Install Meru from a release

You are helping a person install Meru, a personal assistant that runs on their
own Mac, from a release on GitHub. Apple silicon Macs get everything. Intel
Macs get the command-line programs `meru` and `merud` only; the release has no
`Meru.app` for them.

[reference.md](reference.md) holds the long files this skill writes: property
lists, the Google start script, and the commands behind the optional steps.
Read the section you need when you reach it.

On a Mac with Apple silicon, the release also has a disk image,
`Meru-vX.Y.Z-macos-arm64.dmg`, holding "Install Meru.app", which does these
steps in a window. Mention it once at the start: a person who'd rather click
than type can use it instead, and you stop there.

## How to run each step

- **Ask first.** Before any command that installs, downloads, writes or deletes
  something, show the exact command, say in one line what it does, and wait for
  a yes. Commands that only read, such as `uname -m`, need no yes.
- **Run it, then check it.** Run the command yourself, read its output, and run
  the check the step gives. Say what you found in a sentence. Stop and explain
  when a check fails; don't carry on past a failure.
- **Hand interactive commands to the person.** `gh auth login`, `meru setup`,
  `meru setup user` and the Homebrew installer ask questions or a password.
  Ask the person to run those in their own Terminal window and tell you when
  they finish.
- **Keep secrets out of the chat.** Never print a secret, repeat one back, or
  write one to a file other than the one this skill names for it. When a step
  needs a secret, offer the Terminal route in that step, which keeps it out of
  the chat altogether.
- **Use placeholders** such as `<your Google address>` in anything you show,
  until the person gives you the real value.
- **Leave `~/.meru` alone** unless a step says otherwise. It holds the
  person's chats, memories and settings.

## 1. Check the machine

Run these, which only read:

```sh
uname -m                                   # arm64 = Apple silicon, x86_64 = Intel
sw_vers -productVersion                    # macOS version; Meru.app needs 11 or later
sysctl -n hw.memsize | awk '{print $1/1073741824 " GB"}'
df -h ~ | tail -1                          # free disk space in the home volume
```

Then recommend models by memory. The figures come from ARCHITECTURE.md,
"Models we tried" (September 2026, an M4 Max with 64 GB):

| Memory | Recommend | Download |
| --- | --- | --- |
| under 32 GB, 16 GB included | the `lite` profile: MiniCPM5-2B answers and routes, `nomic-embed-text` searches your files | about 2 GB |
| 32 GB | `lite`, plus `gemma4:26b-a4b-it-qat` as the answer model | about 17 GB |
| 48 GB | `lite`, plus `qwen3.6:35b` (Qwen 3.6 35B at 4 bits) as the answer model | about 25 GB |
| 64 GB or more | `lite`, plus `qwen3.6:35b-a3b-mxfp8` as the answer model | about 40 GB |

The model's file, what Ollama holds for a 32,768-token context, and the router
must fit beside macOS and the person's apps. On the 64 GB test Mac, Ollama held
36 to 52 GiB for `qwen3.6:35b-a3b-mxfp8` and 27 to 40 GiB for
`gemma4:26b-mxfp8`, so neither suits a 48 GB Mac. Ollama loaded `qwen3.6:35b`
in 23 GB with a 32,768-token context, about 27 GB with the router, and
`gemma4:26b-a4b-it-qat` in 15 GB, about 18 GB with the router, which leaves a
32 GB Mac room for its apps.

Tell the person what each choice costs. The figures come from a benchmark of
50 private tasks, run three times per model:

- **MiniCPM5-2B** (1.6 GB) routes every question and answers on `lite`. It
  passed 102 of 150, nearly all the direct questions and file searches, but
  only 6 of 36 tasks that need several tools.
- **`qwen3.6:35b-a3b-mxfp8`** (38 GB) answered fastest: a median of 6.8 seconds
  a task, and 133 of 150 passed.
- **`gemma4:26b-mxfp8`** (28 GB), for 64 GB or more, passed 138 of 150 and the
  most tasks that need several tools, but took a median of 16.3 seconds a task.
  Offer it to someone who asks Meru for work across mail, calendar and files and
  will wait.
- **`qwen3.6:35b`** (23 GB) is the same Qwen model at 4 bits. It passed 133 of
  150, as many as the 8-bit build, with a median of 10.6 seconds a task.
- **`gemma4:26b-a4b-it-qat`** (15 GB) is Gemma 4 at 4 bits. It passed 120 of
  150 but only 13 of 36 tasks that need several tools, and took a median of
  17.6 seconds to its first word.
- **`gemma3:12b`** (8.1 GB) reads pictures and answers from what it knows, but
  Ollama gives it no tools, so it can't read mail, notes, the web or files.
  Offer it only to someone who wants that.

Check the free disk space against the download, with room to spare.

## 2. Prerequisites: Homebrew and gh

Check what is there:

```sh
command -v brew; command -v gh; command -v ollama
```

**Homebrew**, if `brew` is missing. Ask first, then have the person run the
command from <https://brew.sh> in their own Terminal, since it asks for their
password:

```sh
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

The installer prints two lines that put `brew` on the `PATH`; ask the person to
run them too. Check with `brew --version`.

**gh**, GitHub's command-line tool. The Meru repository is public, so the
downloads work without it, but the GitHub step later uses it. Ask, then:

```sh
brew install gh
```

Ask the person to run `gh auth login` in their Terminal (GitHub.com, HTTPS,
log in with a browser). Check:

```sh
gh auth status
gh release list --repo aarora79/meru --limit 3
```

If the release list fails with "Could not resolve to a Repository", the account
can't see the repository; the person needs access from its owner.

## 3. Ollama and the models

Meru needs Ollama 0.12.11 or later. If `ollama` is missing, offer two ways and
let the person pick:

- the Ollama app from <https://ollama.com/download>, which the person downloads
  and drags to Applications, then opens once; or
- Homebrew: `brew install ollama`, then `brew services start ollama` to run it
  at login.

Check that it answers:

```sh
curl -s http://127.0.0.1:11434/api/version
```

Pull the models chosen in step 1. Ask first, and say how large each is:

```sh
ollama pull hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M
ollama pull nomic-embed-text
ollama pull gemma4:26b-a4b-it-qat   # 32 to 47 GB
ollama pull qwen3.6:35b             # 48 to 63 GB
ollama pull qwen3.6:35b-a3b-mxfp8   # 64 GB or more
ollama pull gemma3:12b              # only if they asked for it
```

Check with `ollama list`.

`merud` asks Ollama for 32,768 tokens of context on every call, so Ollama
needs no setting for the larger models. Once a model has answered a question,
`ollama ps` shows 32768 in its `CONTEXT` column.

## 4. Meru: download, check, install

Pick the release. By default take the latest; the person may name a tag such
as `v0.4.1`:

```sh
gh release list --repo aarora79/meru --limit 5
TAG=$(gh release view --repo aarora79/meru --json tagName -q .tagName)   # or TAG=v0.4.1
```

Download into a fresh temporary folder. On Apple silicon take `darwin-arm64`
and the app; on Intel take `darwin-amd64` and skip the app:

```sh
DL=$(mktemp -d)
gh release download "$TAG" --repo aarora79/meru --dir "$DL" \
  --pattern "meru-$TAG-darwin-arm64.tar.gz" --pattern "Meru-$TAG-macos-arm64.zip" --pattern SHA256SUMS
cd "$DL" && shasum -a 256 -c SHA256SUMS --ignore-missing
```

Every file must say `OK`. Stop if any says `FAILED`: that file broke on the way
or differs from the one GitHub holds. Delete the folder and download again.

**Install the programs** in `~/.local/bin`. Ask, then:

```sh
tar -xzf "meru-$TAG-darwin-arm64.tar.gz"
mkdir -p ~/.local/bin
cp "meru-$TAG-darwin-arm64/meru" "meru-$TAG-darwin-arm64/merud" ~/.local/bin/
```

Check that the folder is on the `PATH` with `command -v meru`. If that prints
nothing, offer to add it to `~/.zshrc`, then have the person open a new
Terminal:

```sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
```

**Install the app** (Apple silicon only) in `/Applications`, or in
`~/Applications` if the person prefers. Ask, then:

```sh
ditto -x -k "Meru-$TAG-macos-arm64.zip" /Applications/
```

Explain the next part in plain words. Meru.app isn't signed or notarized: the project
has no Apple Developer account. If the zip carries the quarantine mark, macOS
refuses to open the app and says it can't check it for malicious software.
Check for the mark:

```sh
xattr -r /Applications/Meru.app | grep -c com.apple.quarantine
```

If the count is above 0, and only if the person says yes, clear it:

```sh
xattr -dr com.apple.quarantine /Applications/Meru.app
```

The other route is to open the app, click **Done**, then choose **Open Anyway**
in System Settings, Privacy & Security.

## 5. Configure and start

**First setup.** Ask the person to run `meru setup --main <model>` in their
Terminal, with the answer model from step 1, for example
`meru setup --main qwen3.6:35b` on 48 GB. On a Mac that gets `lite` alone, plain
`meru setup` and the answer `lite` do. It checks Ollama, pulls the models (they
are already there, so it finishes fast), asks which folders to index, and
writes `~/.meru/config.toml` with that model as `[models] main`, so Meru answers
with it from the first question. Its web-search step offers to have `merud`
run SearXNG in Docker once `merud` runs; type `s` to skip it if they don't run
Docker, and turn it on later with `meru mcp set searxng enabled=true`. Its tools
step asks about Obsidian and Google, which `merud` installs and runs; they can
skip both now.

**Start `merud` at login** with the launchd file from the repository's
`deploy/launchd/`, pointed at `~/.local/bin`. reference.md, "Start merud at
login", has the commands. Ask first, run them, then check:

```sh
launchctl print gui/$(id -u)/com.meru.merud | grep -E 'state|last exit'
meru ping
meru tools
```

`meru ping` should answer that `merud` is up. If it can't connect, read
`~/.meru/merud.err.log`: a missing model or a config error shows there.

**About you.** Offer `meru setup user`, run by the person in their Terminal. It
asks their name, email and how they like answers, and saves each as a memory.

**The answer model.** `meru setup --main` already wrote it. If they had a
`config.toml` before, setup leaves it alone: open Meru.app, go to Settings,
Models, and click **Use for answers** on the model's card, and `merud` takes it
with no restart. Without the app, set `main = "<model>"` under `[models]` in
`~/.meru/config.toml` and restart `merud` with
`launchctl kickstart -k gui/$(id -u)/com.meru.merud`. Thinking stays off unless
`[models] think = true`, as in the benchmark.

## 6. Optional: Gmail, Calendar and Drive

Ask whether they want Meru to read their Google mail, calendar and Drive. If
not, skip to step 7. If yes, say it takes about 20 minutes in a browser, and
follow [docs/google-setup.md](../../../docs/google-setup.md) with them, one
step at a time. Read that file now; it has every screen. `merud` installs and
runs the Google server itself, as a connector, so they need no uv, start
script or launchd job. In short:

1. **Google Cloud.** They make a project named `meru`, turn on the Gmail,
   Calendar, Drive and Docs APIs, set up the consent screen (External), add
   their own address as a test user, and create an OAuth client of type
   **Desktop app**. Give them the links from google-setup.md steps 1 to 5 and
   wait while they click through.
2. **Turn the connector on.** Ask for the client ID (it ends in
   `.apps.googleusercontent.com`; it isn't secret) and their Google address.
   The secret, which starts with `GOCSPX-`, must stay out of this chat: ask
   the person to run this in their own Terminal, with their values in place of
   the angle brackets. `meru` asks for the secret without showing it, and
   `merud` saves it in `~/.meru/secrets.toml`, which only they can read:

   ```sh
   meru mcp set google enabled=true email=<their address> client_id=<their client ID> client_secret
   ```

   Meru.app's Settings, Connections, has the same form on Google's card. If the
   person pastes the secret here anyway, never repeat it, and ask them to use
   the command or the form instead of passing it on yourself.
3. **Wait for the install.** The command prints each step while `merud`
   downloads its own uv, Python and the server, which takes a minute or two
   the first time, and ends with "Google needs you to sign in." and a link
   after `Sign in here:`.
4. **First sign-in.** They open that link, or click **Sign in to Google** on
   the card in Settings. Google warns "Google hasn't verified this app": that
   is their own project, so they click **Advanced**, then **Go to Meru
   (unsafe)**, tick every box and click **Continue**. `meru mcp status` then
   shows `google` as `ok`.

If they already run the Google server by hand, from a start script or the
launchd job `com.meru.workspace-mcp`, `meru mcp` says "set up by hand", and
`meru mcp adopt google` moves it over; it shows every change and asks before
it makes any. If something stops working later, `meru mcp fix google` asks for
what is missing or checks the server again.

Tell them that while the project is in Testing mode, Google ends the sign-in
every 7 days; google-setup.md, "Signing in again every 7 days", shows how to
publish the project and stop that.

## 7. Optional: GitHub commands

Ask whether Meru should read their GitHub pull requests, issues and CI runs
through `gh`. These commands only read. `merud` started by launchd can't find
`gh` on its short `PATH`, so each command needs `gh`'s full path. reference.md,
"GitHub commands", has the command that copies the block from
`meru config template` into `~/.meru/config.toml` with that path filled in.
Show the block, ask, append it, restart `merud`, and check that `meru tools`
lists the `cmd.gh-*` tools.

## 8. Check that it works

- Ask the person to open Meru.app (or run `meru chat`) and ask "which model are
  you using?". The answer comes from the `about_meru` tool and should name the
  model they chose. Settings, About shows the version, such as `v0.4.1`.
- Ask them for a question whose answer sits in a file in one of the folders
  they indexed. `meru index -status` shows how far indexing got. The answer
  should list the file under Sources.
- If they set up Google, ask about their calendar for tomorrow.

Tell them where to read more: `docs/running.md` in the repository.

## 9. Update to a newer release

Run step 4 again with the new tag, then:

1. Ask the person to quit Meru.app.
2. Copy `meru` and `merud` over the old ones, and unzip the app over the old
   one (remove `/Applications/Meru.app` first, with their yes, so no old file
   stays behind).
3. Restart `merud`: `launchctl kickstart -k gui/$(id -u)/com.meru.merud`.
4. Check with `meru ping`, and the version under Settings, About.

Meru never checks for updates on its own. The person decides when to update.

## 10. Uninstall

Ask before each line, and show what each removes. reference.md, "Uninstall",
lists the full set of commands:

1. Stop and remove the launchd jobs: `merud`, and the Google server and the
   Ollama context job if an older install made them. `merud`'s own
   connectors stop with it; their programs live in `~/.meru/runtime`, and
   Meru's SearXNG container is `meru-searxng` (`docker rm -f meru-searxng`,
   with a yes).
2. Remove `~/.local/bin/meru`, `~/.local/bin/merud` and `/Applications/Meru.app`.
3. Offer to remove the models with `ollama rm`, and Ollama itself.
4. **`~/.meru` last, and only after an explicit yes** to a question that says
   it deletes every chat, every memory, the settings and the search index, and
   can't be undone. Offer to copy it elsewhere first. Never remove it as part
   of an update.
5. The Google files: `~/.google_workspace_mcp/` holds the Google sign-in, and
   `~/.config/workspace-mcp/`, from an older install, holds a client secret. Remove them only with a
   yes. The Google Cloud project stays until they delete it in the console.
