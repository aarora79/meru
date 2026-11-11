# Releasing Meru

A release is a git tag such as `v0.4.1` and a GitHub release that holds the
programs for that tag. The owner builds it on a Mac with Apple silicon with one
command:

```sh
make release VERSION=v0.4.1
```

This page covers what the command does, why it runs on a Mac and not in CI, and
what people who download a release need to know.
[running.md](running.md#install-from-a-release) shows how to install one, and
the `meru-install` skill in `.claude/skills/` walks a user through it.

## Why it builds on a Mac

`Meru.app` uses Wails, which needs cgo and the Mac's own WebView, so it builds
only on a Mac, for that Mac's kind of chip. The owner's Mac has Apple silicon,
so the release carries the app for Apple silicon only. `meru` and `merud` need
no cgo and cross-compile for five platforms from any machine.

CI could build the command-line programs, but a second path for the app would
mean two places that pack a release. One script on one machine keeps one path,
and the owner sees each file before it goes out. CI still runs every check on
the commit first, and the script runs `make check` again before it builds.

## The release in two steps

1. **On the Mac:** `make release VERSION=v0.4.1`. It publishes Meru.app, the
   disk image, `meru` and `merud` for every platform, and both install
   scripts.
2. **On GitHub:** run the `desktop-linux` workflow for the same tag. It builds
   the Linux desktop app on Ubuntu 24.04 and adds its archive to the release:

   ```sh
   gh workflow run desktop-linux.yml --repo aarora79/meru -f version=v0.4.1
   gh run watch --repo aarora79/meru   # pick the run to follow it
   ```

   Until it runs, `install-linux.sh` installs `meru` and `merud` and says the
   release has no desktop app.

## What you need

**The Mac for `make release`.** Any Mac with Apple silicon (M1 or later);
the script refuses an Intel Mac, because Meru.app builds only for the chip it
builds on. On a fresh Mac:

```sh
xcode-select --install          # git, and the C compiler cgo needs for Meru.app
brew install go gh              # any recent Go; go.mod pins the toolchain, which Go downloads
gh auth login                   # so gh can push the tag and create the release
git clone https://github.com/aarora79/meru && cd meru
make release VERSION=v0.4.1 DRY_RUN=1
```

`ditto`, `shasum`, `zip`, `plutil` and `hdiutil` come with macOS. The release
needs no Apple Developer account, no Docker, no Ollama and no Node: the app is
signed ad hoc, the tests use the fake Ollama, and the check tools run through
`go run` at pinned versions. The first run takes longer, while Go downloads
the toolchain, the modules and those tools.

**The Linux desktop app.** The `desktop-linux` workflow needs nothing from
you but the tag. To build the same archive on your own machine, use Ubuntu
24.04, which has the GTK 4 and WebKitGTK 6.0 the pinned Wails needs:

```sh
sudo apt install gcc pkg-config libgtk-4-dev libwebkitgtk-6.0-dev
# install Go as above, then from the repository:
make desktop-linux VERSION=v0.4.1   # writes dist/meru-desktop-v0.4.1-linux-amd64.tar.gz
```

A hand-built archive is for trying out. The release gets the workflow's
build, which also signs a build attestation.

## Before you start

- Merge everything the release needs into `main`, and wait for CI to pass.
- Pick the version. The first release is `v0.4.1`: `v0.4.0` is a tag with no
  release, and `v0.5.0` belongs to the scheduler milestone in
  [ROADMAP.md](../ROADMAP.md).
- Write the release notes with the `release-notes` skill in `.claude/skills/`.
  It writes `docs/release-notes/v0.4.1.md`: what changed, what an upgrade
  needs, the pull requests and the closed issues. Merge its pull request
  before you run `make release`, which uses that file as the GitHub release's
  text. Without the file, the release gets GitHub's list of merged pull
  requests alone. The same pull request moves the version lines: the README's
  status, AGENTS.md's Status, and the landing page's release pill, whose
  version, link and time `docs/index.html` holds.
- Run `collector/check.sh` and confirm the collector answers 204 to the good
  report and every line says `ok`. Each release sends the daily usage report
  there, so a broken collector loses that release's counts. The good reports
  write rows with Meru version `0.0.0`;
  [collector/README.md](../collector/README.md) shows how to delete them. A
  release that raises the report's version needs the collector to accept it
  first: run the migration the collector's README names, and deploy, before
  `make release`.

## What `make release` does

`make release` runs `scripts/release.sh`, which stops at the first step that
fails:

1. **Checks.** It refuses to go on unless `VERSION` looks like `vX.Y.Z`, the
   Mac has Apple silicon, you are on `main` with no changes, `main` matches
   `origin/main`, and no tag of that name exists here or on GitHub.
2. **`make check`**, every check CI runs.
3. **Builds.** `make build` writes `meru` and `merud` for macOS (Apple silicon
   and Intel), Linux (x86-64 and ARM) and Windows (x86-64) to `bin/`, and
   `make dmg` writes `bin/Meru.app`, then `bin/Install Meru.app`, the Mac
   installer, with `meru`, `merud` and `Meru.app` inside it, and packs the
   installer in a disk image with `hdiutil`. The Makefile sets both apps'
   `Info.plist` version to `0.4.1`, so Finder's Get Info shows it.
4. **Packing.** Into `dist/`, which git ignores:

   | File | What it holds |
   | --- | --- |
   | `meru-v0.4.1-darwin-arm64.tar.gz` | `meru`, `merud`, `LICENSE` and `README.md` for a Mac with Apple silicon |
   | `meru-v0.4.1-darwin-amd64.tar.gz` | the same for an Intel Mac |
   | `meru-v0.4.1-linux-amd64.tar.gz`, `meru-v0.4.1-linux-arm64.tar.gz` | the same for Linux |
   | `meru-v0.4.1-windows-amd64.zip` | `meru.exe`, `merud.exe`, `LICENSE` and `README.md` |
   | `Meru-v0.4.1-macos-arm64.zip` | `Meru.app`, for Apple silicon only |
   | `Meru-v0.4.1-macos-arm64.dmg` | the Mac installer, "Install Meru.app", with `meru`, `merud` and `Meru.app` inside, and "Read me first.txt"; about 60 MB |
   | `Meru-macos-arm64.dmg` | the same disk image under a name with no version, so the README and the landing page can link to the newest one |
   | `SHA256SUMS` | the SHA-256 of each file above |

   Each archive opens to one folder named after the archive.
5. **Publishing.** It makes an annotated tag, pushes it, and runs
   `gh release create` with the files and `docs/release-notes/v0.4.1.md` as
   the release's text.

### Try it first

`DRY_RUN=1` runs steps 1 to 4 and stops before the tag:

```sh
make release VERSION=v0.4.1 DRY_RUN=1
ls -l dist
```

In a dry run the branch, clean-tree and up-to-date checks warn and carry on,
so you can try the script on a branch. The version and tag checks, and
`make check`, still stop it.

## How the version gets into the programs

Go's linker sets a string variable in a package when it gets
`-X package.name=value`. The script passes:

```text
-X github.com/aarora79/meru/internal/obs.releaseVersion=v0.4.1
-X github.com/aarora79/meru/internal/about.releaseVersion=v0.4.1
```

through the `LDFLAGS` variable of `make build` and `make desktop`. `merud`
reports the first in its traces and through the `about_meru` tool. The app's
About section and the header of `meru chat` show the second. Every other build leaves both empty,
and the programs fall back to what Go records in each binary: `(devel)` and
the git commit for a local build. Nobody bumps a version number in the code.

Check a build:

```sh
strings bin/darwin-arm64/merud | grep -x v0.4.1
```

## When a release stops halfway

- **Before the tag:** fix the cause and run the command again. It starts from
  empty `bin/` and `dist/` folders.
- **After the tag, before the release:** the script names the tag it pushed.
  Rerun only the last step by hand:

  ```sh
  gh release create v0.4.1 --repo aarora79/meru --title "Meru v0.4.1" \
    --verify-tag --notes-file docs/release-notes/v0.4.1.md \
    dist/*.tar.gz dist/*.zip dist/*.dmg dist/SHA256SUMS scripts/install.sh \
    scripts/install-linux.sh
  ```

  To start over instead, delete the tag in both places with
  `git push origin :refs/tags/v0.4.1` and `git tag -d v0.4.1`.

## What users need to know

**Anyone can download a release.** The repository is public, so a Mac installs
the latest release with one line:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
```

Linux has its own line:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install-linux.sh | bash
```

`make release` attaches `scripts/install.sh` and `scripts/install-linux.sh` to
every release, so those URLs always serve the latest copy. The Linux desktop
app comes from the `desktop-linux` workflow, step 2 of
[the release in two steps](#the-release-in-two-steps) ([ci.md](ci.md#desktop-linux)). A plain `curl -LO` of any file's URL works too. People
who'd rather not use Terminal download `Meru-v0.4.1-macos-arm64.dmg`, open it and
run "Install Meru.app", which walks through eleven steps in a window
([running.md](running.md#the-mac-installer)).

To try the disk image before a release, `make dmg` builds
`dist/Meru-dev-macos-arm64.dmg` from the working tree.

**Meru.app and the installer are signed ad hoc, not by Apple, and aren't
notarized**, because the project has no Apple Developer account. `make
desktop-app` and `make installer-app` sign each finished bundle with
`codesign --sign -` and verify it: without that seal over `Info.plist` and the
resources, macOS calls a downloaded copy "damaged" and won't open it. The disk
image's "Read me first.txt" explains how to open the installer the first time, and the installer's Install Meru
step offers to clear the quarantine mark from what it installs. A browser marks a downloaded file with the
`com.apple.quarantine` attribute, and macOS then refuses to open the app,
saying it can't check it for malicious software. The user can clear the mark
once they trust the file:

```sh
xattr -dr com.apple.quarantine /Applications/Meru.app
```

or open the app, click **Done**, and choose **Open Anyway** under System
Settings, Privacy & Security. Command-line tools such as `gh` and `curl` don't
set the mark, so a zip they download may open without it; a zip saved from a
browser always carries it.

**Meru never checks for updates.** No release, and no part of Meru, reaches
GitHub on its own (AGENTS.md, non-negotiable 2). The anonymous daily usage
report names the version a user runs, but it goes to the project's collector,
and `merud` reads nothing from the answer but its status. A user updates by
downloading a newer release; [running.md](running.md#install-from-a-release)
shows how.
