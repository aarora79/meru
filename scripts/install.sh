#!/usr/bin/env bash
# install.sh installs Meru on a Mac from a GitHub release. Run it with:
#
#   curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
#
# It downloads the release, checks every file against SHA256SUMS, puts meru
# and merud in ~/.local/bin and Meru.app in /Applications, and then offers
# the next steps: Ollama, the models, `meru setup`, and starting merud at
# login. It first lists what it will install and asks once: everything, each
# step in turn, or quit. MERU_YES=1 answers "everything" without asking.
# MERU_VERSION=v0.4.1 installs a given release instead of the latest.
#
# Meru never runs this script itself and never checks for updates; you run
# it again to update. MERU_APP_DIR puts Meru.app somewhere other than
# /Applications, such as a test folder.

# set -e stops at the first failed command, -u at an unset variable, and
# pipefail makes a pipeline fail when any part of it fails.
set -euo pipefail

repo="aarora79/meru"
bin_dir="$HOME/.local/bin"
# all is 1 when the user answers "install everything" at the start, or sets
# MERU_YES=1 to skip the question.
all="${MERU_YES:-0}"

say() { printf '\n==> %s\n' "$*"; }
fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# ask prints a yes/no question and returns 0 for yes. The script's own input
# is the curl pipe, so answers come from the terminal, /dev/tty. With no
# terminal, such as in CI, every answer is no.
ask() {
  local reply
  # With "install everything" chosen at the start, every step says yes.
  if [ "$all" = 1 ]; then printf '%s yes\n' "$1"; return 0; fi
  # Opening /dev/tty fails when there is no terminal, even if the file
  # exists, so try it quietly first.
  if ! { : </dev/tty; } 2>/dev/null; then return 1; fi
  printf '%s [y/N] ' "$1" >/dev/tty
  read -r reply </dev/tty || return 1
  case "$reply" in [yY]|[yY][eE][sS]) return 0 ;; *) return 1 ;; esac
}

[ "$(uname -s)" = "Darwin" ] || fail "this installer is for macOS; see docs/running.md for Linux and Windows"
case "$(uname -m)" in
  arm64) arch="arm64" ;;
  x86_64) arch="amd64" ;;
  *) fail "unsupported processor $(uname -m)" ;;
esac

# The latest release's tag: github.com/<repo>/releases/latest redirects to
# .../releases/tag/<tag>, and curl's url_effective is where it landed.
version="${MERU_VERSION:-}"
if [ -z "$version" ]; then
  url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")"
  version="${url##*/}"
fi
case "$version" in v[0-9]*.[0-9]*.[0-9]*) ;; *) fail "couldn't find the release to install (got '$version')" ;; esac
# What this Mac will get, so the banner can list it before anything starts.
mem_gb=$(( $(sysctl -n hw.memsize) / 1073741824 ))
has_ollama=0; command -v ollama >/dev/null 2>&1 && has_ollama=1
has_brew=0; command -v brew >/dev/null 2>&1 && has_brew=1

echo
echo "  Meru $version for macOS on $arch"
echo "  --------------------------------"
echo "  This takes several minutes, most of it downloading models."
echo "  Here is what it installs:"
echo
echo "    - meru and merud, about 40 MB, in $bin_dir"
[ "$arch" = "arm64" ] && echo "    - Meru.app, about 12 MB, in ${MERU_APP_DIR:-/Applications}"
if [ "$has_ollama" = 1 ]; then
  echo "    - Ollama: already installed"
elif [ "$has_brew" = 1 ]; then
  echo "    - Ollama, with Homebrew"
else
  echo "    - Ollama: install it yourself from https://ollama.com/download"
fi
echo "    - the lite models, about 2 GB: MiniCPM5-2B and nomic-embed-text"
[ "$mem_gb" -ge 48 ] && echo "    - qwen3.6:35b, 23 GB, a stronger answer model (this Mac has $mem_gb GB)"
echo "    - meru setup, which asks its own questions: your folders and about you"
echo "    - merud, started now and at every login"
echo
echo "  Nothing goes anywhere but the downloads from GitHub, Homebrew and Ollama."
echo

if [ "$all" != 1 ] && { : </dev/tty; } 2>/dev/null; then
  printf 'Install everything [a], choose each step [s], or quit [q]? ' >/dev/tty
  read -r choice </dev/tty || choice=q
  case "$choice" in
    [aA]*) all=1 ;;
    [sS]*) all=0 ;;
    *) echo "Nothing installed."; exit 0 ;;
  esac
fi

say "Installing Meru $version for macOS on $arch"

