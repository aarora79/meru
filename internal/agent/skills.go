// This file holds the skills part of a turn (ARCHITECTURE.md, "Skills"):
// one short call to the fast model that picks the skills a question needs,
// run side by side with the router, and the two prompt sections that
// follow from it. Every turn lists each skill's name and description; only
// the picked skills' instructions join the prompt. That split is called
// progressive disclosure. It also holds skillTools, which lets a picked
// skill bring the tools its steps use.

package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/skills"
)

// Headers for the two skills sections of the system prompt. The list's
// header says a skill isn't a tool: in a real session the model called
// "web-research", a skill's name, as a tool, twice, and apologised when
// both calls failed.
const (
	skillsListHeader = "Skills you can use. A skill is a set of instructions, not a tool, so never call a skill by name:"
	skillsBodyHeader = "Follow these instructions for this answer:"
)

// maxPicked is how many skills one turn loads at most. Two covers "write
// an explainer, in plain English" (explainer and writing); more would crowd
// a small model's context.
const maxPicked = 2

// pickMaxTokens caps the pick call's answer. Two skill names and a comma
// fit in well under 20 tokens; the cap stops a model that starts to chat.
const pickMaxTokens = 20

// cutNote ends a skill's instructions that didn't fit under maxSkillChars.
const cutNote = "[Meru cut the rest of this skill to keep the prompt short.]"

// Skills hands the agent the skill registry for a turn. merud passes a
// service that loads the registry again when the skills folder changes;
// tests pass a fixed registry. A nil registry means no skills.
type Skills interface {
	Registry(ctx context.Context) *skills.Registry
}

// UseSkills makes every later turn list the skills in s and load the ones
// a turn picks. Call it once, before the first Handle: Handle reads the
// field without a lock. Without it, turns use no skills.
//
// It is a method rather than a parameter of New so that the many tests
// that build an Agent without skills don't change.
func (a *Agent) UseSkills(s Skills) {
	a.skills = s
}

// UseMachine sets the line that describes the user's computer, which the
// system prompt carries after today's date. merud builds it once at
// startup (cmd/merud/machine.go); "" leaves it out.
func (a *Agent) UseMachine(line string) {
	a.machine = line
}

// UseFolders makes every later turn read the [index] folders from folders,
// so a folder the desktop app adds or removes shows in the next prompt's
// note on the user's files, and in the rule for a question that names a
// folder. Call it once, before the first Handle. Without it, turns use the
// folders config held when New ran.
func (a *Agent) UseFolders(folders func() []string) {
	a.folders = folders
}

// currentFolderNames returns folderNames for the folders as they are now.
func (a *Agent) currentFolderNames() []string {
	if a.folders == nil {
		return a.folderNames
	}
	return folderNames(a.folders())
}

// currentFilesNote returns filesNote for the folders as they are now. The
// text changes only when the folders do, so Ollama can still reuse its
// work on the prompt's opening from one turn to the next.
func (a *Agent) currentFilesNote() string {
	if a.folders == nil {
		return a.filesNote
	}
	return filesNote(a.folders(), a.agentic)
}

// pickedSkills is what a turn knows about skills: the registry it read,
// and the names the pick call chose from it, in the model's order.
type pickedSkills struct {
	reg   *skills.Registry // nil when the agent has no skills
	names []string
}

// routeAndPick asks the router for the route and, at the same time, asks
// the fast model which skills the question needs. The two calls don't
// depend on each other, so running them side by side saves the time of the
// shorter one. An errgroup runs each function in its own goroutine, and
// Wait returns the first error; gctx is cancelled when either fails.
//
// Only the route can fail the turn. A failed pick logs a warning and the
// turn goes on with no skills. The picked names go on the turn's span as
// meru.skills; they come from the registry, so the set stays bounded.
func (a *Agent) routeAndPick(ctx context.Context, question string, history []engine.Message) (Decision, pickedSkills, error) {
	var picked pickedSkills
	if a.skills != nil {
		picked.reg = a.skills.Registry(ctx)
	}
	var dec Decision
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		dec, err = a.route(gctx, question, history)
		return err
	})
	g.Go(func() error {
		picked.names = a.pickSkills(gctx, picked.reg, question)
		return nil
	})
	if err := g.Wait(); err != nil {
		return Decision{}, pickedSkills{}, err
	}
	if picked.reg != nil && len(picked.reg.List()) > 0 {
		// SpanFromContext finds the meru.turn span that Handle put on ctx.
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("meru.skills", strings.Join(picked.names, ",")))
	}
	return dec, picked, nil
}

