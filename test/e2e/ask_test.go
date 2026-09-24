//go:build e2e

// This file tests a question from start to finish when everything works: the
// answer on stdout, streaming order, the transcript, a session that carries
// on across turns, and the route the router reports.

package e2e

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// TestAsk runs `meru "question"` and checks stdout, the exit status and the
// two transcript lines the turn writes.
func TestAsk(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	const question = "What is the capital of France?"
	const answer = "Paris is the capital of France."
	s.fake.enqueue(t, fastModel, directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: answer, PromptTokens: 42, OutputTokens: 7})

	res := runMeru(t, s.home, question)
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s", res.code, res.stderr)
	}
	if res.stdout != answer+"\n" {
		t.Errorf("stdout = %q, want %q", res.stdout, answer+"\n")
	}

	files := sessionFiles(t, s.home)
	if len(files) != 1 {
		t.Fatalf("want one transcript, found %v", files)
	}
	lines := readTranscript(t, files[0])
	if len(lines) != 2 {
		t.Fatalf("transcript has %d lines, want 2: %+v", len(lines), lines)
	}
	user, asst := lines[0], lines[1]
	if user.Type != transcript.TypeUser || user.Text != question {
		t.Errorf("line 1 = %+v, want the user's question", user)
	}
	if asst.Type != transcript.TypeAssistant || asst.Text != answer {
		t.Errorf("line 2 = %+v, want the assistant's answer", asst)
	}
	if asst.TokensIn != 42 || asst.TokensOut != 7 {
		t.Errorf("assistant tokens in/out = %d/%d, want 42/7", asst.TokensIn, asst.TokensOut)
	}
	for i, l := range lines {
		if l.TS.IsZero() {
			t.Errorf("line %d has no timestamp", i+1)
		}
	}
}

// TestStreaming scripts an answer in several chunks with a pause between
// them, and checks that the pieces arrive in order, all of them, and that
// meru prints them as they come rather than all at the end.
func TestStreaming(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	chunks := []string{"One ", "two ", "three ", "four."}
	full := strings.Join(chunks, "")
	// slow returns the scripted answer. The 50ms pause before each later
	// chunk gives the test time to see the output grow.
	slow := fakeollama.Reply{Chunks: chunks, ChunkDelay: 50 * time.Millisecond}

	t.Run("socket events", func(t *testing.T) {
		s.fake.enqueue(t, fastModel, directRoute())
		s.fake.enqueue(t, mainModel, slow)
		events := ask(t, s.home.socket, "", "count to four")

		var got []string
		for _, ev := range eventsOf(events, rpc.EventToken) {
			got = append(got, ev.Text)
		}
		if !slices.Equal(got, chunks) {
			t.Errorf("token events = %q, want %q", got, chunks)
		}
		// The reply's events come in a fixed order: session, route, tokens,
		// done.
		if events[0].Type != rpc.EventSession || events[1].Type != rpc.EventRoute {
			t.Errorf("first events = %s, %s; want session, route", events[0].Type, events[1].Type)
		}
		if last := lastEvent(t, events); last.Type != rpc.EventDone {
			t.Errorf("last event = %+v, want done", last)
		}
	})

	t.Run("meru stdout", func(t *testing.T) {
		s.fake.enqueue(t, fastModel, directRoute())
		s.fake.enqueue(t, mainModel, slow)
		p := startMeru(t, s.home, "count to four")

		// Record each distinct state of stdout until meru exits.
		var seen []string
		for !p.exited() {
			if out := p.stdout.String(); len(seen) == 0 || out != seen[len(seen)-1] {
				seen = append(seen, out)
			}
			time.Sleep(time.Millisecond)
		}
		if code := p.wait(t, exitTimeout); code != 0 {
			t.Fatalf("meru exited %d, stderr:\n%s", code, p.stderr.String())
		}
		final := p.stdout.String()
		if final != full+"\n" {
			t.Fatalf("stdout = %q, want %q", final, full+"\n")
		}
		partial := false
		for _, out := range seen {
			if !strings.HasPrefix(final, out) {
				t.Errorf("stdout was once %q, which isn't a prefix of the final %q", out, final)
			}
			if out != "" && len(out) < len(full) {
				partial = true
			}
		}
		if !partial {
			t.Errorf("never saw part of the answer on stdout before the end; states seen: %q", seen)
		}
	})
}

