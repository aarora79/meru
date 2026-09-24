// This file tests the router: the probability arithmetic, letter matching,
// the three outcomes through a fake engine, and the request that reaches a
// fake Ollama. No test here needs a real model.

package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// alts builds a PositionLogProbs from token and log-probability pairs, to
// keep the tables short.
func alts(pairs ...any) engine.PositionLogProbs {
	var pos engine.PositionLogProbs
	for i := 0; i+1 < len(pairs); i += 2 {
		pos.Top = append(pos.Top, engine.TokenLogProb{Token: pairs[i].(string), LogProb: pairs[i+1].(float64)})
	}
	return pos
}

// near reports whether a and b differ by less than a rounding error.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestProbs(t *testing.T) {
	ln := math.Log
	tests := []struct {
		name string
		pos  engine.PositionLogProbs
		temp float64
		want map[Route]float64 // nil means "no distribution"
	}{
		{
			name: "no letters",
			pos:  alts("The", -0.1, "I", -2.0, "1", -3.0),
			temp: 1,
			want: nil,
		},
		{
			name: "empty",
			pos:  engine.PositionLogProbs{},
			temp: 1,
			want: nil,
		},
		{
			name: "one letter",
			pos:  alts("B", ln(0.3), "The", ln(0.6)),
			temp: 1,
			want: map[Route]float64{RouteSearch: 1},
		},
		{
			name: "four letters, raw",
			pos:  alts("A", ln(0.4), "B", ln(0.2), "C", ln(0.1), "D", ln(0.1), "The", ln(0.2)),
			temp: 1,
			want: map[Route]float64{RouteDirect: 0.5, RouteSearch: 0.25, RouteTools: 0.125, RouteSearchTools: 0.125},
		},
		{
			name: "tie",
			pos:  alts("A", ln(0.3), "C", ln(0.3)),
			temp: 1,
			want: map[Route]float64{RouteDirect: 0.5, RouteTools: 0.5},
		},
		{
			// Temperature 2 takes the square root of each probability
			// before normalising: 0.64 and 0.16 become 0.8 and 0.4.
			name: "temperature flattens",
			pos:  alts("A", ln(0.64), "B", ln(0.16)),
			temp: 2,
			want: map[Route]float64{RouteDirect: 0.8 / 1.2, RouteSearch: 0.4 / 1.2},
		},
		{
			name: "spaced and lower-case tokens add up",
			pos:  alts("A", ln(0.2), " A", ln(0.1), "a", ln(0.1), "D", ln(0.4)),
			temp: 1,
			want: map[Route]float64{RouteDirect: 0.5, RouteSearchTools: 0.5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := probs(tt.pos, tt.temp)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("probs = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("probs = %v, want %v", got, tt.want)
			}
			sum := 0.0
			for r, w := range tt.want {
				if !near(got[r], w) {
					t.Errorf("probs[%s] = %v, want %v", r, got[r], w)
				}
				sum += got[r]
			}
			if !near(sum, 1) {
				t.Errorf("probabilities sum to %v, want 1", sum)
			}
		})
	}
}

func TestTemperatureKeepsWinner(t *testing.T) {
	pos := alts("A", -1.2, "B", -0.4, "C", -2.5, "D", -3.0)
	for _, temp := range []float64{0.5, 1, 2, 2.5, 10} {
		win, _ := best(probs(pos, temp))
		if win != RouteSearch {
			t.Errorf("temperature %v: winner %s, want search", temp, win)
		}
	}
}

func TestBestBreaksTiesByLetterOrder(t *testing.T) {
	p := map[Route]float64{RouteTools: 0.5, RouteSearch: 0.5}
	// Go walks maps in random order, so repeat to catch an order-dependent tie-break.
	for range 50 {
		if win, conf := best(p); win != RouteSearch || conf != 0.5 {
			t.Fatalf("best = %s %v, want search 0.5", win, conf)
		}
	}
	if win, conf := best(nil); win != "" || conf != 0 {
		t.Errorf("best(nil) = %q %v, want empty", win, conf)
	}
}

