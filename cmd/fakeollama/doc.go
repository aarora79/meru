// Command fakeollama serves the test fake from internal/testutil/fakeollama
// on a loopback port, so the end-to-end tests can point a real merud at it
// instead of a real Ollama. It is a test tool; Meru never ships or runs it.
//
// Usage:
//
//	fakeollama -addr 127.0.0.1:0 [-script replies.json] [-models a,b] [-latency 50ms] [-vision a]
//
// -vision names the models /api/show says can look at images.
//
// On start it prints one line, "listening on http://127.0.0.1:PORT", so the
// caller can read the port it got. It stops on SIGINT or SIGTERM. Tests
// script replies and read back requests through the /_fake/ endpoints (see
// the fakeollama package).
package main