// TestSessionContinues asks two questions on one session and checks that
// the second model call carries the first turn, and that the transcript
// holds all four lines.
func TestSessionContinues(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	s.fake.enqueue(t, fastModel, directRoute(), directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "Nice to meet you, Ada."}, fakeollama.Reply{Text: "Your name is Ada."})

	first := ask(t, s.home.socket, "", "My name is Ada.")
	sessions := eventsOf(first, rpc.EventSession)
	if len(sessions) != 1 || sessions[0].Session == "" {
		t.Fatalf("first reply has session events %+v, want one with an ID", sessions)
	}
	id := sessions[0].Session

	second := ask(t, s.home.socket, id, "What is my name?")
	if got := eventsOf(second, rpc.EventSession); len(got) != 1 || got[0].Session != id {
		t.Errorf("second reply's session events = %+v, want session %s", got, id)
	}
	if got := answerOf(second); got != "Your name is Ada." {
		t.Errorf("second answer = %q", got)
	}

	// The answer calls are the streaming /api/chat requests for the main
	// model; the warm-up call before them doesn't stream.
	var answers []fakeollama.Request
	for _, r := range s.fake.chatRequests(t, mainModel) {
		if r.Stream {
			answers = append(answers, r)
		}
	}
	if len(answers) != 2 {
		t.Fatalf("fake got %d answer calls, want 2", len(answers))
	}
	// A struct type written in place, with only the fields the test reads.
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(answers[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range body.Messages {
		got = append(got, m.Role+": "+m.Content)
	}
	// The system prompt comes first; its text is merud's business.
	want := []string{"user: My name is Ada.", "assistant: Nice to meet you, Ada.", "user: What is my name?"}
	if len(got) != 4 || !strings.HasPrefix(got[0], "system: ") || !slices.Equal(got[1:], want) {
		t.Errorf("second answer call's messages =\n%q\nwant a system message, then\n%q", got, want)
	}

	lines := readTranscript(t, sessionFile(t, s.home, id))
	var types []string
	for _, l := range lines {
		types = append(types, l.Type)
	}
	wantTypes := []string{"user", "assistant", "user", "assistant"}
	if !slices.Equal(types, wantTypes) {
		t.Errorf("transcript line types = %q, want %q", types, wantTypes)
	}
	if n := len(sessionFiles(t, s.home)); n != 1 {
		t.Errorf("found %d transcripts, want 1", n)
	}
}

// TestRouting scripts the router's letter probabilities and checks the route
// event: a clear winner gives its route and probability, and an unsure or
// meaningless distribution gives the fallback, "search+tools".
func TestRouting(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	tests := []struct {
		name      string
		reply     fakeollama.Reply
		wantRoute string
		wantConf  float64
	}{
		{"clear direct", directRoute(), "direct", 0.9},
		{"clear tools", routeReply(letter{"C", 0.8}, letter{"A", 0.1}, letter{"B", 0.05}, letter{"D", 0.05}), "tools", 0.8},
		{"clear search", routeReply(letter{"B", 0.7}, letter{"A", 0.2}, letter{"D", 0.1}), "search", 0.7},
		// The best letter, A at 0.3, falls below min_confidence (0.45), so
		// the router takes the fallback and reports the score that fell short.
		{"low confidence", routeReply(letter{"A", 0.3}, letter{"B", 0.25}, letter{"C", 0.25}, letter{"D", 0.2}), "search+tools", 0.3},
		// No route letter among the alternatives: nothing to go on.
		{"no letters", routeReply(letter{"Hello", 0.6}, letter{"The", 0.4}), "search+tools", 0},
	}
	// These subtests share one merud, so they run one after another.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.fake.enqueue(t, fastModel, tt.reply)
			s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "ok"})
			events := ask(t, s.home.socket, "", "route this: "+tt.name)
			if last := lastEvent(t, events); last.Type != rpc.EventDone {
				t.Fatalf("reply ended with %+v, want done", last)
			}
			routes := eventsOf(events, rpc.EventRoute)
			if len(routes) != 1 {
				t.Fatalf("got %d route events, want 1", len(routes))
			}
			r := routes[0]
			if r.Route != tt.wantRoute || math.Abs(r.Confidence-tt.wantConf) > 1e-6 {
				t.Errorf("route = %s at %.4f, want %s at %.4f", r.Route, r.Confidence, tt.wantRoute, tt.wantConf)
			}
		})
	}

	// Every router call asked for one token with log probabilities.
	for _, r := range s.fake.chatRequests(t, fastModel) {
		var body struct {
			Stream      bool `json:"stream"`
			LogProbs    bool `json:"logprobs"`
			TopLogProbs int  `json:"top_logprobs"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		if !body.LogProbs {
			continue // the warm-up call
		}
		if body.Stream || body.TopLogProbs != 20 {
			t.Errorf("router call: stream=%v top_logprobs=%d, want false and 20", body.Stream, body.TopLogProbs)
		}
	}
}
