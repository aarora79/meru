// This file gathers the facts the about_meru tool reports: the profile and
// models, what Ollama says about the main model, the computer, the search
// index, the tool sources, the skills and the memory counts. The tool
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
	eng     engine.Engine           // for Ollama's version and the main model's details
	st      *store.Store            // what the search index holds
	folders func() []string         // the [index] folders now, as the desktop app may change them
	servers func() []rpc.ServerInfo // every tool source, as `meru tools` lists them
	skills  *skillService
	mem     *memory.Store
	machine string // the line that describes the computer
	home    string // the home folder, so paths show as ~/...
	log     *slog.Logger
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
		Fast:    s.cfg.Models.Fast, Main: s.cfg.Models.Main, Embed: s.cfg.Models.Embed,
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
		if details, err := d.Details(octx, s.cfg.Models.Main); err == nil {
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
			})
		case dispatch.KindCommand:
			for _, t := range srv.Tools {
				a.Commands = append(a.Commands, t.Name)
			}
		}
	}

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
