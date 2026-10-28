---
name: meru-install
description: Use when someone wants to install, update or remove Meru from a GitHub release on a Mac or on Linux. Finds the system, runs the right installer (the Mac disk image, install.sh or install-linux.sh), and stays with the person until merud, the meru command, meru chat and the desktop app all answer, then hands them to the desktop app's Settings for the rest. Asks before each step that installs or changes anything.
---

# Install Meru from a release

You are helping a person install Meru, a personal assistant that runs on their
own computer, from a release on GitHub. Each system has its own installer:

| System | Installer | Desktop app |
| --- | --- | --- |
| Mac with Apple silicon | the disk image, "Install Meru.app" in a window, or `install.sh` in Terminal | Meru.app |
| Intel Mac | `install.sh` | none; `meru` and `meru chat` only |
| Linux on x86-64 or arm64 | `install-linux.sh` | `meru-desktop`, on x86-64 once the release has it |
| Windows | none; send them to `docs/running.md` and stop | none |

Your job ends when four things answer: `merud`, a question through `meru`,
`meru chat`, and the desktop app. Then you hand over (step 6). Don't set up
mail, web search or folders yourself: the desktop app's Settings does those.

[reference.md](reference.md) holds the longer commands: installing by hand when
an installer fails, the GitHub commands, and removing Meru. Read the section
you need when you reach it.

## How to run each step

- **Ask first.** Before any command that installs, downloads, writes or deletes
  something, show the exact command, say in one line what it does, and wait for
  a yes. Commands that only read, such as `uname -m`, need no yes.
- **Hand the installers to the person.** Both install scripts and `meru setup`
  ask questions and may ask for a `sudo` password; the Mac installer is a
  window. Your shell has no terminal for them to answer in. Give the person the
  command to paste into their own terminal, say what it will ask, and wait for
  them to say it finished. Ask them to paste any error it printed.
- **Then check it yourself.** After each step, run the checks it gives, read
  the output, and say what you found in a sentence. When a check fails, find
  the cause and fix it, with a yes, before you move on.
- **Keep secrets out of the chat.** Never print a secret, repeat one back, or
  write one anywhere. Settings in the desktop app takes them in a form.
- **Leave `~/.meru` alone** unless a step says otherwise. It holds the
  person's chats, memories and settings.

## 1. Find the system

Run these, which only read:

```sh
uname -s; uname -m                  # Darwin or Linux; arm64, x86_64 or aarch64
df -h ~ | tail -1                   # free space; the models need 2.3 GB to 40 GB
command -v meru merud ollama        # what is there already
ls ~/.meru/config.toml 2>/dev/null  # a config from an earlier install
```

**On a Mac**, add:

```sh
sw_vers -productVersion                                   # Meru.app needs macOS 11 or later
sysctl -n hw.memsize | awk '{print $1/1073741824 " GB"}'
```

**On Linux**, add:

```sh
. /etc/os-release && echo "$PRETTY_NAME"   # tested on Ubuntu 24.04
nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>/dev/null
[ -d /run/systemd/system ] && echo systemd || echo "no systemd"
```

Tell the person what you found and which installer fits. If Meru is already
installed, this is an update: go to step 7.

The installers pick the answer model themselves, so you only tell the person
what to expect. Every install gets the `lite` models, about 2.3 GB:
MiniCPM5-2B routes and answers, and `embeddinggemma-2:440m` searches files and
images. A Mac with more memory also gets a larger answer model:

| Memory | Answer model | Extra download |
| --- | --- | --- |
| under 32 GB | MiniCPM5-2B, from `lite` | none |
| 32 GB | `gemma4:26b-a4b-it-qat` | about 15 GB |
| 48 GB | `qwen3.6:35b` | about 23 GB |
| 64 GB or more | `qwen3.6:35b-a3b-mxfp8` | about 38 GB |