tmp="$(mktemp -d)"
# trap removes the download folder when the script ends, however it ends.
trap 'rm -rf "$tmp"' EXIT
base="https://github.com/$repo/releases/download/$version"
cli="meru-$version-darwin-$arch.tar.gz"
app="Meru-$version-macos-arm64.zip"

files=("$cli" SHA256SUMS)
[ "$arch" = "arm64" ] && files+=("$app")
for f in "${files[@]}"; do
  curl -fsSL -o "$tmp/$f" "$base/$f" || fail "couldn't download $f from $base"
done

say "Checking the downloads against SHA256SUMS"
(cd "$tmp" && shasum -a 256 -c SHA256SUMS --ignore-missing) || fail "a download doesn't match SHA256SUMS; nothing was installed"

say "Installing meru and merud in $bin_dir"
mkdir -p "$bin_dir"
tar -xzf "$tmp/$cli" -C "$tmp"
install -m 0755 "$tmp/meru-$version-darwin-$arch/meru" "$tmp/meru-$version-darwin-$arch/merud" "$bin_dir/"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *)
    echo "$bin_dir isn't on your PATH."
    if ask "Add it to ~/.zshrc?"; then
      echo 'export PATH="$HOME/.local/bin:$PATH"' >>"$HOME/.zshrc"
      echo "Added. Open a new Terminal window for it to take effect."
    fi
    export PATH="$bin_dir:$PATH"
    ;;
esac

if [ "$arch" = "arm64" ]; then
  apps="${MERU_APP_DIR:-/Applications}"
  [ -w "$apps" ] || apps="$HOME/Applications"
  say "Installing Meru.app in $apps"
  mkdir -p "$apps"
  rm -rf "$apps/Meru.app"
  ditto -x -k "$tmp/$app" "$apps"
  echo "Meru.app isn't signed by Apple, so macOS blocks it the first time."
  if ask "Clear the quarantine flag so it opens normally?"; then
    xattr -dr com.apple.quarantine "$apps/Meru.app"
  else
    echo "To open it later: right-click Meru.app, choose Open, then Open again."
  fi
else
  echo "Meru.app is built for Apple silicon only; on this Mac use meru and meru chat."
fi

say "Ollama runs the models"
if command -v ollama >/dev/null 2>&1; then
  echo "Ollama is installed: $(ollama --version 2>/dev/null || echo unknown version)."
elif command -v brew >/dev/null 2>&1 && ask "Install Ollama with Homebrew?"; then
  brew install ollama
  brew services start ollama
else
  echo "Install Ollama from https://ollama.com/download, open it, then run this script again or carry on below."
fi

if command -v ollama >/dev/null 2>&1; then
  echo "This Mac has $mem_gb GB of memory. Meru's lite models need about 2 GB."
  if ask "Download the lite models (MiniCPM5-2B and nomic-embed-text)?"; then
    ollama pull hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M
    ollama pull nomic-embed-text
  fi
  if [ "$mem_gb" -ge 48 ] && ask "Also download qwen3.6:35b (23 GB), a stronger answer model?"; then
    ollama pull qwen3.6:35b
    echo "Pick it later in Meru.app under Settings, Models, Use for answers."
  fi
fi

say "Setting up Meru"
# meru setup asks its own questions, so it needs a terminal even when every
# step says yes.
if { : </dev/tty; } 2>/dev/null && ask "Run meru setup now (config, folders, and questions about you)?"; then
  "$bin_dir/meru" setup </dev/tty
else
  echo "Run 'meru setup' when you're ready."
fi

plist="$HOME/Library/LaunchAgents/com.meru.merud.plist"
if ask "Start merud now and at every login?"; then
  mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.meru"
  curl -fsSL "https://raw.githubusercontent.com/$repo/$version/deploy/launchd/com.meru.merud.plist" |
    sed -e "s|__HOME__/go/bin/merud|$bin_dir/merud|" -e "s|__HOME__|$HOME|g" >"$plist"
  launchctl unload "$plist" 2>/dev/null || true
  launchctl load "$plist"
  sleep 2
  "$bin_dir/meru" ping || echo "merud didn't answer yet; check ~/.meru/merud.err.log."
else
  echo "Start it yourself with: merud &"
fi

say "Done"
echo "Ask a question:   meru \"what can you do?\""
echo "Chat:             meru chat"
[ "$arch" = "arm64" ] && echo "Desktop app:      open Meru.app"
echo "Gmail, Calendar and Drive: https://github.com/$repo/blob/main/docs/google-setup.md"
