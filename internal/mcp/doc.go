// Package mcp is Meru's client for MCP (Model Context Protocol) servers: the
// programs that give the model tools to call. See ARCHITECTURE.md, "MCP".
//
// A Pool starts or connects to each configured server, lists its tools, and
// keeps only the tools the server's allow list names. It renames each kept
// tool to "<server>.<tool>", so two servers can offer a tool with the same
// name without one hiding the other. The agent loop reads the kept tools with
// Tools, and calls one with Call. At the start of each turn that offers
// tools, Refresh lists each connected server's tools again and gives each
// server that isn't connected one try, so the list follows a server that
// restarted. Nothing runs between turns.
//
// A managed server, a connector, is the one exception: its ServerConfig
// carries a Spawner, the Spawn hook, which the supervisor in
// internal/connectors provides. The Pool then connects to nothing at
// startup or in Refresh; it offers the tools the supervisor reports and
// asks it for a session on each call. The supervisor starts, stops and
// restarts the program. The servers added by hand in [[mcp.servers]]
// keep the rules above.
//
// Probe starts a server for a moment, before it goes into config, and
// reports every tool it offers with the server's read-only and destructive
// hints. It calls no tool, so it needs no allow list.
//
// It speaks the two transports in the current MCP spec, both through the
// official Go SDK (github.com/modelcontextprotocol/go-sdk):
//
//   - stdio: the Pool starts the server as a child process and talks JSON-RPC
//     over its stdin and stdout.
//   - Streamable HTTP: the Pool connects to a server that is already running,
//     at a URL that must be loopback unless the entry says remote = true.
//
// What the package leaves to others: it doesn't ask the user to confirm a
// call (NeedsConfirm only answers the question), and it doesn't write the
// tool_calls audit row or the transcript lines. Both belong to dispatch in
// the agent loop, the one path every tool call takes (AGENTS.md,
// non-negotiable 4). Nothing outside dispatch should call Pool.Call.
//
// It doesn't confine servers either. A stdio server runs as an ordinary
// process with the user's permissions; the Pool only trims the environment
// it hands the child.
package mcp
