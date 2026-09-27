// This file answers the model ops. OpModels shows the models: the
// profile, the model for each tier, which ones Ollama holds in memory
// now, the models we tried as the answer model, and the [[models.sets]]
// the user can switch between. OpModelUse switches to a set until merud
// stops, OpModelSave makes the models in use the default in config.toml,
// and OpModelSet, the desktop app's "Use for answers", switches to one of
// the models we tried and saves it in one step.
//
// Every switch takes one path, switchMain: unload the old answer model,
// wait until Ollama lets it go, load the new one, and only then hand it to
// the agent. See ARCHITECTURE.md, "Model tiers".

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
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

// evictWait is how long a switch waits for Ollama to let the old answer
// model go, and evictPoll how often it asks. Ollama frees a model in well
// under a second once asked; ten seconds covers a busy machine. A model
// still there after that means something holds it, such as a question
// another program is running, and loading a second large model on top
// would make a 64 GB Mac swap.
const (
	evictWait = 10 * time.Second
	evictPoll = 100 * time.Millisecond
)

// noToolsWarning is the warning a switch sends when the new answer model
// can't call tools. The router still sends questions to the tool routes,
// and the agent then answers them without tools (internal/agent/notools.go).
const noToolsWarning = "Meru can't use tools with this model; questions that need mail, files or the web will fail."

// fastWarning is the warning a switch sends when the set names a fast
// model other than the one merud runs. The router reads log probabilities
// and needs one letter per route (docs/fast-router.md); not every model
// gives both.
const fastWarning = "This set changes the fast model, which routes each question. The router needs log probabilities " +
	"and one token per route letter (docs/fast-router.md), and not every model gives both; " +
	"run make router-eval before you keep it."

// modelLister is the engine method OpModels needs to say which models
// Ollama has on disk and how big each is. OllamaEngine has it; like
// modelDetailer, it sits outside the Engine interface (ARCHITECTURE.md,
// "Engine layer").
type modelLister interface {
	Pulled(ctx context.Context) ([]engine.PulledModel, error)
}

// modelUnloader is the engine method a switch needs to drop the old
// answer model from Ollama's memory. OllamaEngine has it too.
type modelUnloader interface {
	Unload(ctx context.Context, model string) error
}

// answerModel is the part of the agent the model ops use: which model
// answers now, and a way to change it and whether it thinks. *agent.Agent
// has both methods.
type answerModel interface {
	Main() string
	SetMain(model string, noThink bool)
}

// modelService answers the model ops. models holds the tiers and sets as
// merud started with them: fast and embed stay those until a restart,
// while answer says which model answers now.
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
	// wait and poll are evictWait and evictPoll; tests shorten them.
	wait, poll time.Duration

	// switchMu lets one switch or save run at a time. A switch holds it
	// while Ollama loads a model, which can take a minute; a second switch
	// waits its turn rather than loading a third model beside the first
	// two.
	switchMu sync.Mutex
	// mu guards active, the name of the set in use now ("" for none). It
	// is held only to read or write the name, so OpModels answers while a
	// switch runs.
	mu     sync.Mutex
	active string
}

// newModelService returns the service for the models merud started with.
// The set in use at startup is the first whose models match those; when
// that set turns thinking off, newModelService tells the agent so, as a
// switch to the set would. Every later change goes through switchMain.
func newModelService(cfg config.Config, configPath, outputDir string, eng engine.Engine, answer answerModel,
	edit func(func() error) error, log *slog.Logger) *modelService {
	m := &modelService{
		models: cfg.Models, profile: cfg.Profile, configPath: configPath, outputDir: outputDir,
		eng: eng, answer: answer, edit: edit, log: log, wait: evictWait, poll: evictPoll,
	}
	if s, ok := matchingSet(cfg.Models.Sets, cfg.Models); ok {
		m.active = s.Name
		answer.SetMain(cfg.Models.Main, s.ThinkOff())
	}
	return m
}