// pickSkills asks the fast model which of reg's skills question needs, and
// returns their names, at most maxPicked. It makes no call and returns nil
// when reg is nil or empty. The call runs with thinking off: a thinking
// model would spend its few tokens on hidden reasoning and name nothing.
//
// It records a meru.skills.pick span with a gen_ai.chat span under it, and
// a debug line with the names and the time taken. A failed call returns
// nil after a warning, unless ctx ended, which the route call reports.
func (a *Agent) pickSkills(ctx context.Context, reg *skills.Registry, question string) []string {
	if reg == nil {
		return nil
	}
	list := reg.List()
	if len(list) == 0 {
		return nil
	}
	ctx, span := obs.Tracer().Start(ctx, "meru.skills.pick")
	defer span.End()
	start := time.Now()

	model := a.models.Fast
	cctx, chat := obs.StartChat(ctx, obs.Chat{Tier: "fast", Model: model, MaxTokens: pickMaxTokens})
	zero := 0.0
	comp, err := a.engine.Generate(cctx, pickMessages(list, question), nil, engine.Options{
		Model: model, MaxTokens: pickMaxTokens, Temperature: &zero, NoThink: true,
	})
	if err != nil {
		obs.EndSpanErr(cctx, chat, err)
		chat.End()
		obs.EndSpanErr(ctx, span, err)
		if ctx.Err() == nil {
			a.log.WarnContext(ctx, "skill pick failed; answering without skills", "err", err)
		}
		return nil
	}
	u := comp.Usage
	obs.ChatResult(chat, obs.Usage{
		PromptTokens: u.PromptTokens, OutputTokens: u.OutputTokens,
		LoadDuration: u.LoadDuration, PromptEvalDuration: u.PromptEvalDuration,
		EvalDuration: u.EvalDuration,
	}, comp.DoneReason)
	chat.End()

	names := parsePick(comp.Text, reg)
	span.SetAttributes(attribute.String("meru.skills", strings.Join(names, ",")))
	a.log.DebugContext(ctx, "skills picked", "skills", strings.Join(names, ","),
		"offered", len(list), "model", model, "ms", time.Since(start).Milliseconds())
	return names
}

// pickMessages builds the pick prompt: a system message that lists each
// skill as "- name: description" and says how to answer, then the question
// as the user's message. The answer it asks for is names and commas, or
// "none", which parsePick reads without any JSON.
//
// The fixed part comes first and doesn't change between turns while the
// skills stay the same, so Ollama can reuse its work on it.
func pickMessages(list []skills.Summary, question string) []engine.Message {
	var b strings.Builder
	b.WriteString("You choose which skills help answer the user's message. " +
		"A skill is a set of instructions for one kind of task.\n\nSkills:\n")
	for _, s := range list {
		// strings.Fields and Join fold a description written over several
		// lines onto one, so the list keeps one line per skill.
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, strings.Join(strings.Fields(s.Description), " "))
	}
	fmt.Fprintf(&b, "\nReply with the names of the skills this message needs, at most %d, separated by commas. "+
		"Reply \"none\" when no skill fits, as for a plain question, a lookup or small talk. "+
		"Reply with names only, no other words.", maxPicked)
	return []engine.Message{
		{Role: engine.RoleSystem, Content: b.String()},
		{Role: engine.RoleUser, Content: question},
	}
}

// parsePick reads the skill names out of the pick call's answer. It splits
// the text at anything that can't be part of a name, keeps the words that
// name a skill in reg, drops repeats, and stops at maxPicked. "none", or an
// answer that names no skill, gives nil.
func parsePick(text string, reg *skills.Registry) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
	})
	var names []string
	for _, w := range words {
		if len(names) == maxPicked {
			break
		}
		w = strings.Trim(w, "-")
		if reg.Has(w) && !slices.Contains(names, w) {
			names = append(names, w)
		}
	}
	return names
}

// skillsSection builds the two skills parts of the system prompt: the list
// of every skill's name and description, which stays the same from turn to
// turn, and the picked skills' instructions, which change with the
// question. They go in different places (see budget.go). Both are "" when
// there are no skills. It records their size as meru.context.tokens with
// section "skills".
//
// Both parts leave out a skill that names tools when the turn offers none
// of them: specs, the tools the turn offers, decide (see skillFits). A
// real turn showed why. The list named web-research on a direct turn that
// offered no web tool; the model called a tool named "web-research",
// dispatch refused it, and the model told the user it couldn't search.
// The list is then the same on every turn that offers the same tools,
// which in auto scope is nearly every turn, since every route offers the
// web tools while web_search is on.
//
// A picked skill whose file can't be read now, say because it was deleted
// a moment ago, is left out with a warning.
func (a *Agent) skillsSection(ctx context.Context, p pickedSkills, specs []engine.ToolSpec) (list, bodies string) {
	if p.reg == nil {
		return "", ""
	}
	offered := make([]string, len(specs))
	for i, s := range specs {
		offered[i] = s.Name
	}
	list = skillList(p.reg, offered)

	var bodyList []namedBody
	for _, name := range p.names {
		if !skillFits(p.reg, name, offered) {
			a.log.DebugContext(ctx, "picked skill left out: the turn offers none of its tools", "skill", name)
			continue
		}
		body, err := p.reg.Body(name)
		if err != nil {
			a.log.WarnContext(ctx, "skill left out of the prompt", "skill", name, "err", err)
			continue
		}
		bodyList = append(bodyList, namedBody{name: name, body: body})
	}
	if text, cut := formatBodies(bodyList, maxSkillChars); text != "" {
		bodies = text
		if cut {
			a.log.DebugContext(ctx, "skills over their cap; cut the last one", "cap_chars", maxSkillChars)
		}
	}
	obs.RecordContextTokens(ctx, "skills", (utf8.RuneCountInString(list)+utf8.RuneCountInString(bodies))/4)
	return list, bodies
}

