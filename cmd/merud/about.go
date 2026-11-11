// This file gathers the facts the about_meru tool reports: the profile and
// models, what Ollama says about the main model, the computer, the search
// index, the tool sources, web search's state, the skills and the memory
// counts. The tool
// itself, and the text it writes, live in internal/builtin/about.go.

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// aboutTimeout bounds the two questions about_meru asks Ollama. Both
// answer from memory, and the engine keeps the /api/show answer, so a
// slow reply means Ollama is busy or gone; the tool then leaves those
// facts out rather than hold up the turn.
const aboutTimeout = 3 * time.Second

// modelDetailer is the one engine method about_meru needs beyond Engine.
// OllamaEngine has it. It sits outside the Engine interface, which keeps
// that interface at four methods (ARCHITECTURE.md, "Engine layer"); a test
// engine without it just leaves the details out.
type modelDetailer interface {
	Details(ctx context.Context, model string) (engine.ModelDetails, error)
}

// aboutService holds what about_meru reads. Every source is one merud
// already keeps, so the tool reports the setup as merud runs it.
type aboutService struct {
	cfg     config.Config           // profile and models, as merud started with them
	main    func() string           // the answer model now, which Settings can change
	eng     engine.Engine           // for Ollama's version and the main model's details
	st      *store.Store            // what the search index holds
	folders func() []string         // the [index] folders now, as the desktop app may change them
	servers func() []rpc.ServerInfo // every tool source, as `meru tools` lists them
	skills  *skillService
	mem     *memory.Store
	machine string // the line that describes the computer
	home    string // the home folder, so paths show as ~/...
	// web says whether web search works now, with the SearXNG
	// connector's sentence; nil leaves the line out.
	web func() (bool, string)
	// configPath is config.toml, read on each call for [usage_report]
	// send, which the clients can change while merud runs.
	configPath string
	log        *slog.Logger
}

// facts gathers the facts about_meru reports. It never fails: a fact it
// can't read stays out, and the reason goes to the log at debug level.
//
// It copies names, counts and paths only. It reads the servers from the
// same list `meru tools` shows, which holds no env or header values, and
// skips each server's last error, which can quote a URL, and each
// memory's text.
func (s aboutService) facts(ctx context.Context) builtin.About {
	a := builtin.About{
		Version: obs.BuildVersion(),
		Profile: s.cfg.Profile,
		Fast:    s.cfg.Models.Fast, Main: s.main(), Embed: s.cfg.Models.Embed,
		Machine: s.machine,
	}

	octx, cancel := context.WithTimeout(ctx, aboutTimeout)
	// defer runs cancel when facts returns, which frees the timer.
	defer cancel()
	if info, err := s.eng.Info(octx); err == nil {
		a.RuntimeVersion = info.RuntimeVersion
	} else {
		s.log.DebugContext(ctx, "about_meru: no Ollama version", "err", err)
	}
	// A type assertion with ", ok" asks whether eng also has Details.
	if d, ok := s.eng.(modelDetailer); ok {
		if details, err := d.Details(octx, a.Main); err == nil {
			a.MainDetails = details
		} else {
			s.log.DebugContext(ctx, "about_meru: no details for the main model", "err", err)
		}
	}

	for _, f := range s.folders() {
		a.Folders = append(a.Folders, rpc.ShortPath(s.home, f))
	}
	if stats, err := s.st.Stats(ctx); err == nil {
		a.Files, a.Chunks = stats.Documents, stats.Chunks
	} else {
		s.log.DebugContext(ctx, "about_meru: no index counts", "err", err)
	}
	if n, err := s.st.DiskBytes(); err == nil {
		a.DBBytes = n
	}

	for _, srv := range s.servers() {
		switch srv.Kind {
		case dispatch.KindMCP, dispatch.KindA2A:
			a.Sources = append(a.Sources, builtin.AboutSource{
				Name: srv.Name, Kind: srv.Kind, Connected: srv.Connected, Tools: len(srv.Tools),
				Sentence: srv.Sentence,
			})
		case dispatch.KindCommand:
			for _, t := range srv.Tools {
				a.Commands = append(a.Commands, t.Name)
			}
		}
	}

	if s.web != nil {
		_, a.Web = s.web()
	}
	a.UsageReport = usageReportSentence(s.configPath)

	a.Skills, a.Disabled = s.skills.names(ctx)

	// List reads every memory file; only the kind of each is kept. A
	// folder it can't read in part still gives the counts it could read.
	mems, err := s.mem.List()
	if err != nil {
		s.log.DebugContext(ctx, "about_meru: memory folder", "err", err)
	}
	a.Memories = map[string]int{}
	for _, m := range mems {
		a.Memories[m.Kind]++
	}
	return a
}

// usageReportSentence says, for about_meru, whether merud sends the
// anonymous daily usage report, so the model answers "does Meru send
// anything about me?" from the setting and not from its training. It reads
// config.toml at path; "" when the file doesn't load.
func usageReportSentence(path string) string {
	cfg, err := config.Load(path)
	if err != nil {
		return ""
	}
	const what = "once a day Meru sends " + rpc.UsageReportFields + " "
	const change = "The Usage screen, /report in meru chat and `meru report off` turn it off."
	switch cfg.UsageReport.Send {
	case config.ReportOn:
		return "on: " + what + change
	case config.ReportOff:
		return "off. Meru sends nothing about itself. `meru report on` turns it on."
	}
	return "off until the user chooses; meru chat and the app ask. If they say yes, " + what + change
}
