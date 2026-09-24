// Package builtin holds the tools built into merud, as one dispatch.Backend.
// v0.3 has one: configure, which adds an MCP server to config.toml when you
// ask in chat ("connect my Gmail"). See ARCHITECTURE.md, "First run and
// setup" and "Approving a tool call".
//
// configure always asks. dispatch shows the user the exact arguments and
// offers only "approve once" and "deny", whatever [builtin] confirm says,
// because config grants lasting trust and the model mustn't grant any to
// itself. It writes through catalog.AppendServer, the same code `meru mcp
// add` uses, so the two paths can't drift apart.
//
// What it doesn't do: it never takes an API key. A server that needs a key
// not yet in secrets.toml isn't written; the tool tells the model to send
// the user to `meru mcp add <name>` in a terminal, so keys never pass
// through the model or the transcript. It also doesn't restart servers
// itself: it calls the onChange hook merud gives it, and merud decides how
// to reload.
package builtin
