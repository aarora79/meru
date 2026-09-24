// Package catalog is the short list of MCP servers Meru knows how to set up:
// google (Gmail, Calendar, Drive and Docs) and obsidian (notes). It has no
// shell server: merud runs the programs you declare in [[commands]] itself.
// It has no web search server either: web search is a built-in tool, and
// this package holds CheckSearXNG, the check meru setup and merud share for
// it. See ARCHITECTURE.md, "Adding an MCP server" and "Web search".
//
// Each Entry says how to reach the server, what it needs from the user (an
// API key, a sign-in, a server to start), and a safe starting allow list: tools that
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
// them. It edits config; merud starts the stdio servers and connects to
// the url ones, which the user starts, and `meru mcp add` asks merud to
// try a server before it writes the entry.
package catalog
