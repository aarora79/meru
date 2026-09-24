// Package agent runs one turn of a conversation: it loads the session, asks
// the router for a route, searches the user's files when the route says so,
// builds the prompt, streams the main model's answer back to the client and
// writes both sides to the session transcript.
//
// See ARCHITECTURE.md, "Agent loop" and "A question, end to end". A turn
// makes one model call. On the "search" and "search+tools" routes it first
// runs hybrid search (internal/retrieve, through the Searcher interface),
// adds the excerpts to the system prompt with numbered citations, and
// tells the client which files it used in a "sources" event. Tools with
// dispatch arrive in v0.3; until then the "tools" route answers directly.
//
// What this package deliberately doesn't do: it holds no socket code (that's
// internal/rpc), picks no model names (those come from config), and uses no
// agent framework. The loop is ours to read.
package agent
