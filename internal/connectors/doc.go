// Package connectors describes the programs Meru can install, start and
// check for the user: SearXNG for web search, the Obsidian and Google MCP
// servers, and Ollama, which Meru only reports on. Each one has a manifest,
// a TOML file in manifests/ compiled into merud, that pins its version and
// says how to install it, how to start it, what to ask the user and how to
// tell that it works. See ARCHITECTURE.md, "Connectors and the supervisor".
//
// This first part of the package only reads and checks the manifests.
// Load parses every embedded manifest and refuses one that breaks a rule:
// a version that isn't exact, a container image without a digest, a
// download without a checksum, a secret with a default, and the rest that
// Validate lists. merud doesn't call Load yet; the supervisor that uses the
// manifests arrives in later changes (issue #87).
//
// What the package chooses not to do: it runs no program, reads no user
// file and touches no network. The clients never import it
// (internal/policy): merud owns the connectors, and a client asks merud
// about them over the socket.
package connectors
