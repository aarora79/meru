// This file picks the model that answers a turn: the main model, the fast
// model for an easy question, or the large model when the user asks for
// it. It also holds the step that asks main again when the fast model's
// answer ends early. See docs/design/model-router.md.

package agent

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/config"
)

// The tiers a turn's answer can come from, as metrics and the transcript
// name them.
const (
	tierFast  = "fast"
	tierMain  = "main"
	tierLarge = "large"
)

// errNoLarge is what an ask for the large model gets when config names
// none.
var errNoLarge = errors.New("there's no large model: set [models] large in config.toml, as a model name or \"@name\" of an [[endpoints]] entry")

// useLarge makes the large model answer the turn, for a request with
// Large set. It fails when config names no large model.
func (a *Agent) useLarge(t *turn) error {
	if a.models.Large == "" {
		return errNoLarge
	}
	t.model, t.tier = a.models.Large, tierLarge
	// The large model thinks as the main model does now.
	_, t.noThink = a.mainModel()
	return nil
}

// pickFast gives the turn to the fast model when the question is easy:
// with [router] answers = "routed", the route is direct and the router
// chose it itself, at least easy_above sure, and the turn carries no
// picture, no skill, no web search and no hand-off from a Continue. The
// fast model then starts in a fraction of the time the main model takes.
// Anything else stays with the main model, and so does a turn that
// escalated, or one asked of the large model.
//
// It reads the route after every rule in respond has had its say, so a
// direct question that names an indexed folder, which a rule sends to
// search, stays with main. It reports whether it picked the fast model.
func (a *Agent) pickFast(t *turn, dec Decision, skills []string) bool {
	switch {
	case a.answers != config.AnswersRouted, t.tier != tierMain, t.noFast:
		return false
	case dec.Route != "direct" || dec.Outcome != "ok" || dec.Confidence < a.easyAbove:
		return false
	case len(t.images) > 0 || len(skills) > 0 || t.webFirst != "" || t.handoff != "":
		return false
	case a.models.Fast == "" || a.models.Fast == t.model:
		return false
	}
	t.model, t.noThink, t.tier = a.models.Fast, true, tierFast
	return true
}

// weakEnding reports whether a turn that ended as ended gave a weak
// answer: cut off at its length, out of rounds, out of room in its
// window, given up, or output Ollama couldn't read. A fast model's weak
// answer goes to main; a main model's offers the large one.
func weakEnding(ended string) bool {
	switch ended {
	case endCutOff, endCapped, endWindowFull, endGaveUp, endBadOutput:
		return true
	}
	return false
}

// whyEscalate says, for the escalate event, why the fast model's answer
// didn't stand.
func whyEscalate(ended string) string {
	switch ended {
	case endCutOff:
		return "the fast model's answer was cut off"
	case endCapped:
		return "the fast model used every tool round"
	case endWindowFull:
		return "the fast model ran out of room"
	case endBadOutput:
		return "the fast model wrote a tool call Ollama couldn't read"
	}
	return "the fast model gave no answer"
}

// escalated returns a turn that asks the question again of the main
// model, after t's fast answer ended weak. It keeps what identifies the
// turn (the session, the question, the approver, the IDs it numbered) and
// drops what the first try gathered: its rounds, its counts and its
// tools. noFast keeps the router from picking the fast model again.
func (a *Agent) escalated(t *turn) *turn {
	n := &turn{
		sess: t.sess, source: t.source, scope: t.scope, images: t.images,
		traceID: t.traceID, emit: t.emit, approve: t.approve, question: t.question,
		job: t.job, skill: t.skill, handoff: t.handoff,
		// Tool call IDs number across the whole turn, so the second try's
		// calls don't reuse the first try's.
		calls:  t.calls,
		noFast: true,
	}
	n.model, n.noThink = a.mainModel()
	n.tier = tierMain
	return n
}

// escalateText is the escalate event's Text: the reason, and the model
// that answers next.
func escalateText(ended, model string) string {
	return fmt.Sprintf("%s, so %s answers.", whyEscalate(ended), model)
}

// recordEscalation marks the turn's span with an "escalate" event and how
// the fast answer ended, so a trace shows the second try and why.
func recordEscalation(ctx context.Context, ended string) {
	trace.SpanFromContext(ctx).AddEvent("escalate", trace.WithAttributes(attribute.String("meru.ended", ended)))
}
