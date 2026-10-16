// This file holds the warm-up calls merud makes to the model runtime for
// the fast and embedding models. The Ollama version check is in ollama.go,
// against config.MinOllama. The answer model warms in the
// background; see agent.StartWarm.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// warm loads the fast and embedding models into Ollama with a tiny request
// each, once Ollama answers and before merud takes questions: the router needs the fast model for
// every question, and the store needs the embedding model to open. Both
// are small, so this takes a second or two. The engine sends keep_alive
// from config with every call, so the models then stay loaded.
//
// The answer model, often tens of gigabytes, loads in the background once
// the socket is open; see agent.StartWarm. In the lite profile it is the
// fast model, which this has loaded already. warm fails on the first model
// that can't load, which usually means it hasn't been pulled yet, and says
// so.
func warm(ctx context.Context, eng engine.Engine, m config.Models, log *slog.Logger) error {
	hello := []engine.Message{{Role: engine.RoleUser, Content: "hi"}}
	start := time.Now()
	log.DebugContext(ctx, "warming", "model", m.Fast)
	if _, err := eng.Generate(ctx, hello, nil, engine.Options{Model: m.Fast, MaxTokens: 1}); err != nil {
		return fmt.Errorf("warm %s: %w (try `ollama pull %s`)", m.Fast, err, m.Fast)
	}
	log.Info("warmed", "model", m.Fast, "ms", time.Since(start).Milliseconds())

	start = time.Now()
	log.DebugContext(ctx, "warming", "model", m.Embed)
	if _, err := eng.Embed(ctx, engine.Texts("hi")); err != nil {
		return fmt.Errorf("warm %s: %w (try `ollama pull %s`)", m.Embed, err, m.Embed)
	}
	log.Info("warmed", "model", m.Embed, "ms", time.Since(start).Milliseconds())
	return nil
}
