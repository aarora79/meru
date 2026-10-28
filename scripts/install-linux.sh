#!/usr/bin/env bash
# install-linux.sh installs Meru on Linux from a GitHub release. Run it with:
#
#   curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install-linux.sh | bash
#
# It downloads the release, checks every file against its SHA256SUMS file,
# and puts meru and merud in ~/.local/bin. Then it offers the next steps:
# the desktop app, Ollama, `meru setup`, which suggests the answer model
# for this machine's GPU and downloads the models, and merud as a systemd
# user service that starts at boot. It first lists what it will install and
# asks once: everything, each step in turn, or quit. MERU_YES=1 answers
# "everything" without asking. MERU_VERSION=v0.5.1 installs a given release
# instead of the latest.
#
# Tested on Ubuntu 24.04. Meru never runs this script itself and never
# checks for updates; you run it again to update. scripts/install.sh is the
# Mac's.

# set -e stops at the first failed command, -u at an unset variable, and
# pipefail makes a pipeline fail when any part of it fails.
set -euo pipefail

repo="aarora79/meru"
bin_dir="$HOME/.local/bin"
# all is 1 when the user answers "install everything" at the start, or sets
# MERU_YES=1 to skip the question.
all="${MERU_YES:-0}"

say() { printf '\n==> %s\n' "$*"; }
fail() { printf 'install-linux.sh: %s\n' "$*" >&2; exit 1; }

# has_tty says whether there is a terminal to ask on. The script's own input
# is the curl pipe, so answers come from /dev/tty. Opening it fails when
# there is no terminal, even if the file exists, so try it quietly.
has_tty() { { : </dev/tty; } 2>/dev/null; }

# ask prints a yes/no question and returns 0 for yes. With no terminal,
# such as in CI, every answer is no unless MERU_YES=1.
ask() {
  local reply
  if [ "$all" = 1 ]; then printf '%s yes\n' "$1"; return 0; fi
  has_tty || return 1
  printf '%s [y/N] ' "$1" >/dev/tty
  read -r reply </dev/tty || return 1
  case "$reply" in [yY]|[yY][eE][sS]) return 0 ;; *) return 1 ;; esac
}

[ "$(uname -s)" = "Linux" ] || fail "this installer is for Linux; on a Mac use install.sh"
case "$(uname -m)" in
  x86_64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *) fail "unsupported processor $(uname -m)" ;;
esac
command -v curl >/dev/null 2>&1 || fail "curl is missing; install it with: sudo apt install curl"
# A container or WSL may ship systemctl with no systemd running, so look
# for the folder systemd makes when it boots the machine.
[ -d /run/systemd/system ] && has_systemd=1 || has_systemd=0

# The latest release's tag: github.com/<repo>/releases/latest redirects to
# .../releases/tag/<tag>, and curl's url_effective is where it landed.
version="${MERU_VERSION:-}"
if [ -z "$version" ]; then
  url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")"
  version="${url##*/}"
fi
case "$version" in v[0-9]*.[0-9]*.[0-9]*) ;; *) fail "couldn't find the release to install (got '$version')" ;; esac

base="https://github.com/$repo/releases/download/$version"
cli="meru-$version-linux-$arch.tar.gz"
desktop="meru-desktop-$version-linux-$arch.tar.gz"
# The desktop app comes from a workflow that runs after the release, so an
# older release may lack it. curl -I asks for the headers only.
has_desktop=0
curl -fsSLI -o /dev/null "$base/$desktop" 2>/dev/null && has_desktop=1
has_ollama=0; command -v ollama >/dev/null 2>&1 && has_ollama=1
# nvidia-smi answers only where the NVIDIA driver works.
gpu="$(nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>/dev/null | head -1 || true)"

echo
echo "  Meru $version for Linux on $arch"
echo "  --------------------------------"
echo "  This takes several minutes, most of it downloading models."
echo "  Here is what it installs:"
echo
echo "    - meru and merud, about 40 MB, in $bin_dir"
if [ "$has_desktop" = 1 ]; then
  echo "    - the desktop app, about 25 MB, in $bin_dir and your app menu, with GTK 4 and WebKitGTK from apt"
fi
if [ "$has_ollama" = 1 ]; then
  echo "    - Ollama: already installed"
else
  echo "    - Ollama, with its install script, which uses sudo"
fi
if [ -n "$gpu" ]; then
  echo "    - meru setup, which offers the answer model that fits the GPU ($gpu) and downloads the models"
else
  echo "    - meru setup, which downloads the lite models, about 2.3 GB, and asks about your folders"
