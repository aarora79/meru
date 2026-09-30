# meru-install: reference

The files and longer commands SKILL.md points to. Show each command to the
person and get a yes before you run anything that writes or deletes.

## Start merud at login

The release holds only the programs, so fetch the launchd file from the
repository at the same tag. It names `__HOME__/go/bin/merud`; the first `sed`
expression points it at `~/.local/bin`, and the second fills in the home
folder, since launchd doesn't expand `~` or `$HOME`:

```sh
mkdir -p ~/.meru ~/Library/LaunchAgents
gh api -H "Accept: application/vnd.github.raw" \
  "repos/aarora79/meru/contents/deploy/launchd/com.meru.merud.plist?ref=$TAG" \
  | sed -e "s|__HOME__/go/bin/merud|$HOME/.local/bin/merud|" -e "s|__HOME__|$HOME|g" \
  > ~/Library/LaunchAgents/com.meru.merud.plist
grep -c "$HOME/.local/bin/merud" ~/Library/LaunchAgents/com.meru.merud.plist   # expect 1
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.meru.merud.plist
```

If `bootstrap` says the service is already loaded, an older job exists. Remove
it with `launchctl bootout gui/$(id -u)/com.meru.merud`, then bootstrap again.

Useful afterwards:

```sh
launchctl print gui/$(id -u)/com.meru.merud        # state and last exit code
launchctl kickstart -k gui/$(id -u)/com.meru.merud # restart
tail -20 ~/.meru/merud.err.log                     # what merud printed before its log opened
```

## Google by hand

The Google connector needs no file of its own: `merud` installs and runs the
server (SKILL.md, step 6). Only an install from before connectors runs the
server from `~/.config/workspace-mcp/start.sh` and the launchd job
`com.meru.workspace-mcp`; `meru mcp adopt google` moves such a setup over, and
[docs/google-setup.md](../../../docs/google-setup.md), "Run the server
yourself", keeps the steps for anyone who wants to run it by hand.

## GitHub commands

`merud` started by launchd has only `/usr/bin:/bin:/usr/sbin:/sbin` on its
`PATH`, so each command names `gh` by its full path. This prints the template's
GitHub block with the comment mark `#` taken off each line and that path filled in:

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
launchctl kickstart -k gui/$(id -u)/com.meru.merud
meru tools | grep cmd.gh
```

`merud` reads `[[commands]]` only when it starts, so the restart matters. If
`merud` refuses the config, `~/.meru/merud.err.log` names the line; put the
backup back and look again.

## Uninstall

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

# The models, one at a time; `ollama list` names them.
ollama rm hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M
ollama rm nomic-embed-text
ollama rm gemma4:26b-a4b-it-qat     # if they pulled it
ollama rm qwen3.6:35b               # if they pulled it
ollama rm qwen3.6:35b-a3b-mxfp8     # if they pulled it
ollama rm qwen3-embedding:0.6b      # if they used the full profile
```

Only after an explicit yes to a question that names what goes:

```sh
docker rm -f meru-searxng                          # Meru's web search container, if it runs
rm -rf ~/.meru                                     # every chat, memory, setting, the index and the connectors' installs
rm -rf ~/.config/workspace-mcp ~/.google_workspace_mcp   # the Google client secret and sign-in
```

`~/meru-output/` holds files Meru wrote for the person, such as saved answers
and mail attachments. Ask before removing it too.
