// This file builds the engines for [[endpoints]]: answer models on a
// server other than the local Ollama, which [models] main and the sets
// name as "@name". See docs/design/model-router.md.

package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/secrets"
)

// newEndpoints returns an engine.Endpoint for each [[endpoints]] entry, by
// name. Config has checked each entry already.
//
// An endpoint whose key secrets.toml lacks still goes in the map, with the
// reason in its Err: merud starts, the rest of Meru works, and a question
// for that model says what to fix. newEndpoints fails only when
// secrets.toml can't be read at all.
func newEndpoints(cfg config.Config, log *slog.Logger) (map[string]engine.Endpoint, error) {
	out := map[string]engine.Endpoint{}
	if len(cfg.Endpoints) == 0 {
		return out, nil
	}
	sec, err := secrets.Load(secrets.Path(cfg.Dir))
	if err != nil {
		return nil, fmt.Errorf("endpoints: %w", err)
	}
	for _, c := range cfg.Endpoints {
		ep := engine.Endpoint{Model: c.Model, Details: endpointDetails(c)}
		ep.Engine, ep.Err = newEndpointEngine(c, sec, log)
		if ep.Err != nil {
			log.Warn("an answer model in [[endpoints]] can't answer", "endpoint", c.Name, "err", ep.Err)
		} else {
			log.Info("answer endpoint", "endpoint", c.Name, "reach", c.Reach, "model", c.Model)
		}
		out[c.Name] = ep
	}
	return out, nil
}

// errNoKey is why an endpoint whose key secrets.toml lacks can't answer.
// It is a fixed sentence on purpose: nothing read from api_key, not even
// the entry's name, flows into a log line or an error, so neither can carry
// a key by mistake.
var errNoKey = errors.New("the api_key entry it names isn't in secrets.toml; add it there")

// newEndpointEngine builds the engine for one entry, with its key read
// from sec. It fails with errNoKey when the key's entry is missing from
// secrets.toml.
func newEndpointEngine(c config.Endpoint, sec *secrets.Secrets, log *slog.Logger) (engine.Engine, error) {
	key := ""
	if c.APIKey != "" {
		var err error
		if key, err = sec.Resolve(c.APIKey); err != nil {
			return nil, errNoKey
		}
	}
	ec := engine.EndpointConfig{Name: c.Name, Reach: c.Reach, URL: c.URL, Key: key, Model: c.Model}
	return engine.NewOpenAI(ec, nil, log)
}

// endpointDetails returns what an entry says its model can do, in the form
// Ollama's /api/show gives for a local model, so the vision and tool
// checks and the prompt's budget treat both alike.
func endpointDetails(c config.Endpoint) engine.ModelDetails {
	caps := []string{"completion"}
	if c.Vision {
		caps = append(caps, engine.Vision)
	}
	if c.CallsTools() {
		caps = append(caps, engine.ToolUse)
	}
	return engine.ModelDetails{Capabilities: caps, ContextLength: c.ContextLength}
}