fi
[ "$has_systemd" = 1 ] && echo "    - merud, as a systemd user service started now and at every boot"
echo
echo "  Nothing goes anywhere but the downloads from GitHub, Ollama and your apt mirror."
echo

if [ "$all" != 1 ] && has_tty; then
  printf 'Install everything [a], choose each step [s], or quit [q]? ' >/dev/tty
  read -r choice </dev/tty || choice=q
  case "$choice" in
    [aA]*) all=1 ;;
    [sS]*) all=0 ;;
    *) echo "Nothing installed."; exit 0 ;;
  esac
fi

say "Installing Meru $version for Linux on $arch"

tmp="$(mktemp -d)"
# trap removes the download folder when the script ends, however it ends.
trap 'rm -rf "$tmp"' EXIT
for f in "$cli" SHA256SUMS; do
  curl -fsSL -o "$tmp/$f" "$base/$f" || fail "couldn't download $f from $base"
done

say "Checking the downloads against SHA256SUMS"
(cd "$tmp" && sha256sum -c SHA256SUMS --ignore-missing) || fail "a download doesn't match SHA256SUMS; nothing was installed"

say "Installing meru and merud in $bin_dir"
mkdir -p "$bin_dir"
# Releases up to v0.5.0 carry macOS extended attributes, which GNU tar
# would warn about once per file.
tar --warning=no-unknown-keyword -xzf "$tmp/$cli" -C "$tmp"
install -m 0755 "$tmp/meru-$version-linux-$arch/meru" "$tmp/meru-$version-linux-$arch/merud" "$bin_dir/"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *)
    echo "$bin_dir isn't on your PATH."
    if ask "Add it to ~/.bashrc?"; then
      echo 'export PATH="$HOME/.local/bin:$PATH"' >>"$HOME/.bashrc"
      echo "Added. Open a new terminal for it to take effect."
    fi
    export PATH="$bin_dir:$PATH"
    ;;
esac

if [ "$has_desktop" = 1 ] && ask "Install the desktop app?"; then
  for f in "$desktop" SHA256SUMS-desktop-linux; do
    curl -fsSL -o "$tmp/$f" "$base/$f" || fail "couldn't download $f from $base"
  done
  (cd "$tmp" && sha256sum -c SHA256SUMS-desktop-linux --ignore-missing) || fail "the desktop app doesn't match SHA256SUMS-desktop-linux; it wasn't installed"
  # The app draws its window with GTK 4 and shows its page in WebKitGTK.
  if command -v apt-get >/dev/null 2>&1; then
    if ! dpkg -s libgtk-4-1 libwebkitgtk-6.0-4 >/dev/null 2>&1; then
      if ask "Install GTK 4 and WebKitGTK 6.0 with apt (uses sudo)?"; then
        sudo apt-get install -y libgtk-4-1 libwebkitgtk-6.0-4
      else
        echo "The desktop app needs libgtk-4-1 and libwebkitgtk-6.0-4 to open."
      fi
    fi
  else
    echo "The desktop app needs GTK 4 and WebKitGTK 6.0; install them with your package manager."
  fi
  tar -xzf "$tmp/$desktop" -C "$tmp"
  src="$tmp/${desktop%.tar.gz}"
  apps="$HOME/.local/share/applications"
  icons="$HOME/.local/share/icons/hicolor/512x512/apps"
  mkdir -p "$apps" "$icons"
  install -m 0755 "$src/meru-desktop" "$bin_dir/"
  install -m 0644 "$src/meru.png" "$icons/meru.png"
  # A desktop entry doesn't expand ~ or $HOME, so the path goes in whole.
  sed "s|__HOME__|$HOME|g" "$src/meru.desktop" >"$apps/meru.desktop"
  chmod 0644 "$apps/meru.desktop"
  echo "Meru is in your app menu. From a terminal: meru-desktop"
elif [ "$has_desktop" = 0 ]; then
  echo "Release $version has no Linux desktop app; use meru and meru chat."
fi

say "Ollama runs the models"
# Meru's search model needs Ollama 0.40.5 or later. Ollama's script
# installs the newest one, and updates an older copy in place.
min_ollama="0.40.5"
# version_ge says whether dotted version $1 is at least $2, comparing each
# number in turn.
version_ge() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    n = split(a, x, "."); m = split(b, y, ".")
    for (i = 1; i <= 3; i++) { if (x[i] + 0 != y[i] + 0) { exit !(x[i] + 0 > y[i] + 0) } }
    exit 0 }'
}
ollama_version=""
if command -v ollama >/dev/null 2>&1; then
  # `ollama --version` prints "ollama version is 0.40.5", and may print a
  # warning line first; the last number on its last line is the version.
  ollama_version="$(ollama --version 2>/dev/null | tail -1 | grep -Eo '[0-9]+\.[0-9]+(\.[0-9]+)?' | tail -1 || true)"
