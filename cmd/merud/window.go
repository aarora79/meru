// This file sizes the answer model's context window: the num_ctx Ollama
// loads it with. merud asks Ollama what the model can read and how much
// memory a token of context takes, and gives it as much as fits in an
// eighth of the memory models load into, never less than [ollama]
// context_length. See ARCHITECTURE.md, "Context length".

package main

import (
	"context"
	"log/slog"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// windowMemoryShare is the part of memory the answer model's context cache
// may take: one eighth, 8 GB on a 64 GB Mac. The model's weights, the fast
// and embedding models, and on a Mac the user's own programs need the
// rest. On Linux with an NVIDIA card the memory is the card's own, so the
// share is a cautious one there.
const windowMemoryShare = 8

// windower is the set of engine methods that size a model's window:
// what the model can read, and the window to load it with. engine.Engines
// and engine.OllamaEngine both have them.
type windower interface {
	Details(ctx context.Context, model string) (engine.ModelDetails, error)
	SetWindow(model string, n int)
	Window(model string) int
}

// windowFitter returns the function that sizes model's window and sets it
// on eng, for startup and for each switch of the answer model. fallback
// is [ollama] context_length and memGB the memory models load into:
// platform.Machine.ModelMemoryGB, the GPU's own memory or main memory. It returns
// nil when eng has no windower methods, as in tests with a fake.
//
// An endpoint's window is its context_length, which it keeps: there is
// nothing to load on this machine, so nothing to size.
//
// The function asks Ollama about model through Details, which caches the
// answer, works the window out with engine.FitWindow, and logs it. When
// Ollama can't say, or fallback is 0, which leaves the window to Ollama,
// the model keeps fallback. Call it before the model loads: a model that
// loaded with another window loads again on its next call.
func windowFitter(eng engine.Engine, fallback, memGB int, log *slog.Logger) func(ctx context.Context, model string) {
	oe, ok := eng.(windower)
	if !ok || fallback <= 0 {
		return nil
	}
	budget := int64(memGB) << 30 / windowMemoryShare
	return func(ctx context.Context, model string) {
		if _, ok := config.EndpointRef(model); ok {
			return
		}
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
