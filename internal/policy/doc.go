// Package policy holds no code that ships. Its tests read Meru's own source
// and fail `go test ./...` when the code breaks one of the non-negotiables in
// AGENTS.md, so the rules hold on every run, not only when a reviewer spots a
// problem.
//
// The tests check four things:
//
//   - No Go file imports a cloud-model SDK, and go.mod requires none
//     (non-negotiable 1, ARCHITECTURE.md "Privacy boundary").
//   - No Go file imports a telemetry, crash-report or self-update library
//     (non-negotiable 2).
//   - No shipped Go file holds a string literal naming a cloud-model API
//     host, or an http(s) URL whose host isn't loopback, unless
//     allowed_urls.txt lists it with a reason (non-negotiables 1 and 2).
//   - The clients stay thin (AGENTS.md "Shape"). cmd/meru and the desktop
//     app (cmd/meru-desktop with internal/desktop) never reach the engine,
//     the store, the agent loop or any other package that runs in merud,
//     and never set up the OpenTelemetry SDK. The desktop app also leaves
//     out catalog, secrets and tui, and never uses Wails' updater.
//
// The deny-lists live in testdata/*.txt, one entry per line, so that no Go
// string literal in this package names a provider host. The URL check reads
// string literals only; a link in a comment is documentation and passes.
//
// What this package deliberately doesn't do: it can't prove that no code
// reaches the network. A host built at run time from pieces slips past it.
// Code review and the loopback checks in config and obs remain the real
// defence; these tests catch the plain mistakes.
package policy
