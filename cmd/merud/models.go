// This file answers the two model ops the desktop app's Library sends.
// OpModels shows the models: the profile, the model for each tier, which
// ones Ollama holds in memory now, and the models we tried as the answer
// model, each with whether Ollama has it. OpModelSet makes one of those
// the answer model: merud writes [models] main and hands the name to the
// agent, so the next question uses it without a restart.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// modelsTimeout bounds the calls that ask Ollama what it holds and what
// each model can do. Ollama answers from memory; a slow answer means it
// is busy or gone, and the rest of the reply shouldn't wait on it.
const modelsTimeout = 3 * time.Second

// noToolsWarning is the warning OpModelSet sends when the new answer
// model can't call tools. The router still sends questions to the tool
// routes, and Ollama refuses a tool list for such a model, so those turns
// fail.
const noToolsWarning = "Meru can't use tools with this model; questions that need mail, files or the web will fail."

// modelLister is the engine method OpModels needs to say which models
// Ollama has on disk. OllamaEngine has it; like modelDetailer, it sits
// outside the Engine interface (ARCHITECTURE.md, "Engine layer").
type modelLister interface {
	Pulled(ctx context.Context) ([]string, error)
}

// answerModel is the part of the agent the model ops use: which model
// answers now, and a way to change it. *agent.Agent has both methods.
type answerModel interface {
	Main() string
	SetMain(model string)
}

// modelService answers OpModels and OpModelSet. models holds the tiers as
// merud started with them; answer says which model answers now, since
// OpModelSet can change it.
type modelService struct {
	models     config.Models
	profile    string
	configPath string
	outputDir  string
	eng        engine.Engine
	answer     answerModel
	// edit runs a change to config.toml under the one lock every writer
	// in merud shares; see builtin.Tools.EditConfig.
	edit func(func() error) error
	log  *slog.Logger
}

// handleModels answers OpModels with one "models" event. A runtime that
// doesn't answer leaves Loaded empty and says why in Err; the reply still
// goes out, since config's part is known.
func (m modelService) handleModels(ctx context.Context, emit func(rpc.Event) error) error {
	info := m.info(ctx)
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}

// info gathers what OpModels reports. It never fails: what Ollama doesn't
// answer stays empty, and Err says why.
func (m modelService) info(ctx context.Context) rpc.ModelsInfo {
	info := rpc.ModelsInfo{
		Profile: m.profile, Fast: m.models.Fast, Main: m.answer.Main(), Embed: m.models.Embed,
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
	// A failed list leaves every model marked not installed, which is
	// what the user can act on: the pull command shows.
	pulled, err := m.pulled(ictx)
	if err != nil {
		m.log.DebugContext(ctx, "models: no list of pulled models", "err", err)
	}
	for _, k := range config.KnownModels() {
		c := rpc.ModelChoice{
			Name: k.Name, Label: k.Label, Size: k.Size, Good: k.Good, Bad: k.Bad,
			Capabilities: k.Capabilities, Installed: hasModel(pulled, k.Name), Tiers: []string{},
			Pull: "ollama pull " + k.Name, Run: "ollama run " + k.Name,
		}
		if c.Installed {
			c.Capabilities = m.capabilities(ictx, k)
		}
		if info.Main == k.Name {
			c.Tiers = append(c.Tiers, "main")
		}
		if info.Fast == k.Name {
			c.Tiers = append(c.Tiers, "fast")
		}
		info.Choices = append(info.Choices, c)
	}
	return info
}

// handleModelSet answers OpModelSet: it makes req.ID the answer model. It
// refuses a model that isn't on the known list, since the Library offers
// only those, and a model Ollama doesn't have, since every answer would
// then fail; that error names the `ollama pull` to run. A model that
// can't call tools is allowed, with noToolsWarning in the reply.
//
// It writes [models] main under the config lock, keeping every other line
// of config.toml, then hands the name to the agent. The fast and embed
// models stay as they are. Ollama loads the new model on the first
// question, and keeps the old one loaded until it unloads it or stops.
func (m modelService) handleModelSet(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	name := strings.TrimSpace(req.ID)
	k, ok := config.FindKnownModel(name)
	if !ok {
		return fmt.Errorf("%q isn't one of the models the Library offers; to use it, set [models] main in %s and restart merud",
			name, m.configPath)
	}
	ictx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	pulled, err := m.pulled(ictx)
	if err != nil {
		return fmt.Errorf("ask Ollama which models it has: %w", err)
	}
	if !hasModel(pulled, name) {
		return fmt.Errorf("%s isn't in Ollama yet; fetch it first with: ollama pull %s", name, name)
	}
	caps := m.capabilities(ictx, k)

	err = m.edit(func() error {
		return catalog.SetTableString(m.configPath, "models", "main", name, func(next config.Config) error {
			if next.Models.Main != name {
				return fmt.Errorf("the change left [models] main = %q, want %q; edit %s by hand", next.Models.Main, name, m.configPath)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	m.answer.SetMain(name)
	m.log.InfoContext(ctx, "answer model changed", "model", name, "tools", slices.Contains(caps, "tools"))

	info := m.info(ctx)
	if !slices.Contains(caps, "tools") {
		info.Warning = noToolsWarning
	}
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}

// pulled asks the engine which models Ollama has on disk. It fails when
// the engine can't say, as a test engine without Pulled can't, or when
// Ollama doesn't answer.
func (m modelService) pulled(ctx context.Context) ([]string, error) {
	// A type assertion with ", ok" asks whether eng also has Pulled.
	l, ok := m.eng.(modelLister)
	if !ok {
		return nil, errors.New("the engine can't list models")
	}
	return l.Pulled(ctx)
}

// capabilities returns what Ollama says the known model k can do, from
// the engine's cached /api/show answer. When Ollama can't say, it returns
// what Ollama listed in our tests.
func (m modelService) capabilities(ctx context.Context, k config.KnownModel) []string {
	if d, ok := m.eng.(modelDetailer); ok {
		if details, err := d.Details(ctx, k.Name); err == nil && len(details.Capabilities) > 0 {
			return details.Capabilities
		}
	}
	return k.Capabilities
}

// hasModel reports whether pulled, the names Ollama lists, holds name.
// Ollama adds ":latest" to a name pulled without a tag, so "llama3" is
// there when the list holds "llama3:latest".
func hasModel(pulled []string, name string) bool {
	if slices.Contains(pulled, name) {
		return true
	}
	return !strings.Contains(name, ":") && slices.Contains(pulled, name+":latest")
}