// matchingSet returns the first set in sets whose models match running,
// the models in use: the same main model, and the same fast and embed
// models where the set names them. ok is false when none matches.
func matchingSet(sets []config.ModelSet, running config.Models) (config.ModelSet, bool) {
	for _, s := range sets {
		if (s.Main == "" || s.Main == running.Main) && (s.Fast == "" || s.Fast == running.Fast) &&
			(s.Embed == "" || s.Embed == running.Embed) {
			return s, true
		}
	}
	return config.ModelSet{}, false
}

// warnMissingSetModels logs a warning for each model a [[models.sets]]
// entry names that Ollama doesn't list. It doesn't stop merud: the user
// may pull the model later, and OpModelUse refuses a set whose main model
// isn't there yet. A failed list logs nothing, since warm has just shown
// that Ollama answers.
func warnMissingSetModels(ctx context.Context, eng engine.Engine, sets []config.ModelSet, log *slog.Logger) {
	if len(sets) == 0 {
		return
	}
	l, ok := eng.(modelLister)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	pulled, err := l.Pulled(ctx)
	if err != nil {
		log.Debug("model sets: no list of pulled models", "err", err)
		return
	}
	for _, s := range sets {
		for _, model := range []string{s.Main, s.Fast, s.Embed} {
			if model != "" && !hasModel(names(pulled), model) {
				log.Warn("a model set names a model Ollama doesn't have; pull it before you switch to the set",
					"set", s.Name, "model", model, "pull", "ollama pull "+model)
			}
		}
	}
}

// handleModels answers OpModels with one "models" event. A runtime that
// doesn't answer leaves Loaded empty and says why in Err; the reply still
// goes out, since config's part is known.
func (m *modelService) handleModels(ctx context.Context, emit func(rpc.Event) error) error {
	info := m.info(ctx, m.activeSet())
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}

// info gathers what OpModels reports, with active as the set in use.
// info never fails: what Ollama doesn't answer stays empty, and Err says
// why.
func (m *modelService) info(ctx context.Context, active string) rpc.ModelsInfo {
	info := rpc.ModelsInfo{
		Profile: m.profile, Fast: m.models.Fast, Main: m.answer.Main(), Embed: m.models.Embed,
		ConfigPath: m.configPath, OutputDir: m.outputDir, Active: active,
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
			Capabilities: k.Capabilities, Installed: hasModel(names(pulled), k.Name), Tiers: []string{},
			Pull: "ollama pull " + k.Name, Run: "ollama run " + k.Name,
		}
		if c.Installed {
			c.Capabilities = m.capabilities(ictx, k.Name, k.Capabilities)
		}
		if info.Main == k.Name {
			c.Tiers = append(c.Tiers, "main")
		}
		if info.Fast == k.Name {
			c.Tiers = append(c.Tiers, "fast")
		}
		info.Choices = append(info.Choices, c)
	}
	for _, s := range m.models.Sets {
		row := rpc.ModelSet{
			Name: s.Name, Main: s.Main, Fast: s.Fast, Embed: s.Embed, ThinkOff: s.ThinkOff(),
			Active: s.Name == info.Active,
		}
		if s.Main != "" {
			row.Bytes, row.Pulled = sizeOf(pulled, s.Main)
			row.Loaded = hasModel(info.Loaded, s.Main)
		}
		// A set whose main model is one we tried shows what it can do
		// even before it is pulled, as its Library card does.
		var known []string
		if k, ok := config.FindKnownModel(s.Main); ok {
			known = k.Capabilities
		}
		if row.Pulled || known != nil {
			row.Capabilities = m.capabilities(ictx, s.Main, known)
		}
		info.Sets = append(info.Sets, row)
	}
	return info
}

// activeSet returns the name of the set in use now, "" for none.
func (m *modelService) activeSet() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// setActive records name as the set in use; "" for none.
func (m *modelService) setActive(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = name
}