func TestRouteForLetter(t *testing.T) {
	tests := []struct {
		token  string
		want   Route
		wantOK bool
	}{
		{"A", RouteDirect, true},
		{" A", RouteDirect, true},
		{"a", RouteDirect, true},
		{" a", RouteDirect, true},
		{"A\n", RouteDirect, true},
		{"B", RouteSearch, true},
		{"c", RouteTools, true},
		{" D", RouteSearchTools, true},
		{"AB", "", false},
		{"Alpha", "", false},
		{"1", "", false},
		{"E", "", false},
		{"", "", false},
		{" ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			got, ok := routeForLetter(tt.token)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("routeForLetter(%q) = %q, %v; want %q, %v", tt.token, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestBuildMessages(t *testing.T) {
	turn := Turn{
		SystemPrompt: "You are Meru.",
		History: []engine.Message{
			{Role: engine.RoleUser, Content: "first question"},
			{Role: engine.RoleAssistant, Content: "first answer"},
			{Role: engine.RoleTool, Content: "a long tool result"},
			{Role: engine.RoleUser, Content: "second question"},
		},
		Question: "What did I write about Go?",
	}
	msgs := buildMessages(turn)
	if len(msgs) != 2 || msgs[0].Role != engine.RoleSystem || msgs[0].Content != "You are Meru." {
		t.Fatalf("messages = %+v, want a system message then a user message", msgs)
	}
	user := msgs[1].Content
	if msgs[1].Role != engine.RoleUser {
		t.Errorf("second message role = %s, want user", msgs[1].Role)
	}
	if !strings.HasSuffix(user, "Reply with one letter and nothing else.\nAnswer: ") {
		t.Errorf("prompt must end with the answer cue; got %q", user[max(0, len(user)-60):])
	}
	// Newest history first, tool results left out.
	second := strings.Index(user, "user: second question")
	first := strings.Index(user, "user: first question")
	if second < 0 || first < 0 || second > first {
		t.Errorf("history should list newest first:\n%s", user)
	}
	if strings.Contains(user, "tool result") {
		t.Errorf("history should skip tool messages:\n%s", user)
	}
	for _, want := range []string{
		"Question:\nWhat did I write about Go?",
		"A = General knowledge",
		"B = Look in the user's own saved notes",
		"C = Live, recent or outside data",
		"D = Needs the user's notes or files AND",
		"Examples:\nwhat's the boiling point of water in Denver -> A\n",
		"text Maya the gate code from my notes -> D\n",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt lacks %q:\n%s", want, user)
		}
	}
	// The fixed part (options, examples) comes before the parts that change.
	if strings.Index(user, "Examples:") > strings.Index(user, "Conversation so far:") {
		t.Errorf("options and examples should come before the history:\n%s", user)
	}

	// No system prompt and no history: one message, history marked empty.
	msgs = buildMessages(Turn{Question: "hi"})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Conversation so far:\n(none)\n") {
		t.Errorf("messages = %+v", msgs)
	}

	// Two different turns share every token up to the history, so Ollama
	// can reuse its work on that prefix from one turn to the next.
	other := buildMessages(Turn{Question: "something else"})[0].Content
	prefix := msgs[0].Content[:strings.Index(msgs[0].Content, "Conversation so far:")]
	if !strings.HasPrefix(other, prefix) {
		t.Errorf("the fixed prefix changed between turns")
	}
}

func TestExamplesCoverEveryRoute(t *testing.T) {
	count := map[Route]int{}
	for _, e := range examples {
		if letterFor(e.route) == "" {
			t.Errorf("example %q has route %q, which has no letter", e.question, e.route)
		}
		count[e.route]++
	}
	for _, o := range options {
		if count[o.route] == 0 {
			t.Errorf("no example for route %s", o.route)
		}
	}
	if letterFor("web") != "" {
		t.Error(`letterFor("web") should be empty`)
	}
	// Examples must not repeat a labelled question, or the held-out score
	// in eval_integration_test.go would flatter the router.
	rows, err := loadLabelled("testdata/routes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for _, e := range examples {
			if strings.EqualFold(r.Q, e.question) {
				t.Errorf("example %q is also a labelled question", e.question)
			}
		}
	}
}

// fakeEngine is an in-process Engine that returns a canned Completion from
// Generate and remembers what it was asked. Embedding engine.Engine (a nil
// interface) gives it the other three methods; calling them would panic,
// which is fine because the router never does.
type fakeEngine struct {
	engine.Engine
	comp engine.Completion
	err  error

	gotMsgs []engine.Message
	gotOpts engine.Options
}

// Generate records its arguments and returns the canned answer.
func (f *fakeEngine) Generate(_ context.Context, msgs []engine.Message, _ []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	f.gotMsgs, f.gotOpts = msgs, opts
	return f.comp, f.err
}

// testConfig is a valid Config for the outcome tests.
func testConfig() Config {
	return Config{Model: "fast", TopLogProbs: 20, Temperature: 1, MinConfidence: 0.45, Fallback: RouteSearchTools}
}

