// Package connectors describes the programs Meru can install, start and
// check for the user: SearXNG for web search, the Obsidian and Google MCP
// servers, and Ollama, which Meru only reports on. Each one has a manifest,
// a TOML file in manifests/ compiled into merud, that pins its version and
// says how to install it, how to start it, what to ask the user and how to
// tell that it works. See ARCHITECTURE.md, "Connectors and the supervisor".
//
// The package has two parts so far (issue #87, steps 1 and 2):
//
//   - The manifests. Load parses every embedded manifest and refuses one
//     that breaks a rule: a version that isn't exact, a container image
//     without a digest, a download without a checksum, a secret with a
//     default, and the rest that Validate lists.
//   - The installs. An Installer downloads the pinned Node and uv into
//     ~/.meru/runtime (runtimes.go), checking each archive's SHA-256
//     before it unpacks it (download.go); installs a connector's package
//     into ~/.meru/runtime/pkg/<id>-<version>/ (install.go); and says how
//     to start the installed program (launch.go). Every program it runs
//     goes through run.go, by absolute path, with no shell and a short
//     environment.
//
// merud calls none of this yet. The supervisor that will start, check and
// restart connectors arrives in step 3.
//
// What the package chooses not to do: it never asks PATH for a program,
// never touches the user's own npm, pip, uv or Homebrew folders, and
// starts no connector itself. The clients never import it
// (internal/policy): merud owns the connectors, and a client asks merud
// about them over the socket.
package connectors
