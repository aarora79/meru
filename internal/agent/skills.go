// This file holds the skills part of a turn (ARCHITECTURE.md, "Skills"):
// one short call to the fast model that picks the skills a question needs,
// run side by side with the router, and the two prompt sections that
// follow from it. Every turn lists each skill's name and description; only
// the picked skills' instructions join the prompt. That split is called
// progressive disclosure.

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

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/skills"
)

// Headers for the two skills sections of the system prompt.
const (
	skillsListHeader = "Skills you can use:"
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
// A picked skill whose file can't be read now, say because it was deleted
// a moment ago, is left out with a warning.
func (a *Agent) skillsSection(ctx context.Context, p pickedSkills) (list, bodies string) {
	if p.reg == nil {
		return "", ""
	}
	skills := p.reg.List()
	if len(skills) == 0 {
		return "", ""
	}
	lines := []string{skillsListHeader}
	for _, s := range skills {
		lines = append(lines, "- "+s.Name+": "+strings.Join(strings.Fields(s.Description), " "))
	}
	list = strings.Join(lines, "\n")

	var bodyList []namedBody
	for _, name := range p.names {
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
