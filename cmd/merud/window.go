// This file sizes the answer model's context window: the num_ctx Ollama
// loads it with. merud asks Ollama what the model can read and how much
// memory a token of context takes, and gives it as much as fits in an
// eighth of the computer's memory, never less than [ollama]
// context_length. See ARCHITECTURE.md, "Context length".

package main

import (
	"context"
	"log/slog"

	"github.com/aarora79/meru/internal/engine"
)

// windowMemoryShare is the part of memory the answer model's context cache
// may take: one eighth, 8 GB on a 64 GB Mac. The model's weights, the fast
// and embedding models, and the user's own programs need the rest.
const windowMemoryShare = 8

// windowFitter returns the function that sizes model's window and sets it
// on eng, for startup and for each switch of the answer model. fallback
// is [ollama] context_length and memGB the computer's memory. It returns
// nil when eng isn't an OllamaEngine, as in tests with a fake.
//
// The function asks Ollama about model through Details, which caches the
// answer, works the window out with engine.FitWindow, and logs it. When
// Ollama can't say, or fallback is 0, which leaves the window to Ollama,
// the model keeps fallback. Call it before the model loads: a model that
// loaded with another window loads again on its next call.
func windowFitter(eng engine.Engine, fallback, memGB int, log *slog.Logger) func(ctx context.Context, model string) {
	oe, ok := eng.(*engine.OllamaEngine)
	if !ok || fallback <= 0 {
		return nil
	}
	budget := int64(memGB) << 30 / windowMemoryShare
	return func(ctx context.Context, model string) {
		d, err := oe.Details(ctx, model)
		if err != nil {
			log.WarnContext(ctx, "answer model window: Ollama can't describe the model; it keeps [ollama] context_length",
				"model", model, "window", fallback, "err", err)
			return
		}
		n := engine.FitWindow(d, budget, fallback)
		oe.SetWindow(model, n)
		log.InfoContext(ctx, "answer model window", "model", model, "window", n, "model_max", d.ContextLength,
			"cache_mb", int64(n)*d.KVBytesPerToken>>20, "fallback", fallback)
	}
}
