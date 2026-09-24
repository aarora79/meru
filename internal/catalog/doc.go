// Package catalog is the short list of MCP servers Meru knows how to set up:
// web search, web page fetch, Gmail, Calendar, Drive and Docs, and
// Obsidian. See ARCHITECTURE.md, "Adding an MCP server".
//
// Each Entry says how to start the server, what it needs from the user (an
// API key, a sign-in), and a safe starting allow list: tools that read are
// allowed, and tools that send, write or change anything also sit in
// confirm, so each call asks first. Block renders an entry as the
// [[mcp.servers]] text for config.toml, and AppendServer adds that text to
// the file after checking that config still loads.
//
// Both `meru setup` / `meru mcp add` (in the thin client) and the built-in
// configure tool (in merud) use this package, so it imports only config,
// loopback and secrets: nothing that talks to a model or stores data.
//
// What it doesn't do: it doesn't install servers, start them or talk to
// them. It writes config; merud starts the servers the next time it starts.
package catalog