// skillList returns the list of skills for a turn that offers the tools
// named in offered: skillsListHeader, then one line per skill that fits
// the turn (see skillFits), each with its name, its description on one
// line and the offered tools it uses. It returns "" when no skill fits.
func skillList(reg *skills.Registry, offered []string) string {
	var lines []string
	for _, s := range reg.List() {
		if !skillFits(reg, s.Name, offered) {
			continue
		}
		line := "- " + s.Name + ": " + strings.Join(strings.Fields(s.Description), " ")
		if tools := skillToolNames(reg, s.Name, offered); len(tools) > 0 {
			line += " To use it, call " + orList(tools) + "."
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return skillsListHeader + "\n" + strings.Join(lines, "\n")
}

// namedBody is one picked skill's name and instructions.
type namedBody struct {
	name, body string
}

// formatBodies writes the picked skills' instructions under
// skillsBodyHeader, each opened by a "Skill: <name>" line. The first skill
// goes in whole. Each later one gets what is left of limit characters; one
// that doesn't fit is cut at the last line break that fits and ends with
// cutNote. cut reports whether anything was cut. It returns "" for no
// bodies.
func formatBodies(bodies []namedBody, limit int) (text string, cut bool) {
	if len(bodies) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString(skillsBodyHeader)
	used := 0
	for i, nb := range bodies {
		fmt.Fprintf(&b, "\n\nSkill: %s\n\n", nb.name)
		body := nb.body
		n := utf8.RuneCountInString(body)
		if i > 0 && used+n > limit {
			body = cutText(body, max(limit-used, 0))
			if body != "" {
				body += "\n\n"
			}
			body += cutNote
			cut = true
		}
		b.WriteString(body)
		used += n
	}
	return b.String(), cut
}

// cutText returns the start of s, at most n characters long, ending at the
// last line break inside those n characters when there is one, so a cut
// skill doesn't end halfway through a sentence.
func cutText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	head := string(r)
	if i := strings.LastIndex(head, "\n"); i > 0 {
		head = head[:i]
	}
	return strings.TrimRight(head, " \n")
}

// skillTools settles what the picked skills in p need from the tools. A
// skill's allowed-tools key (see skills.Skill) names the tools its steps
// use; web-research names web_search and web_fetch. It returns the picked
// names that stay, in order, and the tools they name that the ToolRunner
// offers, each once.
//
// A skill brings only tools that config already allows: "offers" means the
// ToolRunner lists the tool, and dispatch lists only the built-in tools in
// [builtin] tools (web_search only with a SearXNG address) and the MCP and
// A2A tools on an allowlist. So a skill can't turn on a tool config keeps
// off (AGENTS.md, non-negotiable 3), and every call still goes through
// dispatch. When a named tool is missing and would come from an MCP server
// or A2A agent, it gives each server that isn't connected one try first,
// as a tools route does.
//
// A skill whose named tools are all missing leaves the turn: its steps say
// to use a tool the model won't have. A small model told "search first
// with web_search" and offered only datetime called datetime eight times
// in a row. Names Meru can't recognise as a tool (no dot, and no built-in
// by that name), such as a Claude skill's "Read", count for nothing: a
// skill that names only those stays, as if it named none.
func (a *Agent) skillTools(ctx context.Context, p pickedSkills) (keep, tools []string) {
	if p.reg == nil || len(p.names) == 0 {
		return p.names, nil
	}
	offered := a.offeredNames()
	connected := false
	for _, name := range p.names {
		s, ok := p.reg.Get(name)
		if !ok {
			keep = append(keep, name)
			continue
		}
		named := slices.DeleteFunc(s.AllowedTools, func(n string) bool { return !toolShaped(n) })
		if len(named) == 0 {
			keep = append(keep, name)
			continue
		}
		// A server or agent that isn't connected lists no tools; refresh
		// the servers, which gives it one try, before counting its tools
		// as off.
		missingRemote := slices.ContainsFunc(named, func(n string) bool {
			return !slices.Contains(offered, n) && toolKind(n) != dispatch.KindBuiltin
		})
		if missingRemote && !connected && a.tools != nil {
			a.tools.Refresh(ctx)
			offered = a.offeredNames()
			connected = true
		}
		var have []string
		for _, n := range named {
			if slices.Contains(offered, n) {
				have = append(have, n)
			}
		}
		if len(have) == 0 {
			a.log.DebugContext(ctx, "skill left out: config allows none of the tools it uses",
				"skill", name, "tools", strings.Join(named, ","))
			continue
		}
		keep = append(keep, name)
		for _, n := range have {
			if !slices.Contains(tools, n) {
				tools = append(tools, n)
			}
		}
	}
	return keep, tools
}

// offeredNames returns the names of the tools the ToolRunner offers, or nil
// when tools are off.
func (a *Agent) offeredNames() []string {
	if a.tools == nil {
		return nil
	}
	var names []string
	for _, s := range a.tools.Tools() {
		names = append(names, s.Name)
	}
	return names
}

// toolShaped reports whether name can name a Meru tool: a built-in tool
// such as "web_search", or a name with a dot, such as
// "obsidian.obsidian_simple_search", "a2a.research.summarize" or
// "cmd.git-log".
func toolShaped(name string) bool {
	return strings.Contains(name, ".") || slices.Contains(config.BuiltinTools(), name)
}

// notOffered returns the names in want that specs don't hold, in order.
func notOffered(want []string, specs []engine.ToolSpec) []string {
	var out []string
	for _, n := range want {
		if !slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return s.Name == n }) {
			out = append(out, n)
		}
	}
	return out
}