fi
if [ -n "$ollama_version" ] && version_ge "$ollama_version" "$min_ollama"; then
  echo "Ollama $ollama_version is installed."
else
  [ -n "$ollama_version" ] && echo "Ollama $ollama_version is too old for Meru's search model; Meru needs $min_ollama or later."
  # The script sets Ollama up as a systemd service and finds an NVIDIA
  # or AMD GPU's driver. It runs sudo itself.
  if ask "Install Ollama with its script from ollama.com (uses sudo)?"; then
    curl -fsSL https://ollama.com/install.sh | sh
  else
    echo "Install Ollama with: curl -fsSL https://ollama.com/install.sh | sh"
  fi
fi

# ollama_up waits up to ten seconds for Ollama's API. Ollama's script starts
# Ollama as a systemd service; with no systemd, as in a container, nothing
# starts it, so ollama_up starts `ollama serve` itself, logging to
# ~/.ollama/serve.log, and waits again.
ollama_up() {
  local i
  for i in $(seq 1 10); do curl -fsS http://127.0.0.1:11434/api/version >/dev/null 2>&1 && return 0; sleep 1; done
  echo "Ollama isn't running; starting ollama serve."
  mkdir -p "$HOME/.ollama"
  nohup ollama serve >>"$HOME/.ollama/serve.log" 2>&1 &
  for i in $(seq 1 30); do curl -fsS http://127.0.0.1:11434/api/version >/dev/null 2>&1 && return 0; sleep 1; done
  echo "Ollama didn't start; see ~/.ollama/serve.log."
  return 1
}

say "Setting up Meru"
# meru setup asks its own questions: the answer model that fits this
# machine, your folders, web search and about you. It downloads the models
# it picks. It needs a terminal even when every step says yes.
if has_tty && command -v ollama >/dev/null 2>&1 && ollama_up && ask "Run meru setup now (models, folders, and questions about you)?"; then
  "$bin_dir/meru" setup </dev/tty || echo "meru setup stopped; run 'meru setup' again when you're ready."
elif command -v ollama >/dev/null 2>&1 && [ ! -f "$HOME/.meru/config.toml" ] && ask "Download the lite models and write a default config?"; then
  # No terminal, as over a pipe with MERU_YES=1: the lite profile's two
  # models and the config template, which merud starts with. A failed
  # download leaves the rest of the install to run.
  if ollama_up && ollama pull hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M && ollama pull embeddinggemma-2:440m; then
    mkdir -p "$HOME/.meru"
    "$bin_dir/meru" config template >"$HOME/.meru/config.toml"
    echo "Wrote ~/.meru/config.toml. Run 'meru setup' in a terminal to pick a larger answer model."
  else
    echo "The models didn't download; run 'meru setup' once Ollama runs."
  fi
else
  echo "Run 'meru setup' when you're ready."
fi

if [ "$has_systemd" = 1 ] && ask "Start merud now and at every boot, as a systemd user service?"; then
  unit_dir="$HOME/.config/systemd/user"
  mkdir -p "$unit_dir" "$HOME/.meru"
  # The repository's unit runs ~/go/bin/merud, where go install puts it;
  # a release lives in ~/.local/bin. %h is systemd's name for home.
  curl -fsSL "https://raw.githubusercontent.com/$repo/$version/deploy/systemd/merud.service" |
    sed "s|%h/go/bin/merud|%h/.local/bin/merud|" >"$unit_dir/merud.service"
  systemctl --user daemon-reload
  systemctl --user enable merud
  systemctl --user restart merud
  # A user service stops when you log out and starts when you log in.
  # Lingering starts it at boot, before anyone logs in, as a server needs.
  if [ "$(loginctl show-user "$USER" --property=Linger --value 2>/dev/null || true)" != "yes" ]; then
    if ask "Start merud at boot, before you log in (sudo loginctl enable-linger)?"; then
      sudo loginctl enable-linger "$USER"
    fi
  fi
  for _ in $(seq 1 30); do "$bin_dir/meru" ping >/dev/null 2>&1 && break; sleep 1; done
  "$bin_dir/meru" ping || echo "merud didn't answer yet; check: journalctl --user -u merud"
else
  echo "Start it yourself with: merud &"
fi

say "Done"
echo "Ask a question:   meru \"what can you do?\""
echo "Chat:             meru chat"
[ -x "$bin_dir/meru-desktop" ] && echo "Desktop app:      Meru in your app menu, or meru-desktop"
echo "Gmail, Calendar and Drive: https://github.com/$repo/blob/main/docs/google-setup.md"
