// Package agent runs one turn of a conversation: it loads the session, asks
// the router for a route, searches the user's files when the route says so,
// builds the prompt, runs the main model's rounds with any tool calls, and
// writes the question and the final answer to the session transcript.
//
// See ARCHITECTURE.md, "Agent loop" and "A question, end to end". On every
// route but "direct" a turn first runs hybrid search (internal/retrieve,
// through the Searcher interface), adds the excerpts to the system prompt
// with numbered citations, and tells the client which files it used in a
// "sources" event. On the "tools" and "search+tools" routes it offers the
// model the allowed tools, runs the calls the model makes through the
// ToolRunner (dispatch, in merud), and calls the model again with the
// results, up to [agent] max_rounds model calls per turn. While the router
// decides, one short call to the fast model picks the skills the question
// needs; the prompt lists every skill and holds the picked skills'
// instructions (ARCHITECTURE.md, "Skills"). On every route, direct
// included, it puts the user's profile and the memories recalled for the
// question (through the Profile interface) into the system prompt. On the
// routes that search, it also recalls up to three past sessions and adds
// them under "From earlier conversations" (earlier.go). A question that
// asks for the web, gives a URL, or names a thing the user's files don't
// cover gets a web search or page fetch before the model's first round,
// through the ToolRunner like any call, under "From the web" (webfirst.go,
// ARCHITECTURE.md, "Web first"); the web calls of a turn leave short notes
// on its answer line for later turns (webnotes.go).
//
// What this package deliberately doesn't do: it holds no socket code (that's
// internal/rpc), picks no model names (those come from config), never calls
// a tool except through the ToolRunner, and uses no agent framework. The
// loop is ours to read.
package agent
