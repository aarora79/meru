// Package connectors describes the programs Meru can install, start and
// check for the user: SearXNG for web search, the Obsidian and Google MCP
// servers, and Ollama, which Meru only reports on. Each one has a manifest,
// a TOML file in manifests/ compiled into merud, that pins its version and
// says how to install it, how to start it, what to ask the user and how to
// tell that it works. See ARCHITECTURE.md, "Connectors and the supervisor".
//
// The package has three parts so far (issue #87, steps 1 to 3):
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
//   - The supervisor. merud builds one Supervisor per stdio connector
//     (supervisor.go). It checks the user's [connectors.<id>] values
//     against the manifest (status.go), installs and health-checks the
//     connector, keeps its tool list (health.go), starts it on the first
//     tool call, stops it when idle, and restarts it after a crash with a
//     growing wait. The MCP pool asks it for a session through its Spawn
//     method, and the connectors op reports its Status.
//
// What the package chooses not to do: it never asks PATH for a program,
// never touches the user's own npm, pip, uv or Homebrew folders, and
// leaves alone a connector the user set up by hand in [[mcp.servers]]. It
// supervises no container, HTTP server or dependency yet: SearXNG,
// Ollama and Google come in later steps. The clients never import it
// (internal/policy): merud owns the connectors, and a client asks merud
// about them over the socket.
package connectors
