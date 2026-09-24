// Package agent runs one turn of a conversation: it loads the session, asks
// the router for a route, builds the prompt, streams the main model's answer
// back to the client and writes both sides to the session transcript.
//
// See ARCHITECTURE.md, "Agent loop" and "A question, end to end". v0.1 runs
// one model call per turn. Retrieval (v0.2) and tools with dispatch (v0.3)
// slot in between routing and the answer later; until then every route
// answers directly, and the chosen route is still reported and measured.
//
// What this package deliberately doesn't do: it holds no socket code (that's
// internal/rpc), picks no model names (those come from config), and uses no
// agent framework. The loop is ours to read.
package agent
