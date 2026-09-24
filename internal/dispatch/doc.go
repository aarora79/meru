// Package dispatch is the one path every tool call takes: MCP tools, A2A
// agent skills and merud's built-in tools. See ARCHITECTURE.md, "Agent
// loop", step 4, and AGENTS.md, non-negotiable 4.
//
// For each call it checks the allowlist, asks the user when the tool needs
// a yes, runs the call on the backend that owns the tool, writes the
// transcript lines and the tool_calls row, and records the span and
// metrics. A call to a tool outside the allowlist doesn't run; it still
// gets a row, with outcome "denied".
//
// What it deliberately doesn't do: it doesn't pick tools or talk to the
// model (the agent loop does), and it doesn't know how MCP, A2A or the
// built-ins work inside; each is a Backend.
package dispatch
