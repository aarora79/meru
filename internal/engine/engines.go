// This file holds Engines, the Engine merud uses: the local Ollama, plus
// the answer models in [[endpoints]], which config names as "@name". Each
// call goes to the engine its model names, so the agent, the router and
// the indexer keep calling one Engine and never learn which API a model
// speaks. See docs/design/model-router.md.

package engine

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"
)

// Endpoint is one answer model on a server other than the local Ollama, as
// Engines holds it.
type Endpoint struct {
	// Engine speaks the endpoint's API: an *OpenAIEngine, or a fake in
	// tests.
	Engine Engine
	// Model is the name the server knows the model by; a call's
	// Options.Model becomes this.
	Model string
	// Details says what the model can do and how much it reads, from its
	// [[endpoints]] entry, since only Ollama reports these.
	Details ModelDetails
	// Err, when set, is why the endpoint can't answer, such as an API key
	// missing from secrets.toml. Every call to it fails with Err, and the
	// rest of Meru keeps working.
	Err error
}

// Engines is the Engine for the local Ollama and the endpoints. A call
// whose model starts with "@" goes to that endpoint; any other model, and
// every Embed, goes to the local Ollama. Build one with NewEngines. Its
// map never changes after that, so several goroutines may call it at once.
type Engines struct {
	local     *OllamaEngine
	endpoints map[string]Endpoint // by name, without the "@"
}

// This line checks at compile time that *Engines has every Engine method.
var _ Engine = (*Engines)(nil)

// NewEngines returns the Engine for local and the endpoints, keyed by
// their [[endpoints]] names.
func NewEngines(local *OllamaEngine, endpoints map[string]Endpoint) *Engines {
	return &Engines{local: local, endpoints: endpoints}
}

// Local returns the local Ollama engine, for the calls that only Ollama
// answers.
func (e *Engines) Local() *OllamaEngine {
	return e.local
}

// endpoint returns the endpoint model names, and true, when model starts
// with "@". It fails for an "@name" no entry has, which config refuses at
// load, and for an endpoint that can't answer.
func (e *Engines) endpoint(model string) (Endpoint, bool, error) {
	name, ok := strings.CutPrefix(model, "@")
	if !ok {
		return Endpoint{}, false, nil
	}
	ep, found := e.endpoints[name]
	if !found {
		return Endpoint{}, true, fmt.Errorf("no endpoint called %q", name)
	}
	if ep.Err != nil {
		return Endpoint{}, true, fmt.Errorf("endpoint %s: %w", name, ep.Err)
	}
	return ep, true, nil
}

// Generate sends the call to the engine opts.Model names.
func (e *Engines) Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error) {
	ep, ok, err := e.endpoint(opts.Model)
	if err != nil {
		return Completion{}, err
	}
	if !ok {
		return e.local.Generate(ctx, msgs, tools, opts)
	}
	opts.Model = ep.Model
	return ep.Engine.Generate(ctx, msgs, tools, opts)
}

// Stream sends the call to the engine opts.Model names.
func (e *Engines) Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error) {
	ep, ok, err := e.endpoint(opts.Model)
	if err != nil {
		return nil, err
	}
	if !ok {
		return e.local.Stream(ctx, msgs, tools, opts)
	}
	opts.Model = ep.Model
	return ep.Engine.Stream(ctx, msgs, tools, opts)
}

// Embed always goes to the local Ollama: the embedding model never runs
// anywhere else.
func (e *Engines) Embed(ctx context.Context, inputs []Input) ([]Vector, error) {
	return e.local.Embed(ctx, inputs)
}

// Info reports the local Ollama, the runtime merud depends on.
func (e *Engines) Info(ctx context.Context) (ModelInfo, error) {
	return e.local.Info(ctx)
}

// Details returns what model can do: from its [[endpoints]] entry for an
// endpoint, from Ollama otherwise.
func (e *Engines) Details(ctx context.Context, model string) (ModelDetails, error) {
	ep, ok, err := e.endpoint(model)
	if err != nil {
		return ModelDetails{}, err
	}
	if ok {
		return ep.Details.clone(), nil
	}
	return e.local.Details(ctx, model)
}

// Capabilities returns the capabilities Details lists.
func (e *Engines) Capabilities(ctx context.Context, model string) ([]string, error) {
	d, err := e.Details(ctx, model)
	if err != nil {
		return nil, err
	}
	return d.Capabilities, nil
}

// Pulled lists the models Ollama has on disk, then each endpoint as
// "@name" with no size, since a model set may name one and a switch
// checks the list first. An endpoint that can't answer stays off the
// list, so a switch to it fails with the command's own words.
func (e *Engines) Pulled(ctx context.Context) ([]PulledModel, error) {
	out, err := e.local.Pulled(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(e.endpoints))
	for name, ep := range e.endpoints {
		if ep.Err == nil {
			names = append(names, name)
		}
	}
	// A map has no order; sort so the list reads the same each time.
	slices.Sort(names)
	for _, name := range names {
		out = append(out, PulledModel{Name: "@" + name})
	}
	return out, nil
}

// Unload asks Ollama to drop model from memory. An endpoint's model isn't
// in this machine's memory, so there is nothing to do.
func (e *Engines) Unload(ctx context.Context, model string) error {
	if strings.HasPrefix(model, "@") {
		return nil
	}
	return e.local.Unload(ctx, model)
}

// SetWindow sets the window Ollama loads model with. An endpoint's window
// is its context_length, which nothing here changes.
func (e *Engines) SetWindow(model string, n int) {
	if strings.HasPrefix(model, "@") {
		return
	}
	e.local.SetWindow(model, n)
}

// Window returns the context window model reads: an endpoint's
// context_length, or what Ollama loads a local model with.
func (e *Engines) Window(model string) int {
	ep, ok, err := e.endpoint(model)
	if ok {
		if err != nil {
			return 0
		}
		return ep.Details.ContextLength
	}
	return e.local.Window(model)
}
