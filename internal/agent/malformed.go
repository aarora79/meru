// This file counts the tool calls the main model writes that Meru can't
// run as written. `/usage by model` shows the count per model, and the
// meru.model.malformed_calls metric records it, because a model that
// mangles one call in ten is no use in a loop of tool calls, and no public
// benchmark measures that on your own questions (ARCHITECTURE.md,
// "Metrics").

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
)

// Why a call counts as malformed, for the debug log line. Each is fixed
// text, so the log never holds what the model wrote.
const (
	// whyUnreadable: Ollama couldn't parse what the model wrote into a
	// tool call and sent engine.ErrModelOutput in the middle of the stream.
	whyUnreadable = "ollama couldn't read the call"
	// whyNotOffered: the call names a tool this round didn't offer,
	// whether or not the tool exists.
	whyNotOffered = "the round didn't offer the tool"
	// whyBadArgs: the call's arguments aren't a JSON object, which every
	// tool's schema asks for.
	whyBadArgs = "the arguments aren't a JSON object"
)

// malformed counts one tool call that model wrote and Meru couldn't run
// as written: it adds one to the turn's count, which the assistant line
// keeps, and to the meru.model.malformed_calls metric, and logs why at
// debug level.
func (a *Agent) malformed(ctx context.Context, t *turn, model, why string) {
	t.badCalls++
	obs.RecordMalformedCall(ctx, model)
	a.log.DebugContext(ctx, "malformed tool call", "model", model, "why", why, "round", t.rounds)
}

// checkCalls counts each of calls, the tool calls one round wrote, that
// names a tool offer doesn't hold, or whose arguments aren't a JSON
// object. It changes nothing about how the calls run: a call to a tool
// that doesn't exist still goes to dispatch, which refuses it and records
// it, and a call made when no tools were offered still doesn't run (see
// converse).
func (a *Agent) checkCalls(ctx context.Context, t *turn, model string, calls []engine.ToolCall, offer []engine.ToolSpec) {
	for _, c := range calls {
		switch {
		case !offered(c.Name, offer):
			a.malformed(ctx, t, model, whyNotOffered)
		case !isObject(c.Arguments):
			a.malformed(ctx, t, model, whyBadArgs)
		}
	}
}

// offered reports whether offer holds a tool called name.
func offered(name string, offer []engine.ToolSpec) bool {
	return slices.ContainsFunc(offer, func(s engine.ToolSpec) bool { return s.Name == name })
}

// isObject reports whether args, a call's arguments as JSON, is an object.
// No arguments at all counts as an empty object, as argsOf treats it.
// Ollama parses the model's call itself, so arguments that aren't an
// object are rare; a model that writes a string or a list there still
// gets counted.
func isObject(args []byte) bool {
	args = bytes.TrimSpace(args)
	if len(args) == 0 {
		return true
	}
	return args[0] == '{' && json.Valid(args)
}
