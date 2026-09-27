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

## Before you start

- Merge everything the release needs into `main`, and wait for CI to pass.
- Run `gh auth login` once, so `gh` can push the tag and create the release.
- Pick the version. The first release is `v0.4.1`: `v0.4.0` is a tag with no
  release, and `v0.5.0` belongs to the scheduler milestone in
  [ROADMAP.md](../ROADMAP.md).
- To open the notes with a few lines of your own, write them in
  `.scratchpad/release/v0.4.1.md`. Git ignores `.scratchpad/`, so the file
  doesn't dirty the tree. GitHub adds the list of merged pull requests below
  your lines. Without the file, the release gets GitHub's list alone.

## What `make release` does

`make release` runs `scripts/release.sh`, which stops at the first step that
fails:

1. **Checks.** It refuses to go on unless `VERSION` looks like `vX.Y.Z`, the
   Mac has Apple silicon, you are on `main` with no changes, `main` matches
   `origin/main`, and no tag of that name exists here or on GitHub.
2. **`make check`**, every check CI runs.
3. **Builds.** `make build` writes `meru` and `merud` for macOS (Apple silicon
   and Intel), Linux (x86-64 and ARM) and Windows (x86-64) to `bin/`, and
   `make desktop-app` writes `bin/Meru.app`. The script sets the app's
   `Info.plist` version to `0.4.1`, so Finder's Get Info shows it.
4. **Packing.** Into `dist/`, which git ignores:

   | File | What it holds |
   | --- | --- |
   | `meru-v0.4.1-darwin-arm64.tar.gz` | `meru`, `merud`, `LICENSE` and `README.md` for a Mac with Apple silicon |
   | `meru-v0.4.1-darwin-amd64.tar.gz` | the same for an Intel Mac |
   | `meru-v0.4.1-linux-amd64.tar.gz`, `meru-v0.4.1-linux-arm64.tar.gz` | the same for Linux |
   | `meru-v0.4.1-windows-amd64.zip` | `meru.exe`, `merud.exe`, `LICENSE` and `README.md` |
   | `Meru-v0.4.1-macos-arm64.zip` | `Meru.app`, for Apple silicon only |
   | `SHA256SUMS` | the SHA-256 of each file above |

   Each archive opens to one folder named after the archive.
5. **Publishing.** It makes an annotated tag, pushes it, and runs
   `gh release create` with the files, your notes and GitHub's generated
   notes.

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
    --verify-tag --generate-notes dist/*.tar.gz dist/*.zip dist/SHA256SUMS
  ```

  To start over instead, delete the tag in both places with
  `git push origin :refs/tags/v0.4.1` and `git tag -d v0.4.1`.

## What users need to know

**Anyone can download a release.** The repository is public, so a Mac installs
the latest release with one line:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
```

`make release` attaches `scripts/install.sh` to every release, so that URL always
serves the latest copy. A plain `curl -LO` of any file's URL works too.

**Meru.app isn't signed or notarized**, because the project has no Apple
Developer account. A browser marks a downloaded file with the
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
GitHub on its own (AGENTS.md, non-negotiable 2). A user updates by
downloading a newer release; [running.md](running.md#install-from-a-release)
shows how.