On Linux, `meru setup` reads the graphics card's memory and offers the largest
model that fits it; with no NVIDIA card it keeps `lite`. `docs/running.md`, "On
Linux with an NVIDIA card", has that table, and "Models we tried for answers"
has the benchmark behind both.

On Linux with no systemd, such as a container or WSL, the script can't start
`merud` at boot; step 4 covers starting it by hand.

## 2. Ollama first, if they want to pick how

Meru needs Ollama 0.40.5 or later. Both scripts install it when it is missing:
`install.sh` with Homebrew or from ollama.com, `install-linux.sh` with
Ollama's own script, which uses `sudo`. A person who wants the Ollama app on a
Mac can install it from <https://ollama.com/download> first and open it once.
Either way, nothing to do here unless they ask.

## 3. Run the installer

### Mac, in a window (Apple silicon)

Offer to download the disk image to `~/Downloads` and open it. Ask, then:

```sh
curl -fL -o ~/Downloads/Meru-macos-arm64.dmg \
  https://github.com/aarora79/meru/releases/latest/download/Meru-macos-arm64.dmg
open ~/Downloads/Meru-macos-arm64.dmg
```

Ask the person to open "Install Meru.app" in the window that appears. Apple
hasn't checked Meru, so the first time macOS refuses: they click **Done**, then
**Open Anyway** in System Settings, Privacy & Security. The installer walks
through eleven steps and says what each downloads; they can skip any but About
you. Wait for them to reach the last screen, then go to step 4.

### Mac, in Terminal

Ask the person to paste this into Terminal:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
```

It lists what it installs and asks once: everything, one step at a time, or
quit. It puts `meru` and `merud` in `~/.local/bin` and Meru.app in
`/Applications`, then offers Ollama, the models, `meru setup` and starting
`merud` at login.

### Linux

`install-linux.sh` needs `curl`; `command -v curl` checks, and
`sudo apt install curl` adds it. Ask the person to paste this into a terminal:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install-linux.sh | bash
```

It lists what it installs and asks once. It puts `meru` and `merud` in
`~/.local/bin`, then offers the desktop app (with GTK 4 and WebKitGTK 6.0 from
apt), Ollama, `meru setup`, and `merud` as a systemd user service that starts
at boot. If it says the release has no Linux desktop app yet, the desktop app
arrives on the release a little after the rest; they can run the script again
later to add it.

`MERU_VERSION=v0.6.0` in front of `bash` installs a given release instead of
the latest, on either script.

## 4. Check each part, and fix what doesn't answer

Go down this list in order. Each part needs the one before it.

**The programs.**

```sh
command -v meru merud
```

If the shell can't find them, `~/.local/bin` isn't on the `PATH` yet: the
script added it to `~/.zshrc` or `~/.bashrc`, which counts only in a new
terminal. Ask the person to open one. Until then, call them as
`~/.local/bin/meru`.

**Ollama and the models.**

```sh
curl -s http://127.0.0.1:11434/api/version
ollama list
```

No answer means Ollama isn't running. On a Mac, ask them to open the Ollama
app, or run `brew services start ollama`. On Linux, `systemctl status ollama`;
with no systemd, `nohup ollama serve >~/.ollama/serve.log 2>&1 &`. An empty
`ollama list` means `meru setup` never ran: ask them to run `meru setup` in
their terminal.

**A config.** `ls ~/.meru/config.toml`. If it's missing, `meru setup` didn't
finish; ask them to run it.

**`merud`.**

```sh
meru ping
```

If it can't connect, look at the service and its log:

| | Mac | Linux |
| --- | --- | --- |
| Is it running? | `launchctl print gui/$(id -u)/com.meru.merud \| grep -E 'state\|last exit'` | `systemctl --user status merud` |
| What it said | `tail -30 ~/.meru/merud.err.log` | `journalctl --user -u merud -n 30` |
| Restart | `launchctl kickstart -k gui/$(id -u)/com.meru.merud` | `systemctl --user restart merud` |

