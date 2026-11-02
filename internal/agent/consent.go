// This file asks the user before a turn goes off this machine. When the
// answer model is an [[endpoints]] entry with reach "near-edge" or
// "remote", the turn's whole prompt would travel there: the question, the
// chat so far, the profile and recalled memories, file excerpts, pictures,
// and each tool result. So the turn asks first, "this time" or "this
// chat", never "always". A no, an incognito chat, a scheduled job, or a
// client that can't ask keeps the turn on this machine, answered by the
// fast model. See docs/design/model-router.md.

package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// consents remembers the chats whose user said "this chat" to an
// endpoint, by session ID and endpoint name, until merud stops. Nothing
// writes it to disk, so a restart asks again: a yes given yesterday
// shouldn't quietly cover today.
type consents struct {
	mu sync.Mutex // guards ok
	ok map[string]bool
}

// key joins a session ID and an endpoint name. The byte between them can
// be in neither, so two pairs never share a key.
func (c *consents) key(session, endpoint string) string {
	return session + "\x00" + endpoint
}

// has reports whether the chat said "this chat" to the endpoint.
func (c *consents) has(session, endpoint string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ok[c.key(session, endpoint)]
}

// allow records "this chat" for the chat and the endpoint.
func (c *consents) allow(session, endpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok == nil {
		c.ok = map[string]bool{}
	}
	c.ok[c.key(session, endpoint)] = true
}

// consent checks the turn's answer model before the turn builds its
// prompt. When it is an endpoint off this machine, consent asks the user
// unless the chat already said "this chat", and on anything but a yes it
// switches the turn to the fast model, which runs on the local Ollama,
// with the reason in t.keptHere for the notice under the answer. It writes
// a consent line to the transcript for each answer the user gives.
//
// historyTurns is how many earlier turns the prompt will hold, for the
// question's list of what the turn carries. It fails only when the client
// connection fails or ctx ends while it waits for the answer, or when the
// transcript can't be written.
func (a *Agent) consent(ctx context.Context, t *turn, historyTurns int) error {
	name, ok := config.EndpointRef(t.model)
	if !ok {
		return nil
	}
	ep, found := config.FindEndpoint(a.endpoints, name)
	if !found || ep.Reach == config.ReachLocal || ep.Reach == "" {
		return nil
	}
	session := t.sess.ID()
	switch {
	case t.sess.Incognito():
		a.keepHere(t, "an incognito chat stays on this computer")
		return nil
	case t.job != "":
		a.keepHere(t, "a scheduled job runs on this computer")
		return nil
	case t.approve == nil:
		a.keepHere(t, "this client can't ask you before a question leaves this computer")
		return nil
	case a.consented.has(session, name):
		return nil
	}

	choice, err := t.approve(ctx, rpc.Approval{
		ID:      "consent-" + t.traceID,
		Name:    t.model,
		Kind:    rpc.KindConsent,
		Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny},
		Consent: &rpc.ConsentInfo{Endpoint: name, Reach: ep.Reach, Model: ep.Model,
			Carries: carries(historyTurns, len(t.images))},
	})
	if err != nil {
		return fmt.Errorf("ask before sending the turn to %s: %w", t.model, err)
	}
	// Only the three choices offered count; anything else is a no.
	switch choice {
	case rpc.ChoiceSession:
		a.consented.allow(session, name)
	case rpc.ChoiceOnce:
	default:
		choice = rpc.ChoiceDeny
		a.keepHere(t, "you said no to sending it to "+t.model)
	}
	a.log.InfoContext(ctx, "consent", "endpoint", name, "reach", ep.Reach, "choice", string(choice))
	return a.appendLine(ctx, t.sess, transcript.Line{
		Type: transcript.TypeConsent, Tool: t.model, Reach: ep.Reach, Choice: string(choice), TraceID: t.traceID,
	})
}

// keepHere switches the turn to the fast model, which always runs on the
// local Ollama, and records why for the notice: "Answered on this computer
// by <model>, since <why>."
func (a *Agent) keepHere(t *turn, why string) {
	t.model, t.noThink = a.models.Fast, true
	t.keptHere = "Answered on this computer by " + t.model + ", since " + why + "."
}

// carries lists what a turn's prompt holds, for the consent question:
// always the question, the profile and memories, file excerpts and tool
// results, since the turn may add any of them after the user answers; the
// earlier turns and pictures with their counts.
func carries(historyTurns, pictures int) []string {
	out := []string{"your question"}
	if historyTurns > 0 {
		out = append(out, plural(historyTurns, "earlier turn")+" of this chat")
	}
	if pictures > 0 {
		out = append(out, plural(pictures, "picture"))
	}
	return append(out,
		"your profile and the memories Meru recalls for it",
		"excerpts from your files, when the question needs them",
		"the results of any tools this turn runs")
}

// plural writes n and noun, with an "s" when n isn't 1: "1 picture",
// "3 earlier turns".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// offMachine returns model and its reach when model is an endpoint off this
// computer, for the "done" event, and two empty strings otherwise.
func (a *Agent) offMachine(model string) (string, string) {
	name, ok := config.EndpointRef(model)
	if !ok {
		return "", ""
	}
	ep, found := config.FindEndpoint(a.endpoints, name)
	if !found || ep.Reach == config.ReachLocal || ep.Reach == "" {
		return "", ""
	}
	return model, ep.Reach
}