// skillToolNames returns the tools the skill called name uses, from its
// allowed-tools key, that the list offered holds, in the skill's order. It
// returns nil for a skill that names none, or none of them is offered.
//
// The skills list passes the tools the turn offers, and skillHint the
// round's tools, then every tool config allows.
func skillToolNames(reg *skills.Registry, name string, offered []string) []string {
	if reg == nil {
		return nil
	}
	s, ok := reg.Get(name)
	if !ok {
		return nil
	}
	var out []string
	for _, n := range s.AllowedTools {
		if slices.Contains(offered, n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// skillFits reports whether the skill called name belongs in a turn that
// offers the tools named in offered: it does when its allowed-tools key
// names no tool Meru knows (see toolShaped), or when offered holds at
// least one of the tools it names. A skill whose tools are all missing
// would tell the model to call a tool it doesn't have.
func skillFits(reg *skills.Registry, name string, offered []string) bool {
	s, ok := reg.Get(name)
	if !ok {
		return false
	}
	named := slices.DeleteFunc(slices.Clone(s.AllowedTools), func(n string) bool { return !toolShaped(n) })
	return len(named) == 0 || len(skillToolNames(reg, name, offered)) > 0
}

// skillHint returns what the model reads back when it calls a tool named
// after a skill. When offer, the round's tools, holds a tool the skill
// uses, the hint names it: "web-research is a skill, not a tool. Call
// web_search or web_fetch." When config allows the skill's tools but the
// round doesn't offer them, as on the desktop app's "Just talk" scope,
// the hint names them and says they aren't there, so the model answers
// without them instead of calling them next. all holds the names of every
// tool config allows. It returns "" for a name that isn't a skill, or a
// skill whose tools config turns off, and dispatch then gives its usual
// refusal.
//
// The call still goes to dispatch, which denies and records it like any
// call to a tool no backend offers; the hint only changes the words, so
// the model can recover in its next round.
func skillHint(reg *skills.Registry, name string, offer []engine.ToolSpec, all []string) string {
	if reg == nil || !reg.Has(name) {
		return ""
	}
	names := make([]string, len(offer))
	for i, s := range offer {
		names[i] = s.Name
	}
	if tools := skillToolNames(reg, name, names); len(tools) > 0 {
		return name + " is a skill, not a tool. Call " + orList(tools) + "."
	}
	if tools := skillToolNames(reg, name, all); len(tools) > 0 {
		return name + " is a skill, not a tool. It works through " + orList(tools) +
			", and this answer doesn't offer them, so answer without them."
	}
	return ""
}

// orList joins names as English does: "a", "a or b", "a, b or c".
func orList(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// skillInfos turns the picked names into the list the "route" event
// carries. Only Name is set: the client shows the names next to the route.
func skillInfos(names []string) []rpc.SkillInfo {
	if len(names) == 0 {
		return nil
	}
	out := make([]rpc.SkillInfo, len(names))
	for i, n := range names {
		out[i] = rpc.SkillInfo{Name: n}
	}
	return out
}