// handleModelUse answers OpModelUse: it switches to the set named req.ID
// until merud stops, then sends the "models" event.
//
// It refuses a name config.toml doesn't have, a set that changes the embed
// model unless req.Rebuild says the user accepts the re-embedding, and a
// main model Ollama doesn't have, naming the `ollama pull` to run. Only
// the main model changes now. A set's fast and embed models wait for
// OpModelSave and a restart, because the router, the summarizer, the
// skill pick and the index each read them at startup; the reply's Warning
// says so. A failed switch returns an error naming the step that failed,
// and the set in use stays as it was.
func (m *modelService) handleModelUse(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	name := strings.TrimSpace(req.ID)
	set, ok := config.FindSet(m.models.Sets, name)
	if !ok {
		return m.noSuchSet(name)
	}
	if set.Embed != "" && set.Embed != m.models.Embed && !req.Rebuild {
		return fmt.Errorf("set %s changes the embed model from %s to %s. A new embedding model makes vectors of "+
			"another size, so every stored vector goes stale, and merud re-embeds every file in your [index] "+
			"folders once the set is saved and merud restarts. To go ahead, switch again with --rebuild",
			set.Name, m.models.Embed, set.Embed)
	}
	main := set.Main
	if main == "" {
		main = m.answer.Main()
	}

	m.switchMu.Lock()
	// defer runs Unlock when the handler returns, on every path.
	defer m.switchMu.Unlock()
	if err := m.switchMain(ctx, main, set.ThinkOff()); err != nil {
		return err
	}
	m.setActive(set.Name)
	m.log.InfoContext(ctx, "model set in use", "set", set.Name, "main", main, "think_off", set.ThinkOff())

	var warnings []string
	if set.Fast != "" && set.Fast != m.models.Fast {
		warnings = append(warnings, fastWarning)
	}
	if later := m.laterTiers(set); later != "" {
		warnings = append(warnings, "merud moves to the "+later+" only after you save the set and restart merud.")
	}
	if !slices.Contains(m.capabilities(ctx, main, nil), engine.ToolUse) {
		warnings = append(warnings, noToolsWarning)
	}
	info := m.info(ctx, set.Name)
	info.Warning = strings.Join(warnings, " ")
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}

// laterTiers names those of set's fast and embed models that differ from
// the ones merud runs, such as "fast model a and the embed model b", and
// so wait for a save and a restart; "" when none do.
func (m *modelService) laterTiers(set config.ModelSet) string {
	var tiers []string
	if set.Fast != "" && set.Fast != m.models.Fast {
		tiers = append(tiers, "fast model "+set.Fast)
	}
	if set.Embed != "" && set.Embed != m.models.Embed {
		tiers = append(tiers, "embed model "+set.Embed)
	}
	return strings.Join(tiers, " and the ")
}

// noSuchSet returns the error for a set name config.toml doesn't have,
// listing the names it does have.
func (m *modelService) noSuchSet(name string) error {
	if len(m.models.Sets) == 0 {
		return fmt.Errorf("config.toml has no model sets; add [[models.sets]] entries to %s and restart merud "+
			"(config.example.toml shows how)", m.configPath)
	}
	var have []string
	for _, s := range m.models.Sets {
		have = append(have, s.Name)
	}
	return fmt.Errorf("no model set is called %q; config.toml has %s", name, strings.Join(have, ", "))
}

