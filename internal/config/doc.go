// Package config reads ~/.meru/config.toml, fills in defaults from the chosen
// model profile, and checks every value once at startup.
//
// merud fails fast with a clear message when config is wrong, for example when
// the Ollama or OTLP address isn't loopback. After Load returns, the rest of
// Meru can trust the values it gets. See ARCHITECTURE.md, "Model tiers" and
// "Observability", and docs/fast-router.md for the [router] keys.
//
// What this package deliberately doesn't do: it never writes config.toml.
// Changing config is the job of the built-in configure tool, which always asks.
package config