func TestDecideOutcomes(t *testing.T) {
	ln := math.Log
	tests := []struct {
		name     string
		lps      []engine.PositionLogProbs
		want     Route
		wantConf float64
		outcome  Outcome
	}{
		{
			name:     "ok",
			lps:      []engine.PositionLogProbs{alts("C", ln(0.7), "A", ln(0.2), "D", ln(0.05), "B", ln(0.05))},
			want:     RouteTools,
			wantConf: 0.7,
			outcome:  OutcomeOK,
		},
		{
			name:     "low confidence",
			lps:      []engine.PositionLogProbs{alts("A", ln(0.4), "B", ln(0.3), "C", ln(0.3))},
			want:     RouteSearchTools,
			wantConf: 0.4,
			outcome:  OutcomeLowConfidence,
		},
		{
			name:    "degraded, one letter",
			lps:     []engine.PositionLogProbs{alts("A", ln(0.9), "The", ln(0.1))},
			want:    RouteSearchTools,
			outcome: OutcomeDegraded,
		},
		{
			name:    "degraded, no letters",
			lps:     []engine.PositionLogProbs{alts("We", ln(0.9))},
			want:    RouteSearchTools,
			outcome: OutcomeDegraded,
		},
		{
			name:    "degraded, no log probabilities",
			lps:     nil,
			want:    RouteSearchTools,
			outcome: OutcomeDegraded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{comp: engine.Completion{Text: "x", LogProbs: tt.lps}}
			got, err := Decide(context.Background(), eng, testConfig(), Turn{Question: "q"})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got.Route != tt.want || got.Outcome != tt.outcome || !near(got.Confidence, tt.wantConf) {
				t.Errorf("Decide = %s %s %v, want %s %s %v", got.Route, got.Outcome, got.Confidence, tt.want, tt.outcome, tt.wantConf)
			}
			want := engine.Options{Model: "fast", MaxTokens: 1, LogProbs: true, TopLogProbs: 20}
			if eng.gotOpts != want {
				t.Errorf("options = %+v, want %+v", eng.gotOpts, want)
			}
			if n := len(eng.gotMsgs); n == 0 || !strings.HasSuffix(eng.gotMsgs[n-1].Content, "Answer: ") {
				t.Errorf("prompt should end with the answer cue; messages = %+v", eng.gotMsgs)
			}
		})
	}
}

func TestDecideModelError(t *testing.T) {
	boom := errors.New("boom")
	_, err := Decide(context.Background(), &fakeEngine{err: boom}, testConfig(), Turn{Question: "q"})
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want it to wrap boom", err)
	}
}

func TestConfigFrom(t *testing.T) {
	good := config.Router{TopLogProbs: 20, Temperature: 1, MinConfidence: 0.45, Fallback: "search+tools"}
	cfg, err := ConfigFrom(good, "fast-model")
	want := Config{Model: "fast-model", TopLogProbs: 20, Temperature: 1, MinConfidence: 0.45, Fallback: RouteSearchTools}
	if err != nil || cfg != want {
		t.Fatalf("ConfigFrom(good) = %+v, %v; want %+v", cfg, err, want)
	}

	// Each case breaks one field of good.
	tests := []struct {
		name  string
		edit  func(r *config.Router)
		model string
	}{
		{"no model", func(r *config.Router) {}, ""},
		{"top_logprobs 0", func(r *config.Router) { r.TopLogProbs = 0 }, "m"},
		{"top_logprobs 21", func(r *config.Router) { r.TopLogProbs = 21 }, "m"},
		{"temperature 0", func(r *config.Router) { r.Temperature = 0 }, "m"},
		{"temperature NaN", func(r *config.Router) { r.Temperature = math.NaN() }, "m"},
		{"min_confidence 1.5", func(r *config.Router) { r.MinConfidence = 1.5 }, "m"},
		{"fallback unknown", func(r *config.Router) { r.Fallback = "web" }, "m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := good
			tt.edit(&r)
			if _, err := ConfigFrom(r, tt.model); err == nil {
				t.Errorf("ConfigFrom(%+v, %q): got nil error", r, tt.model)
			}
		})
	}
}

func TestDecideAgainstFakeOllama(t *testing.T) {
	// A canned /api/chat reply, shaped like the real one from Ollama 0.34.
	const reply = `{"message":{"role":"assistant","content":"B"},"done":true,"done_reason":"length",
	"logprobs":[{"token":"B","logprob":-0.105,"top_logprobs":[
		{"token":"B","logprob":-0.105},{"token":"A","logprob":-2.9},{"token":"D","logprob":-3.5},
		{"token":"C","logprob":-4.0},{"token":"The","logprob":-9.6}]}],
	"prompt_eval_count":120,"eval_count":1}`

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		io.WriteString(w, reply)
	}))
	defer srv.Close()

	eng, err := engine.NewOllama(srv.URL, "-1", "", srv.Client(), nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	cfg := testConfig()
	cfg.TopLogProbs = 7
	got, err := Decide(context.Background(), eng, cfg, Turn{Question: "What did I note about the budget?"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Route != RouteSearch || got.Outcome != OutcomeOK || got.Confidence < 0.85 {
		t.Errorf("Decide = %+v, want search, ok, confidence above 0.85", got)
	}

	var req struct {
		Model       string `json:"model"`
		Stream      bool   `json:"stream"`
		LogProbs    bool   `json:"logprobs"`
		TopLogProbs int    `json:"top_logprobs"`
		Think       *bool  `json:"think"`
		Options     struct {
			NumPredict int `json:"num_predict"`
		} `json:"options"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if req.Model != "fast" || req.Stream || !req.LogProbs || req.TopLogProbs != 7 ||
		req.Options.NumPredict != 1 || req.Think == nil || *req.Think {
		t.Errorf("request = %s\nwant model fast, stream false, logprobs true, top_logprobs 7, num_predict 1, think false", body)
	}
}

// The compile-time check below makes sure fakeEngine still satisfies
// engine.Engine if the interface changes.
var _ engine.Engine = (*fakeEngine)(nil)
