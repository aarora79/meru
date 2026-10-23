// This file keeps a turn's prompt inside the model's context window. Each
// tool round adds its calls and their results to the prompt, and every
// later round sends them all again, so a turn that reads many pages grows
// past [ollama] context_length. Ollama then stops the answer with "length"
// after a few hundred words. Before each round, fitWindow estimates the
// next prompt; past windowShare of the window it shortens the turn's
// oldest tool results, and when that isn't enough the turn stops offering
// tools. See ARCHITECTURE.md, "Agent loop", step 4.

package agent

import (
	"context"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/engine"
)

// windowShare is the percent of the context window a round's prompt may
// fill. The rest is room for the answer: at the default 32,768 tokens,
// 20% leaves about 6,500 tokens to write in.
const windowShare = 80

// charsPerToken turns characters into tokens for the estimate. English
// prose runs about four characters a token, and tool results with URLs,
// JSON and code closer to three. The estimate counts only the messages
// Ollama hasn't counted yet; see fitWindow.
const charsPerToken = 3

// keepChars is how much of a shortened result stays: enough for the model
// to recall what the call found and decide whether to call it again.
const keepChars = 400

// shortenedNote ends each shortened result.
const shortenedNote = "\n\n[Meru shortened this earlier result to keep the prompt inside the model's context window. " +
	"Call the tool again if you need all of it.]"

// fitWindow readies msgs for the turn's next round. base is the index of
// the turn's first tool round in msgs, newest the index of the latest
// round's assistant message, and measured the prompt tokens Ollama counted
// for the round that just ran (0 when unknown). specs are the tools the
// next round offers, whose schemas count toward the prompt.
//
// When the estimate passes windowShare of a.window, it shortens the tool
// results in msgs[base:newest], oldest first, until the estimate fits.
// It never touches the latest round's results, which the model hasn't
// read yet, nor anything before base: the system prompt, the history and
// the question are the budget's to size (budget.go). A shortened call
// leaves t.seen, so calling it again runs it afresh instead of counting
// as a repeat.
//
// It changes msgs in place and returns true when the prompt still doesn't
// fit, so the caller stops offering tools and the model answers from what
// it has. With a window of 0, Ollama's own setting, it does nothing.
func (a *Agent) fitWindow(ctx context.Context, t *turn, msgs []engine.Message, base, newest, measured int, specs []engine.ToolSpec) bool {
	window := a.answerWindow()
	if window <= 0 {
		return false
	}
	limit := window * windowShare / 100
	// Ollama counted the prompt the round that just ran sent, so only the
	// latest round's messages, new since then, need an estimate. Counting
	// the whole prompt from characters runs far high: the tool schemas
	// and skills are JSON and Markdown that pack more characters into a
	// token. In a real turn that estimate passed 80% of the window after
	// one web_search whose prompt Ollama counted at 18,000 tokens, and the
	// turn lost its tools. The count from characters is only the fallback
	// for an engine that reports none, as the tests' fake does.
	need := measured + charsOf(msgs[newest:])/charsPerToken
	if measured <= 0 {
		need = (charsOf(msgs) + schemaChars(specs)) / charsPerToken
	}
	if need <= limit {
		return false
	}
	before := need
	for i := base; i < newest && need > limit; i++ {
		m := &msgs[i]
		if m.Role != engine.RoleTool || utf8.RuneCountInString(m.Content) <= keepChars+len(shortenedNote) {
			continue
		}
		short := string([]rune(m.Content)[:keepChars]) + shortenedNote
		for key, result := range t.seen {
			if result == m.Content {
				delete(t.seen, key)
			}
		}
		need -= (utf8.RuneCountInString(m.Content) - utf8.RuneCountInString(short)) / charsPerToken
		m.Content = short
		t.shortened++
	}
	full := need > limit
	a.log.DebugContext(ctx, "prompt near the context window", "window", window, "limit", limit,
		"estimate", before, "after", need, "shortened", t.shortened, "tools_off", full)
	return full
}

// charsOf counts the characters the model reads in msgs: each message's
// text and its tool calls' names and arguments.
func charsOf(msgs []engine.Message) int {
	n := 0
	for _, m := range msgs {
		n += utf8.RuneCountInString(m.Content)
		for _, c := range m.ToolCalls {
			n += len(c.Name) + len(c.Arguments)
		}
	}
	return n
}
