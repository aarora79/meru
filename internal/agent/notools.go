// This file holds what a turn does when the answer model can't call
// tools, as gemma3:12b can't: Ollama refuses any request that offers such
// a model a tool, with a 400 that says the model "does not support
// tools". Every route offers some tool, datetime and about_meru at
// least, so without this check every turn with that model would fail.
// Instead the turn offers no tools, answers from the model alone, and
// says so under the answer.

package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// noToolsNotice is the notice under an answer when the answer model,
// named at %s, can't call tools and the turn would have offered more than
// the tools every route offers, or the user picked the web scope.
const noToolsNotice = "%s can't call tools, so Meru answered without them: no mail, calendar, notes, web or file tools. " +
	"To use them, pick another answer model under Library, Models in the desktop app, or in [models] main in config.toml."

// UseToolCheck gives the agent a check of whether a model can call tools;
// merud passes one that reads OllamaEngine.Capabilities. Call it once,
// before the first Handle. Without it, every turn offers the tools its
// route gives, as before.
func (a *Agent) UseToolCheck(check func(ctx context.Context, model string) (bool, error)) {
	a.canCallTools = check
}

// offerable returns specs, the tools the turn would offer, or nil when
// the answer model can't call tools. In that case it also sets t.noTools
// to the model's name, so Handle adds noToolsNotice under the answer,
// unless specs held only the tools every route offers (see everyRoute):
// then the user asked nothing a tool would answer, and a notice on every
// plain question would only be noise. The web scope gets the notice all
// the same, since there the user asked for the web.
//
// A check that fails, as when Ollama is down, leaves specs as they are:
// the model call then fails with Ollama's own reason.
func (a *Agent) offerable(ctx context.Context, t *turn, specs []engine.ToolSpec) []engine.ToolSpec {
	if len(specs) == 0 || a.canCallTools == nil {
		return specs
	}
	model := a.Main()
	ok, err := a.canCallTools(ctx, model)
	if err != nil {
		a.log.DebugContext(ctx, "couldn't check whether the answer model can call tools", "model", model, "err", err)
		return specs
	}
	if ok {
		return specs
	}
	a.log.DebugContext(ctx, "the answer model can't call tools; the turn offers none", "model", model, "tools", len(specs))
	if t.scope == rpc.ScopeWeb || slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return !everyRoute(s.Name, true) }) {
		t.noTools = model
	}
	return nil
}

// noToolsText returns noToolsNotice for model.
func noToolsText(model string) string {
	return fmt.Sprintf(noToolsNotice, model)
}
