// Package loopback holds one rule: an address Meru talks to must be on this
// machine. CheckURL accepts an http or https URL whose host is in
// 127.0.0.0/8, ::1, or "localhost" when every address it resolves to is
// loopback, and refuses everything else.
//
// The engine (Ollama's address), config (validation at startup) and obs (the
// OTLP endpoint) all call it, so the rule lives in one place. The package
// imports only the standard library, so the thin meru client can depend on
// config without pulling in the engine. See ARCHITECTURE.md, "Privacy
// boundary".
package loopback
