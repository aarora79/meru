# meru-install: reference

The longer commands SKILL.md points to. Show each command to the person and
get a yes before you run anything that writes or deletes.

## Install by hand

For when an installer keeps failing. This does what the scripts do, one
command at a time. Set the release and the platform first:

```sh
TAG=$(curl -fsSL https://api.github.com/repos/aarora79/meru/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4)
PLAT=darwin-arm64     # or darwin-amd64, linux-amd64, linux-arm64
```

Download into a fresh folder and check every file against `SHA256SUMS`:

```sh
DL=$(mktemp -d) && cd "$DL"
base=https://github.com/aarora79/meru/releases/download/$TAG
curl -fsSLO "$base/meru-$TAG-$PLAT.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
shasum -a 256 -c SHA256SUMS --ignore-missing   # Linux: sha256sum -c SHA256SUMS --ignore-missing
```

Every file must say `OK`. Stop if any says `FAILED`, delete the folder and
download again. Then install the programs:

```sh
tar -xzf "meru-$TAG-$PLAT.tar.gz"
mkdir -p ~/.local/bin
cp "meru-$TAG-$PLAT/meru" "meru-$TAG-$PLAT/merud" ~/.local/bin/
```

**Meru.app** (Apple silicon only):

```sh
curl -fsSLO "$base/Meru-$TAG-macos-arm64.zip"
shasum -a 256 -c SHA256SUMS --ignore-missing
ditto -x -k "Meru-$TAG-macos-arm64.zip" /Applications/
```

Meru.app isn't signed or notarized, since the project has no Apple Developer
account. If `xattr -r /Applications/Meru.app | grep -c com.apple.quarantine`
prints more than 0, macOS refuses to open it. With a yes, clear the mark with
`xattr -dr com.apple.quarantine /Applications/Meru.app`, or have the person
click **Done**, then **Open Anyway** in System Settings, Privacy & Security.

**The Linux desktop app** (x86-64, once the release has it):

```sh
curl -fsSLO "$base/meru-desktop-$TAG-linux-amd64.tar.gz"
curl -fsSLO "$base/SHA256SUMS-desktop-linux"
sha256sum -c SHA256SUMS-desktop-linux --ignore-missing
tar -xzf "meru-desktop-$TAG-linux-amd64.tar.gz"
src="meru-desktop-$TAG-linux-amd64"
mkdir -p ~/.local/share/applications ~/.local/share/icons/hicolor/512x512/apps
install -m 0755 "$src/meru-desktop" ~/.local/bin/
install -m 0644 "$src/meru.png" ~/.local/share/icons/hicolor/512x512/apps/meru.png
sed "s|__HOME__|$HOME|g" "$src/meru.desktop" > ~/.local/share/applications/meru.desktop
sudo apt install libgtk-4-1 libwebkitgtk-6.0-4
```

Then ask the person to run `meru setup` in their terminal, and start `merud`
with one of the next two sections.

## Start merud at login

On a Mac. The release holds only the programs, so fetch the launchd file from
the repository at the same tag. It names `__HOME__/go/bin/merud`; the first
`sed` expression points it at `~/.local/bin`, and the second fills in the home
folder, since launchd doesn't expand `~` or `$HOME`:

```sh
mkdir -p ~/.meru ~/Library/LaunchAgents
curl -fsSL "https://raw.githubusercontent.com/aarora79/meru/$TAG/deploy/launchd/com.meru.merud.plist" \
  | sed -e "s|__HOME__/go/bin/merud|$HOME/.local/bin/merud|" -e "s|__HOME__|$HOME|g" \
  > ~/Library/LaunchAgents/com.meru.merud.plist
grep -c "$HOME/.local/bin/merud" ~/Library/LaunchAgents/com.meru.merud.plist   # expect 1
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.meru.merud.plist
```

If `bootstrap` says the service is already loaded, an older job exists. Remove
it with `launchctl bootout gui/$(id -u)/com.meru.merud`, then bootstrap again.

## Start merud on Linux

As a systemd user service. The repository's unit runs `~/go/bin/merud`; the
`sed` points it at `~/.local/bin`, and `%h` is systemd's name for the home
folder:

