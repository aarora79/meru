# How do I update or uninstall Meru?

Meru never checks for updates on its own.

## Update

**From a release**, quit `Meru.app`, then run the install script again. It
fetches the latest release, lists what it will install and asks before it
starts.

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
```

On Linux, run the Linux script again instead:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install-linux.sh | bash
```

**From source:**

```sh
cd ~/repos/meru
git pull
go install ./cmd/merud ./cmd/meru
```

**Then restart `merud`.** The old program keeps running until you do:

```sh
pkill merud; merud &                                 # started by hand
launchctl kickstart -k gui/$(id -u)/com.meru.merud   # started by launchd
systemctl --user restart merud                       # started by systemd (Linux)
```

**After an update**, take the newer built-in skills, since `merud` never
overwrites your copies:

```sh
meru skills reset --yes web-research
meru skills reset --yes file-research
```

Then run `meru check` to confirm your questions still pass (see
[check-answers.md](check-answers.md)).

## Uninstall

1. If `merud` starts at login, remove the service first:

   ```sh
   launchctl bootout gui/$(id -u)/com.meru.merud
   rm ~/Library/LaunchAgents/com.meru.merud.plist
   ```

   On Linux:

   ```sh
   systemctl --user disable --now merud
   rm ~/.config/systemd/user/merud.service
   systemctl --user daemon-reload
   ```

2. Remove the programs. For a release install:

   ```sh
   rm ~/.local/bin/meru ~/.local/bin/merud
   rm -rf /Applications/Meru.app
   ```

   On Linux, remove the desktop app and its menu entry too:

   ```sh
   rm -f ~/.local/bin/meru-desktop ~/.local/share/applications/meru.desktop
   rm -f ~/.local/share/icons/hicolor/512x512/apps/meru.png
   ```

   For a source install: `rm ~/go/bin/merud ~/go/bin/meru`.

3. Remove your data, if you want it gone. This deletes your settings, keys,
   memories, every transcript and the index:

   ```sh
   rm -rf ~/.meru
   ```

   Files Meru wrote for you stay in `~/meru-output` until you delete them.

4. Free the space the models use with `ollama rm <model>` for each one.
