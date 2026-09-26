// This file answers OpModels, which the desktop app's Library sends to
// show the models: the profile, the model for each tier, and which ones
// Ollama holds in memory now.

package main

import (
	"context"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// modelsTimeout bounds the call that asks Ollama what it holds. Ollama
// answers from memory; a slow answer means it is busy or gone, and the
// rest of the reply shouldn't wait on it.
const modelsTimeout = 3 * time.Second

// modelService answers OpModels from config as merud started with it and
// from the engine.
type modelService struct {
	models     config.Models
	profile    string
	configPath string
	outputDir  string
	eng        engine.Engine
}

// handleModels answers OpModels with one "models" event. A runtime that
// doesn't answer leaves Loaded empty and says why in Err; the reply still
// goes out, since config's part is known.
func (m modelService) handleModels(ctx context.Context, emit func(rpc.Event) error) error {
	info := rpc.ModelsInfo{
		Profile: m.profile, Fast: m.models.Fast, Main: m.models.Main, Embed: m.models.Embed,
		ConfigPath: m.configPath, OutputDir: m.outputDir,
	}
	ictx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	mi, err := m.eng.Info(ictx)
	if err != nil {
		info.Err = err.Error()
	} else {
		info.Runtime, info.RuntimeVersion, info.Loaded = mi.Runtime, mi.RuntimeVersion, mi.LoadedModels
	}
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}
