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

## Google start script

This is google-setup.md step B. Ask for the client ID and the Google address
first. Write the file with the secret left as a placeholder:

```sh
mkdir -p ~/.config/workspace-mcp
cat > ~/.config/workspace-mcp/start.sh <<'EOF'
#!/bin/sh
# Starts the Google server Meru connects to. Keep this file private.
export GOOGLE_OAUTH_CLIENT_ID="<your client ID>"
export GOOGLE_OAUTH_CLIENT_SECRET="<your client secret>"
export USER_GOOGLE_EMAIL="<your Google address>"
export WORKSPACE_ATTACHMENT_DIR="$HOME/meru-output/attachments"
exec uvx workspace-mcp==1.30.0 --transport streamable-http \
  --tool-tier extended --tools gmail calendar drive docs
EOF
chmod 700 ~/.config/workspace-mcp/start.sh
```

Then put in the client ID and the address with your file edit tool, or with
`sed -i ''`. Neither is secret.

**The secret, by the Terminal route.** Ask the person to run this in their own
Terminal. `read -rs` reads the secret without showing it, `sed` writes it into
the file, and `unset` forgets it; the secret never reaches this chat:

```sh
read -rs "s?Client secret: " && sed -i '' "s|<your client secret>|$s|" ~/.config/workspace-mcp/start.sh && unset s && echo saved
```

(`"s?Client secret: "` is zsh's way to give `read` a prompt. In bash, use
`read -rsp "Client secret: " s` instead.)

Check without showing the secret:

```sh
grep -c '<your client secret>' ~/.config/workspace-mcp/start.sh   # expect 0
grep -c 'GOCSPX-' ~/.config/workspace-mcp/start.sh                # expect 1
ls -l ~/.config/workspace-mcp/start.sh                            # expect -rwx------
```

## Start the Google server at login

google-setup.md step F, with the home folder filled in by the shell. The
`PATH` line lets launchd find `uvx` from Homebrew:

```sh
cat > ~/Library/LaunchAgents/com.meru.workspace-mcp.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.meru.workspace-mcp</string>
  <key>ProgramArguments</key>
  <array><string>$HOME/.config/workspace-mcp/start.sh</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:/usr/bin:/bin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/.config/workspace-mcp/server.log</string>
  <key>StandardErrorPath</key><string>$HOME/.config/workspace-mcp/server.log</string>
</dict>
</plist>
EOF
launchctl load ~/Library/LaunchAgents/com.meru.workspace-mcp.plist
```

The first start downloads the server, which takes a minute. Then check:

```sh
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8000/mcp   # any number but 000
tail -20 ~/.config/workspace-mcp/server.log
```

google-setup.md, "When something goes wrong", covers the usual errors, such as
`access_denied` (not a test user) and `redirect_uri_mismatch` (not a Desktop
app client).

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
# The launchd jobs; skip the ones that were never installed.
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
rm -rf ~/.meru                                     # every chat, memory, setting and the index
rm -rf ~/.config/workspace-mcp ~/.google_workspace_mcp   # the Google client secret and sign-in
```

`~/meru-output/` holds files Meru wrote for the person, such as saved answers
and mail attachments. Ask before removing it too.
