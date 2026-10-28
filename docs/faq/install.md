# How do I install Meru, on a Mac or on Linux?

## On a Mac

You need Ollama to run the models, then Meru itself.

1. **Install Ollama** 0.40.5 or later from <https://ollama.com/download>, and
   check that it answers:

   ```sh
   curl http://127.0.0.1:11434/api/version
   ```

2. **Install Meru** with the install script. It downloads the latest release,
   checks it and asks before each step:

   ```sh
   curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
   ```

   It puts `meru` and `merud` in `~/.local/bin` and `Meru.app` in
   `/Applications`. If `which meru` finds nothing, add
   `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` and open a new terminal.

3. **Run setup.** It checks Ollama, pulls the models, asks which folders to
   search and writes `~/.meru/config.toml`:

   ```sh
   meru setup
   ```

4. **Start `merud` and ask something:**

   ```sh
   merud &
   meru "what is the capital of France?"
   ```

If macOS won't open `Meru.app` because it can't check it for malicious
software, and you trust the download, clear the quarantine mark once:

```sh
xattr -dr com.apple.quarantine /Applications/Meru.app
```

## On Linux

Tested on Ubuntu 24.04. One script installs Meru and offers the rest:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install-linux.sh | bash
```

It checks the release against `SHA256SUMS`, puts `meru` and `merud` in
`~/.local/bin`, then asks before each of these:

- the desktop app, with GTK 4 and WebKitGTK 6.0 from `apt`, and an entry in
  your app menu;
- Ollama, through Ollama's own install script, which runs it as a systemd
  service and finds an NVIDIA card's driver;
- `meru setup`, which offers the largest answer model that fits an NVIDIA
  card's memory, or `lite` without one;
- `merud` as a systemd user service, started now and at each login, or at boot
  if you let the script turn on linger.

Speech to text works on x86-64 Linux: turn it on in Settings, Connections,
and Meru downloads its own Linux build of whisper.cpp's server.

## From source

**Build from source instead** with Go 1.26 or later:

```sh
git clone https://github.com/aarora79/meru.git
cd meru
go install ./cmd/merud ./cmd/meru     # puts both in ~/go/bin
```

More detail: [running.md, steps 1 to 4](../running.md#1-install-the-prerequisites).