```sh
mkdir -p ~/.config/systemd/user ~/.meru
curl -fsSL "https://raw.githubusercontent.com/aarora79/meru/$TAG/deploy/systemd/merud.service" \
  | sed "s|%h/go/bin/merud|%h/.local/bin/merud|" > ~/.config/systemd/user/merud.service
systemctl --user daemon-reload
systemctl --user enable --now merud
```

A user service stops when the person logs out. To start it at boot, before
anyone logs in, as a server needs: `sudo loginctl enable-linger "$USER"`, with
a yes.

## GitHub commands

`merud` started by launchd or systemd has a short `PATH`, so each command names
`gh` by its full path. This prints the template's GitHub block with the comment
mark `#` taken off each line and that path filled in:

```sh
GH=$(command -v gh)
meru config template \
  | sed -n '/^# GitHub with the gh CLI/,/^# Other agents/p' \
  | sed -n '/^# \[\[commands\]\]/,/^$/p' \
  | sed -e 's/^# \{0,1\}//' -e "s|\[\"gh\", |[\"$GH\", |"
```

Show the output. It holds six `[[commands]]` entries that only read:
`gh-prs`, `gh-pr`, `gh-issues`, `gh-issue`, `gh-runs` and `gh-repos`. First
check that `~/.meru/config.toml` has no uncommented `name = "gh-prs"` already.
With a yes, back up the config and append the block:

```sh
cp ~/.meru/config.toml ~/.meru/config.toml.bak
{ echo; meru config template \
  | sed -n '/^# GitHub with the gh CLI/,/^# Other agents/p' \
  | sed -n '/^# \[\[commands\]\]/,/^$/p' \
  | sed -e 's/^# \{0,1\}//' -e "s|\[\"gh\", |[\"$GH\", |"; } >> ~/.meru/config.toml
launchctl kickstart -k gui/$(id -u)/com.meru.merud   # Linux: systemctl --user restart merud
meru tools | grep cmd.gh
```

`merud` reads `[[commands]]` only when it starts, so the restart matters. If
`merud` refuses the config, its log names the line; put the backup back and
look again.

## Uninstall

On a Mac:

```sh
# The launchd jobs; skip the ones that were never installed. The Google job
# comes only from an install before connectors.
launchctl bootout gui/$(id -u)/com.meru.merud
rm ~/Library/LaunchAgents/com.meru.merud.plist
launchctl unload ~/Library/LaunchAgents/com.meru.workspace-mcp.plist
rm ~/Library/LaunchAgents/com.meru.workspace-mcp.plist
# An older install may have added this login job for Ollama's context.
launchctl bootout gui/$(id -u)/com.meru.ollama-context
rm ~/Library/LaunchAgents/com.meru.ollama-context.plist

# The programs and the app.
rm ~/.local/bin/meru ~/.local/bin/merud
rm -rf /Applications/Meru.app
```

On Linux:

```sh
systemctl --user disable --now merud
rm ~/.config/systemd/user/merud.service
systemctl --user daemon-reload

rm ~/.local/bin/meru ~/.local/bin/merud ~/.local/bin/meru-desktop
rm ~/.local/share/applications/meru.desktop
rm ~/.local/share/icons/hicolor/512x512/apps/meru.png
```

The models, on either system, one at a time; `ollama list` names them:

```sh
ollama rm hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M
ollama rm embeddinggemma-2:440m
ollama rm gemma4:26b-a4b-it-qat     # if they pulled it
ollama rm qwen3.6:35b               # if they pulled it
ollama rm qwen3.6:35b-a3b-mxfp8     # if they pulled it
```

Only after an explicit yes to a question that names what goes:

```sh
docker rm -f meru-searxng                          # Meru's web search container, if it runs
rm -rf ~/.meru                                     # every chat, memory, setting, the index and the connectors' installs
rm -rf ~/.config/workspace-mcp ~/.google_workspace_mcp   # the Google client secret and sign-in
```

`~/meru-output/` holds files Meru wrote for the person, such as saved answers
and mail attachments. Ask before removing it too.