// switchMain makes to the answer model, with its thinking off when
// noThink is set. It is the one path every switch takes, in this order:
//
//  0. check that Ollama has to on disk, so a switch that can't finish
//     unloads nothing;
//  1. ask Ollama to unload the answer model now (see engine.Unload);
//  2. wait until /api/ps no longer lists it, at most m.wait;
//  3. load to with a one-token question, as merud warms each tier at
//     startup, so the first real question doesn't pay for the load;
//  4. hand to to the agent.
//
// Steps 1 and 2 are skipped when the old answer model stays in use: it is
// to itself, or it is the fast or embed model, which merud keeps loaded,
// as the lite profile does with one model for fast and main.
//
// It returns an error that names the step that failed, and then the agent
// keeps its answer model. After a failed step 3, Ollama has unloaded the
// old model already and loads it again on the next question. The caller
// holds m.switchMu.
func (m *modelService) switchMain(ctx context.Context, to string, noThink bool) error {
	start := time.Now()
	from := m.answer.Main()
	lctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	pulled, err := m.pulled(lctx)
	cancel()
	if err != nil {
		return fmt.Errorf("switch to %s: ask Ollama which models it has: %w", to, err)
	}
	if !hasModel(names(pulled), to) {
		return fmt.Errorf("switch to %s: Ollama doesn't have it yet; fetch it first with: ollama pull %s", to, to)
	}

	if from != to && from != m.models.Fast && from != m.models.Embed {
		u, ok := m.eng.(modelUnloader)
		if !ok {
			return fmt.Errorf("switch to %s: step 1 of 3, unload %s: the engine can't unload a model", to, from)
		}
		if err := u.Unload(ctx, from); err != nil {
			return fmt.Errorf("switch to %s: step 1 of 3, unload %s: %w; %s stays the answer model", to, from, err, from)
		}
		if err := m.waitUnloaded(ctx, from); err != nil {
			return fmt.Errorf("switch to %s: step 2 of 3: %w; %s stays the answer model, and Ollama loads it "+
				"again on the next question", to, err, from)
		}
	}

	hello := []engine.Message{{Role: engine.RoleUser, Content: "hi"}}
	if _, err := m.eng.Generate(ctx, hello, nil, engine.Options{Model: to, MaxTokens: 1, NoThink: noThink}); err != nil {
		return fmt.Errorf("switch to %s: step 3 of 3, load %s: %w; %s stays the answer model, and Ollama loads "+
			"it again on the next question", to, to, err, from)
	}
	m.answer.SetMain(to, noThink)
	m.log.InfoContext(ctx, "answer model switched", "from", from, "to", to, "think_off", noThink,
		"ms", time.Since(start).Milliseconds())
	return nil
}

// waitUnloaded asks Ollama which models it holds, every m.poll, until
// model isn't among them. It fails when m.wait passes first, when Ollama
// can't say, or when ctx ends.
func (m *modelService) waitUnloaded(ctx context.Context, model string) error {
	deadline := time.Now().Add(m.wait)
	for {
		mi, err := m.eng.Info(ctx)
		if err != nil {
			return fmt.Errorf("ask Ollama whether it let %s go: %w", model, err)
		}
		if !hasModel(mi.LoadedModels, model) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Ollama still holds %s in memory after %s; see ollama ps", model, m.wait)
		}
		// select waits for whichever comes first: the next poll, or the
		// request ending because the client hung up.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.poll):
		}
	}
}

// handleModelSave answers OpModelSave: it writes the models in use now to
// [models] in config.toml, keeping every other line, so merud starts with
// them. That is the answer model, and the fast and embed models of the set
// in use when it names them; think isn't written, since merud finds the
// set again at startup by its models (see newModelService).
func (m *modelService) handleModelSave(ctx context.Context, emit func(rpc.Event) error) error {
	m.switchMu.Lock()
	defer m.switchMu.Unlock()
	active := m.activeSet()
	values := map[string]string{"main": m.answer.Main()}
	if set, ok := config.FindSet(m.models.Sets, active); ok {
		if set.Fast != "" {
			values["fast"] = set.Fast
		}
		if set.Embed != "" {
			values["embed"] = set.Embed
		}
	}
	if err := m.save(values); err != nil {
		return err
	}
	m.log.InfoContext(ctx, "models saved as the default", "set", active, "main", values["main"])
	info := m.info(ctx, active)
	if set, ok := config.FindSet(m.models.Sets, active); ok {
		if later := m.laterTiers(set); later != "" {
			info.Warning = "merud moves to the " + later + " when it restarts."
		}
	}
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}

