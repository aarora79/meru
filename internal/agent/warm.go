// This file holds the startup warm-up of the answer model: merud loads it
// in the background once the socket is open, and a question that comes
// before the load ends waits for it. See ARCHITECTURE.md, "Model tiers".

package agent

import (
	"context"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// warmQuestion is the user's message in the warm-up prompt. The model
// writes one token of answer, which nobody reads.
const warmQuestion = "hi"

// StartWarm marks the answer model as loading, so a turn that reaches its
// first model call from now on waits for the load (see WaitWarm), and
// returns the function that loads it. merud calls StartWarm before the
// socket takes questions and runs the function it returns in its own
// goroutine, which owns the load: the function returns when the load ends
// or ctx does. Call StartWarm once.
//
// A function that returns a function: the inner one keeps done, the
// channel made here, and closes it when the load ends, since in Go the
// side that sends on a channel, or signals with one, closes it.
func (a *Agent) StartWarm() func(ctx context.Context) {
	done := make(chan struct{})
	a.warmMu.Lock()
	a.warming = done
	a.warmMu.Unlock()
	return func(ctx context.Context) {
		// defer runs close(done) when the load returns, whatever happened,
		// so no turn waits forever.
		defer close(done)
		a.warm(ctx)
	}
}

// WaitWarm waits until the startup load of the answer model ends, and
// returns at once when merud started none or it has ended. A model switch
// calls it too, so a switch doesn't unload the model while it loads. It
// fails only when ctx ends first.
func (a *Agent) WaitWarm(ctx context.Context) error {
	a.warmMu.Lock()
	done := a.warming
	a.warmMu.Unlock()
	if done == nil {
		return nil
	}
	// select waits for whichever case is ready first: the load ending, or
	// ctx ending, as when the user cancels the question.
	select {
	case <-done:
		return nil
	default:
	}
	a.log.DebugContext(ctx, "waiting for the answer model to load")
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// warm loads the answer model into Ollama with the opening of a real
// prompt and one token of answer, and logs when it is warm.
//
// A real session showed why the opening of a real prompt, and not "hi"
// alone. merud warmed a 38 GB mixture-of-experts model with "hi" before it
// took questions, and Ollama kept it loaded, as keep_alive -1 asks. Yet
// the first question, 26 minutes later, spent 2 minutes 17 seconds on a
// prompt of 1,349 tokens, and the next spent 6 seconds on 1,460. In such a
// model each token runs through a few of many experts, so a prompt of
// three tokens reads a sliver of the weights from disk, and the first long
// prompt reads the rest. The warm-up prompt here is the system prompt a
// direct question gets, a thousand tokens or more, so the load reads most
// of the weights before the user asks, and Ollama keeps the prompt's
// opening for the first question to reuse.
//
// A load that fails, say because the model isn't pulled, logs an error
// with the command that fetches it; merud keeps running, and a question
// then fails with Ollama's reason.
func (a *Agent) warm(ctx context.Context) {
	model, noThink := a.mainModel()
	start := time.Now()
	specs := a.toolSpecs("direct")
	if a.canCallTools != nil {
		// Ollama refuses tools for a model that can't call them; see
		// notools.go. A check that fails leaves the tools on.
		if ok, err := a.canCallTools(ctx, model); err == nil && !ok {
			specs = nil
		}
	}
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	var list string
	if a.skills != nil {
		if reg := a.skills.Registry(ctx); reg != nil {
			list = skillList(reg, names)
		}
	}
	system, _ := a.stablePart(ctx, noteFor(specs), list)
	msgs := buildMessages(system, nil, warmQuestion)
	a.log.InfoContext(ctx, "loading the answer model", "model", model)
	if _, err := a.engine.Generate(ctx, msgs, specs, engine.Options{Model: model, MaxTokens: 1, NoThink: noThink}); err != nil {
		if ctx.Err() != nil {
			return // merud is stopping
		}
		a.log.ErrorContext(ctx, "couldn't load the answer model", "model", model, "err", err,
			"try", "ollama pull "+model)
		return
	}
	a.log.InfoContext(ctx, "answer model warm", "model", model, "ms", time.Since(start).Milliseconds())
}
