// Package config reads ~/.meru/config.toml, fills in defaults from the chosen
// model profile, and checks every value once at startup.
//
// merud fails fast with a clear message when config is wrong, for example when
// the Ollama or OTLP address isn't loopback. After Load returns, the rest of
// Meru can trust the values it gets. See ARCHITECTURE.md, "Model tiers" and
// "Observability", and docs/fast-router.md for the [router] keys.
//
// Template returns the commented config.toml that meru setup writes on first
// run: every key, with the defaults uncommented. config.example.toml at the
// repo root is a copy of it.
//
// What this package deliberately doesn't do: it never writes config.toml.
// Changing config is the job of the built-in configure tool, which always asks.
package config