The log names the problem: a model Ollama doesn't have (pull it with
`ollama pull`), a config line it refuses, or Ollama down. If no service exists,
the person said no to that step; ask, then reference.md, "Start merud at
login" or "Start merud on Linux", has the commands. With no systemd, ask
them to run `merud &` in a terminal they leave open.

**A question through `meru`.**

```sh
meru "which model are you using?"
```

The answer should call itself Meru and name the answer model from
`~/.meru/config.toml`. The first answer can take a minute while Ollama loads
the model.

**`meru chat`.** It needs a terminal, so ask the person to run `meru chat`,
ask any question, and quit with `ctrl+d` or `/exit`. Ask what they saw. A
blank screen or an error line goes back to the `merud` check above.

**The desktop app.**

- Mac with Apple silicon: `open -a Meru`.
- Linux: Meru in the app menu, or `meru-desktop` in a terminal. If it doesn't
  open, `ldd ~/.local/bin/meru-desktop | grep "not found"` names a missing
  library; `sudo apt install libgtk-4-1 libwebkitgtk-6.0-4` adds the usual two.

On first open the app shows the Setup screen if setup is unfinished, and the
chat screen after that. Ask the person to send a question from it and tell
you the answer arrived.

Intel Macs, and Linux releases without the desktop app, stop at `meru chat`.

## 5. When an installer fails

Ask the person for the last lines it printed. Common causes:

- **A checksum doesn't match.** The download broke on the way. Run the
  installer again.
- **`sudo` refused**, or they said no to Ollama. Install Ollama yourself from
  <https://ollama.com/download>, then run the installer again; it skips what
  is done.
- **The models didn't download.** Ollama wasn't running. Start it (step 4),
  then `meru setup`.
- **No release for this processor.** Meru ships for Apple silicon and Intel
  Macs, and for Linux on x86-64 and arm64. Anything else builds from source:
  `docs/running.md`, "Build from source".

If an installer keeps failing, reference.md, "Install by hand", does the same
work one command at a time.

## 6. Hand over to the desktop app

Once everything in step 4 answers, say so in a short list: `merud` running and
starting at login or boot, the answer model, `meru`, `meru chat` and the
desktop app. Then tell the person the desktop app's Settings does the rest
from here, with no files to edit:

- **Models** to switch the answer model.
- **Folders** to choose what Meru may read.
- **Connections** for web search, Obsidian, Gmail, Calendar and Drive, speech
  to text, and other tools; each card says what it needs and has a Fix button.
- **Skills** and **Privacy**, and **About** to run setup again.

`docs/running.md` covers each in full, and `docs/google-setup.md` walks
through the Google project. Stop there unless they ask for more. If they want
Meru to read GitHub through `gh`, reference.md, "GitHub commands", has the
steps.

## 7. Update to a newer release

Run the same installer again (step 3). Each one keeps `~/.meru/config.toml`
and the person's data, replaces the programs and the app, and restarts
`merud`. Ask them to quit the desktop app first. Then run the checks in step
4, and check the version under Settings, About in the desktop app.

Meru never checks for updates on its own. The person decides when to update.

## 8. Uninstall

Ask before each line, and show what each removes. reference.md, "Uninstall",
lists the commands for each system:

1. Stop and remove the `merud` service: the launchd job on a Mac, the systemd
   user unit on Linux. `merud`'s connectors stop with it; their programs live
   in `~/.meru/runtime`, and Meru's SearXNG container is `meru-searxng`.
2. Remove `meru`, `merud` and the desktop app: `/Applications/Meru.app`, or
   `meru-desktop` with its menu entry and icon on Linux.
3. Offer to remove the models with `ollama rm`, and Ollama itself.
4. **`~/.meru` last, and only after an explicit yes** to a question that says
   it deletes every chat, every memory, the settings and the search index, and
   can't be undone. Offer to copy it elsewhere first. Never remove it as part
   of an update.
5. The Google sign-in in `~/.google_workspace_mcp/`, only with a yes. The
   Google Cloud project stays until they delete it in the console.
