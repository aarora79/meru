// Package catalog is the short list of MCP servers Meru knows how to set up:
// web search, web page fetch, files in chosen folders, Gmail, Calendar,
// Drive and Docs, Obsidian, and on Windows the desktop. It has no shell
// server: merud runs the programs you declare in [[commands]] itself.
// See ARCHITECTURE.md, "Adding an MCP server".
//
// Each Entry says how to start the server, what it needs from the user (an
// API key, a sign-in, folders), and a safe starting allow list: tools that
// read are allowed, and tools that send, write, run or change anything also
// sit in confirm, so each call asks first. Block renders an entry as the
// [[mcp.servers]] text for config.toml. AppendServer adds that text to the
// file, and RemoveServer takes a server's block out again; both check that
// config still loads before they replace the file.
//
// Both `meru setup` / `meru mcp` (in the thin client) and the built-in
// configure tool (in merud) use this package, so it imports only config,
// loopback and secrets: nothing that talks to a model or stores data.
//
// What it doesn't do: it doesn't install servers, start them or talk to
// them. It edits config; merud starts the servers, and `meru mcp add` asks
// merud to try a server before it writes the entry.
package catalog
