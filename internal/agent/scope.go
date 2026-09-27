// This file holds the turn that runs with a scope the user picked, such as
// "My files" or "Just talk" in the desktop app's composer, instead of the
// route the router picks, and the turn whose question carries images,
// which skips the router too. ARCHITECTURE.md, "Desktop app", says why a
// client may choose where Meru looks.

package agent

import (
	"context"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// scopeRoute is the route each scope takes. The route names what the
// transcript, the usage table and the clients already know, so a scoped
// turn reads like any other afterwards.
func scopeRoute(scope string) string {
	switch scope {
	case rpc.ScopeFiles:
		return "search"
	case rpc.ScopeMail, rpc.ScopeWeb:
		return "tools"
	}
	return "direct" // rpc.ScopeTalk, and rpc.ScopeAuto for a turn with images
}

// respondScoped is respond for a turn whose scope isn't auto. The user
// has said where to look, so the router doesn't run and none of the rules
// that widen a route apply: a scope is a promise about what the turn may
// touch, and "Just talk" must never reach a tool. Skills are still picked,
// since a skill such as writing shapes the answer without a tool, but a
// skill never adds tools here.
//
// A "files" turn searches first, as a search route does. The other scopes
// search nothing.
//
// A question with images comes here in auto too, since the router's fast
// model can't see them. It takes the "direct" route and the tools that
// route offers; see scopeSpecs.
func (a *Agent) respondScoped(ctx context.Context, t *turn, question string, history []engine.Message) (response, error) {
	var res response
	var picked pickedSkills
	if a.skills != nil {
		picked.reg = a.skills.Registry(ctx)
		picked.names = a.pickSkills(ctx, picked.reg, question)
	}
	// skillTools drops a picked skill whose tools config turns off; the
	// tools it names are ignored, because the scope sets the tools.
	picked.names, _ = a.skillTools(ctx, picked)
	res.route = scopeRoute(t.scope)
	// Confidence 1: nothing guessed this route. Fallback stays false.
	ev := rpc.Event{Type: rpc.EventRoute, Route: res.route, Confidence: 1, Skills: skillInfos(picked.names)}
	if err := t.emit(ev); err != nil {
		return res, err
	}

	specs := a.scopeSpecs(ctx, t.scope)
	fileTurn := t.scope == rpc.ScopeFiles
	var files string
	if a.searchesFirst(fileTurn) {
		var sources []rpc.Citation
		var err error
		files, sources, res.docs, err = a.searchFiles(ctx, searchQuery(question, history), false)
		if err != nil {
			return res, err
		}
		if len(sources) > 0 {
			if err := t.emit(rpc.Event{Type: rpc.EventSources, Sources: sources}); err != nil {
				return res, err
			}
		}
		t.sources, t.cites = sources, len(sources)
		files = joinSections(files, a.earlierSection(ctx, searchQuery(question, history), t.sess.ID()))
	}
	a.log.DebugContext(ctx, "route set by the scope", "scope", t.scope, "route", res.route, "tools", len(specs))
	var err error
	res.rep, err = a.finishPrompt(ctx, t, question, history, picked, files, specs, fileTurn)
	return res, err
}

// scopeSpecs returns the tools a turn with scope may use:
//
//   - files: what the search route offers, the file tools, the local
//     commands that don't ask, and datetime and about_meru;
//   - mail: every tool of the mail and calendar servers (see
//     mailServers), with datetime, since "what's on Friday?" needs the
//     date, and about_meru;
//   - web: web_search and web_fetch, with datetime and about_meru;
//   - talk: none at all;
//   - auto, which reaches here only for a question with images: what
//     the direct route offers, datetime and about_meru.
//
// builtin.EveryRoute names the two tools every scope but talk carries.
//
// A mail turn refreshes the tool servers first, as a tools route does, so
// a server started after merud is there for it.
func (a *Agent) scopeSpecs(ctx context.Context, scope string) []engine.ToolSpec {
	if a.tools == nil {
		return nil
	}
	switch scope {
	case rpc.ScopeFiles:
		return a.toolSpecs("search")
	case rpc.ScopeMail:
		a.tools.Refresh(ctx)
		all := a.tools.Tools()
		servers := mailServers(all)
		return slices.DeleteFunc(slices.Clone(all), func(s engine.ToolSpec) bool {
			return !builtin.EveryRoute(s.Name) && !slices.Contains(servers, serverOf(s.Name))
		})
	case rpc.ScopeWeb:
		return slices.DeleteFunc(slices.Clone(a.tools.Tools()), func(s engine.ToolSpec) bool {
			return !builtin.EveryRoute(s.Name) && s.Name != builtin.WebSearch && s.Name != builtin.WebFetch
		})
	case rpc.ScopeAuto:
		return a.toolSpecs("direct")
	}
	return nil
}

// mailServers returns the MCP servers and A2A agents behind specs that
// handle mail or a calendar: those with a tool whose name holds a noun
// ending in "mail" (gmail, email) or the noun "calendar" or "event". It
// reads the same nouns the router's prompt lists (see toolNouns), so
// Meru needs no list of mail providers, and a server's other tools come
// with it: the google server brings Drive and Docs along with Gmail.
//
// A server whose tool names say nothing of mail, such as obsidian, stays
// out, even though it is connected. The scope promises mail and calendar,
// not everything the user connected.
func mailServers(specs []engine.ToolSpec) []string {
	var out []string
	for _, s := range specs {
		server := serverOf(s.Name)
		if server == "" || slices.Contains(out, server) {
			continue
		}
		for _, n := range toolNouns([]engine.ToolSpec{s}) {
			if strings.HasSuffix(n, "mail") || n == "calendar" || n == "event" {
				out = append(out, server)
				break
			}
		}
	}
	return out
}

// serverOf returns the MCP server or A2A agent a full tool name belongs
// to, such as "google" for "google.search_gmail_messages" and "travel" for
// "a2a.travel.book", or "" for a built-in tool or a local command.
func serverOf(name string) string {
	switch toolKind(name) {
	case dispatch.KindMCP:
		s, _, _ := strings.Cut(name, ".")
		return s
	case dispatch.KindA2A:
		s, _, _ := strings.Cut(strings.TrimPrefix(name, "a2a."), ".")
		return s
	}
	return ""
}