// save writes values, keys of [models], to config.toml under the config
// lock, and checks that the file came out as asked.
func (m *modelService) save(values map[string]string) error {
	return m.edit(func() error {
		return catalog.SetTableStrings(m.configPath, "models", values, func(next config.Config) error {
			got := map[string]string{"main": next.Models.Main, "fast": next.Models.Fast, "embed": next.Models.Embed}
			for k, v := range values {
				if got[k] != v {
					return fmt.Errorf("the change left [models] %s = %q, want %q; edit %s by hand", k, got[k], v, m.configPath)
				}
			}
			return nil
		})
	})
}

// handleModelSet answers OpModelSet, the desktop app's "Use for answers":
// it makes req.ID the answer model through switchMain, then writes it to
// [models] main. A pick in the Library is a setting, and every setting
// the Library changes lands in config.toml. It refuses a model that isn't
// on the known list, since the Library offers only those, and a model
// Ollama doesn't have, naming the `ollama pull` to run. A model that can't
// call tools is allowed, with noToolsWarning in the reply.
//
// When a set's main model is the one picked, that set becomes the one in
// use, with its think setting; otherwise none is.
func (m *modelService) handleModelSet(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	name := strings.TrimSpace(req.ID)
	k, ok := config.FindKnownModel(name)
	if !ok {
		return fmt.Errorf("%q isn't one of the models the Library offers; to use it, set [models] main in %s and restart merud",
			name, m.configPath)
	}
	running := m.models
	running.Main = name
	set, inSet := matchingSet(m.models.Sets, running)

	m.switchMu.Lock()
	defer m.switchMu.Unlock()
	if err := m.switchMain(ctx, name, inSet && set.ThinkOff()); err != nil {
		return err
	}
	active := ""
	if inSet {
		active = set.Name
	}
	m.setActive(active)
	if err := m.save(map[string]string{"main": name}); err != nil {
		return fmt.Errorf("%s answers until merud stops, but config.toml didn't change: %w", name, err)
	}
	caps := m.capabilities(ctx, name, k.Capabilities)
	m.log.InfoContext(ctx, "answer model changed and saved", "model", name, "tools", slices.Contains(caps, engine.ToolUse))

	info := m.info(ctx, active)
	if !slices.Contains(caps, engine.ToolUse) {
		info.Warning = noToolsWarning
	}
	return emit(rpc.Event{Type: rpc.EventModels, Models: &info})
}

// pulled asks the engine which models Ollama has on disk. It fails when
// the engine can't say, as a test engine without Pulled can't, or when
// Ollama doesn't answer.
func (m *modelService) pulled(ctx context.Context) ([]engine.PulledModel, error) {
	// A type assertion with ", ok" asks whether eng also has Pulled.
	l, ok := m.eng.(modelLister)
	if !ok {
		return nil, errors.New("the engine can't list models")
	}
	return l.Pulled(ctx)
}

// capabilities returns what Ollama says model can do, from the engine's
// cached /api/show answer. When Ollama can't say, it returns fallback,
// what Ollama listed in our tests for a known model, or nil.
func (m *modelService) capabilities(ctx context.Context, model string, fallback []string) []string {
	if d, ok := m.eng.(modelDetailer); ok {
		if details, err := d.Details(ctx, model); err == nil && len(details.Capabilities) > 0 {
			return details.Capabilities
		}
	}
	return fallback
}

// names returns the names of the models in pulled.
func names(pulled []engine.PulledModel) []string {
	out := make([]string, len(pulled))
	for i, p := range pulled {
		out[i] = p.Name
	}
	return out
}

// sizeOf returns the size on disk of model among pulled, and whether
// pulled holds it, with the ":latest" rule of hasModel.
func sizeOf(pulled []engine.PulledModel, model string) (int64, bool) {
	for _, p := range pulled {
		if hasModel([]string{p.Name}, model) {
			return p.Bytes, true
		}
	}
	return 0, false
}

// hasModel reports whether list, model names as Ollama gives them,
// holds name. Ollama adds ":latest" to a name pulled without a tag, so
// "llama3" is there when the list holds "llama3:latest".
func hasModel(list []string, name string) bool {
	if slices.Contains(list, name) {
		return true
	}
	return !strings.Contains(name, ":") && slices.Contains(list, name+":latest")
}
