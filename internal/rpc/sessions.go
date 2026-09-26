// This file holds the types of the two session ops, OpSessions and
// OpSessionTurns, which let a client list past conversations and show one
// again. merud reads both from the session transcripts, the source of
// truth, so the answers never depend on meru.db.

package rpc

import "encoding/json"

// SessionInfo is one past conversation, as OpSessions lists it.
type SessionInfo struct {
	// ID names the session; send it back in Request.Session to continue it.
	ID string `json:"id"`
	// Title is the session's first question, on one line and cut to a
	// length a list can show.
	Title string `json:"title"`
	// Started is when the session began and Updated when its transcript
	// last changed, both RFC 3339.
	Started string `json:"started"`
	Updated string `json:"updated"`
	// Turns counts the questions asked in it.
	Turns int `json:"turns"`
}

// TurnInfo is one question of a past session and what came of it, as
// OpSessionTurns returns it. A question with no answer line, because the
// turn failed or was stopped, has an empty Answer.
type TurnInfo struct {
	// Time is when the question arrived (RFC 3339).
	Time     string `json:"time"`
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
	// Route is the route the turn took, and Outcome says how a turn ended
	// without a full answer ("timeout", "cut_off" or "gave_up"), as the
	// transcript's assistant line holds them.
	Route   string `json:"route,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Sources lists the files whose excerpts went into the prompt, as
	// shortened paths such as "~/Notes/lisbon.md". The transcript keeps
	// the files, not the excerpts, so there are no headings or lines.
	Sources []string `json:"sources,omitempty"`
	// Tools lists the turn's tool calls in the order they ran.
	Tools []ToolStep `json:"tools,omitempty"`
	// DurationMillis is how long the turn took, and TokensIn and TokensOut
	// are the main model's token counts.
	DurationMillis int64 `json:"duration_ms,omitempty"`
	TokensIn       int   `json:"tokens_in,omitempty"`
	TokensOut      int   `json:"tokens_out,omitempty"`
}

// ToolStep is one tool call of a past turn.
type ToolStep struct {
	// Name is the tool's full name as the model saw it, as on ToolEvent,
	// and Kind is "mcp", "a2a", "builtin" or "command".
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Args are the call's arguments as the transcript holds them, secrets
	// redacted.
	Args json.RawMessage `json:"args,omitempty"`
	// Outcome is "ok", "error", "denied", "declined", "cancelled" or
	// "timeout", and empty when the transcript has no result line for the
	// call.
	Outcome        string `json:"outcome,omitempty"`
	DurationMillis int64  `json:"duration_ms,omitempty"`
}

// ToolName joins a tool call's kind, server and tool, as a transcript line
// holds them, back into the full name the model saw: "<server>.<tool>" for
// MCP, "a2a.<agent>.<skill>" for A2A, "cmd.<name>" for a local command and
// "<tool>" for a built-in.
func ToolName(kind, server, tool string) string {
	switch kind {
	case "builtin":
		return tool
	case "command":
		return "cmd." + tool
	case "a2a":
		return "a2a." + server + "." + tool
	}
	if server == "" {
		return tool
	}
	return server + "." + tool
}
