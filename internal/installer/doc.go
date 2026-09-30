// Package installer is the Mac installer's logic, minus the window: the
// ten steps that take a Mac from nothing to a running Meru, and the
// Bridge that the installer's page calls. cmd/meru-installer opens the
// window and binds the Bridge; everything else lives here, where tests run
// on any machine without Wails. See ARCHITECTURE.md, "Installer".
//
// The installer runs before merud exists, so unlike the clients it starts
// programs and writes files itself:
//
//   - It runs programs only through run.go, which holds a fixed allowlist
//     of absolute paths (brew, launchctl, open, ditto, xattr, codesign,
//     sysctl, sw_vers, df) and starts each with os/exec, no
//     shell, and arguments the code builds. internal/policy checks that no
//     other file imports os/exec and that the list holds no shell.
//   - It changes config.toml only through internal/catalog, which edits
//     one list or string, keeps every comment, and checks that the result
//     loads before it replaces the file. A missing config.toml starts as
//     the template.
//   - It writes the profile as memory files through internal/memory, the
//     files merud reads.
//   - It talks to merud only over the socket, through internal/rpc. The
//     connectors, SearXNG, Obsidian and Google, are merud's: their steps
//     gather what each needs, and the Start Meru step hands them to merud
//     with connector_set or connector_adopt (connectors.go).
//
// What it doesn't do: it never imports the engine, the agent loop, the
// store or dispatch, never sends a prompt to a model, and never checks for
// updates. It reaches the internet only after the user presses Continue
// on a step that says so: through Homebrew for Ollama, and, on a Mac with
// no Homebrew, straight to Ollama's own download of Ollama.app. merud
// downloads the connectors, at the versions pinned in its release.
package installer
