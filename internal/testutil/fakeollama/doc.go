// Package fakeollama is a stand-in for Ollama's HTTP API, for tests.
//
// Meru's unit tests never need a real model (AGENTS.md, "Tests"). Tests that
// exercise OllamaEngine, the router or merud start a Fake instead: an HTTP
// handler that speaks the parts of Ollama's API Meru uses (/api/version,
// /api/ps, /api/show, /api/chat, /api/generate and /api/embed), answers
// with scripted replies, and records every request so the test can check
// what Meru sent.
//
// A test can script a reply's text, tool calls, log probabilities and usage
// counters, and can inject an HTTP error, a mid-stream error or a delay. The
// cmd/fakeollama program serves the same handler on a loopback port, so the
// end-to-end tests can run the real merud and meru binaries against it; it
// scripts replies through the /_fake/ control endpoints.
//
// What this package deliberately doesn't do: it runs no model and makes no
// attempt to produce sensible text. Unscripted calls get a fixed default
// reply. It covers only the request and response fields Meru reads.
package fakeollama
