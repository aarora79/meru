#!/usr/bin/env bash
# release.sh builds a Meru release on the owner's Mac and publishes it on
# GitHub. `make release VERSION=v0.4.1` runs it; docs/releasing.md explains
# each step and why the build runs here and not in CI.
#
# What it does, in order:
#   1. refuses to start unless the repo and the version are ready;
#   2. runs `make check`;
#   3. builds meru and merud for five platforms, and Meru.app for this Mac,
#      with the version stamped in;
#   4. packs them into dist/ with a SHA256SUMS file;
#   5. tags the commit, pushes the tag and creates the GitHub release.
#
# DRY_RUN=1 does steps 1 to 4 and stops before it tags anything. In a dry run
# the branch, clean-tree and up-to-date checks only warn, so you can try the
# script on a branch.
#
# Meru never runs this script or checks for releases by itself; a person
# runs it, and a person installs what it publishes.

# set -e stops at the first command that fails, -u treats an unset variable
# as an error, and -o pipefail makes a pipe fail when any command in it does.
set -euo pipefail

# The GitHub repository the release goes to.
repo="aarora79/meru"

# The five platforms `make build` writes to bin/<os>-<arch>/.
platforms="darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64"

# VERSION comes from `make release VERSION=...`. ${VERSION:-} reads it as
# empty when it isn't set, so set -u doesn't stop the script before the
# check below can print a helpful message.
version="${VERSION:-}"
dry_run="${DRY_RUN:-}"

# fail prints a message to stderr and stops the script.
fail() {
  echo "release: $*" >&2
  exit 1
}

# warn prints a message to stderr and carries on.
warn() {
  echo "release: warning: $*" >&2
}

# must fails in a real release and warns in a dry run. It covers the checks
# on the git branch and tree, which a dry run on a branch can't pass.
must() {
  if [ -n "$dry_run" ]; then
    warn "$* (a real release stops here)"
  else
    fail "$*"
  fi
}

# Run from the repository's top folder, whatever folder make started in.
cd "$(git rev-parse --show-toplevel)"

# ---- 1. Is everything ready? ----

# [[ ... =~ ... ]] matches a regular expression: v, then three numbers.
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  fail "VERSION must look like v0.4.1; run: make release VERSION=v0.4.1"
fi

# The app in the zip is built for this machine, and its name says arm64.
if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  fail "run releases on a Mac with Apple silicon; Meru.app builds only for the machine that builds it"
fi

for tool in git go gh ditto shasum zip plutil; do
  command -v "$tool" >/dev/null || fail "$tool isn't installed"
done

branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$branch" != "main" ]; then
  must "you are on $branch; releases come from main"
fi

# git status --porcelain prints one line per changed or untracked file.
if [ -n "$(git status --porcelain)" ]; then
  must "the working tree has changes; commit or remove them first"
fi

git fetch --quiet origin main
if [ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]; then
  must "this commit isn't origin/main; pull or push first"
fi

# A tag that exists here or on GitHub means this version already shipped,
# or a release stopped halfway; docs/releasing.md says how to clean up.
if git rev-parse --quiet --verify "refs/tags/$version" >/dev/null; then
  fail "tag $version already exists here"
fi
if [ -n "$(git ls-remote --tags origin "refs/tags/$version")" ]; then
  fail "tag $version already exists on GitHub"
fi

if [ -z "$dry_run" ]; then
  gh auth status >/dev/null 2>&1 || fail "gh isn't signed in; run gh auth login"
fi

# The hand-written summary that opens the release notes. .scratchpad/ is
# ignored by git, so writing it doesn't dirty the tree.
notes=".scratchpad/release/$version.md"
if [ -f "$notes" ]; then
  echo "release: notes: $notes, then the notes GitHub generates"
else
  echo "release: no summary at $notes; the release gets GitHub's generated notes only"
  echo "release: to add one, write a few lines there and run this again"
fi

# ---- 2. Run every check CI runs ----

echo "release: make check"
make check

# ---- 3. Build ----

# -X tells the Go linker to set a string variable in a package. Both
# variables stay empty in every other build; internal/obs reports the
# version for merud and about_meru, internal/desktop for the app's About.
module="$(go list -m)"
ldflags="-X $module/internal/obs.releaseVersion=$version -X $module/internal/desktop.releaseVersion=$version"

# Start from empty folders, so nothing from an earlier build slips in.
rm -rf bin dist
mkdir -p dist/stage

echo "release: build meru and merud for $platforms"
make build LDFLAGS="$ldflags"

echo "release: build Meru.app"
make desktop-app LDFLAGS="$ldflags"

# Finder's Get Info reads the version from Info.plist, which says 0.4 in
# the repo. ${version#v} drops the leading v: v0.4.1 becomes 0.4.1.
plutil -replace CFBundleShortVersionString -string "${version#v}" bin/Meru.app/Contents/Info.plist
plutil -replace CFBundleVersion -string "${version#v}" bin/Meru.app/Contents/Info.plist

# ---- 4. Pack ----

# COPYFILE_DISABLE stops macOS tar from adding ._ files that hold Finder
# metadata. The uid and gid options keep this Mac's user name out of the
# archive.
export COPYFILE_DISABLE=1

for platform in $platforms; do
  name="meru-$version-$platform"
  dir="dist/stage/$name"
  mkdir -p "$dir"

  # Windows programs end in .exe.
  ext=""
  if [ "${platform%%-*}" = "windows" ]; then
    ext=".exe"
  fi
  cp "bin/$platform/meru$ext" "bin/$platform/merud$ext" LICENSE README.md "$dir/"

  # tar -C and zip inside ( cd ... ) keep the stage path out of the
  # archive, so each one opens to a single folder named $name.
  if [ -n "$ext" ]; then
    (cd dist/stage && zip -q -r -X "../$name.zip" "$name")
  else
    tar -C dist/stage --uid 0 --gid 0 --uname root --gname root -czf "dist/$name.tar.gz" "$name"
  fi
done

# ditto is the Mac's own copier. -c -k writes a zip, and --keepParent puts
# Meru.app itself in the zip, not only what it holds. The --no options
# leave out this Mac's extended attributes and resource data, which would
# otherwise add a ._ file beside each file in the zip.
ditto -c -k --keepParent --norsrc --noextattr --noqtn --noacl \
  bin/Meru.app "dist/Meru-$version-macos-arm64.zip"

rm -rf dist/stage

# One line per file: its SHA-256 and its name. Installers check the files
# they download with: shasum -a 256 -c SHA256SUMS --ignore-missing
(cd dist && shasum -a 256 *.tar.gz *.zip > SHA256SUMS)

echo "release: built in dist/:"
ls -l dist

# ---- 5. Tag and publish ----

if [ -n "$dry_run" ]; then
  echo "release: dry run; no tag, no push, no release"
  exit 0
fi

git tag -a "$version" -m "Meru $version"
git push origin "$version"
echo "release: pushed tag $version; if the next step fails, docs/releasing.md says how to finish"

# --verify-tag stops gh if the tag didn't reach GitHub. Notes from the file
# come first; --generate-notes adds the list of merged pull requests.
notes_args=(--generate-notes)
if [ -f "$notes" ]; then
  notes_args+=(--notes-file "$notes")
fi
gh release create "$version" \
  --repo "$repo" \
  --title "Meru $version" \
  --verify-tag \
  "${notes_args[@]}" \
  dist/*.tar.gz dist/*.zip dist/SHA256SUMS scripts/install.sh

echo "release: published https://github.com/$repo/releases/tag/$version"
