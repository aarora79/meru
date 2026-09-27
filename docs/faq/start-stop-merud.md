# How do I start, stop or restart merud?

`merud` is the program that does the work. `meru`, `meru chat` and the desktop
app only talk to it, so a change to `config.toml` needs a restart of `merud`,
not of the chat or the app.

**Run it by hand:**

```sh
merud              # in its own terminal; Ctrl-C stops it
merud &            # in the background
merud -v &         # with the debug log
pkill merud        # stop it
pkill merud; merud &   # restart it
meru ping          # prints "merud is up"
```

**Start it at login** on macOS with the `launchd` agent in the repo. From your
clone:

```sh
sed "s|__HOME__|$HOME|g" deploy/launchd/com.meru.merud.plist \
  > ~/Library/LaunchAgents/com.meru.merud.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.meru.merud.plist
```

That plist runs `~/go/bin/merud`. For a release install in `~/.local/bin`, use
the `sed` line in [deploy/README.md](../../deploy/README.md) instead. `launchd`
starts `merud` again if it exits. To manage it:

```sh
launchctl print gui/$(id -u)/com.meru.merud        # state and last exit code
launchctl kickstart -k gui/$(id -u)/com.meru.merud # restart
launchctl bootout gui/$(id -u)/com.meru.merud      # stop and remove
```

Under `launchd`, `pkill merud` only makes it start again, so restart with
`kickstart -k`.

**On Linux**, install the `systemd` user unit and use
`systemctl --user restart merud`. [deploy/README.md](../../deploy/README.md)
has the steps.

**Which ones need a restart?** Anything you edit by hand in `config.toml`:
`[[commands]]`, `[index] folders`, models other than the answer model, and
`[log]`. Changes made through the desktop app's Settings, `/mcp`, `/folders` and
`meru mcp add` apply at once.

**If it won't start**, run `merud` in a terminal and read what it prints. It
names the key when a config value is wrong, and says so when another `merud`
already runs.
