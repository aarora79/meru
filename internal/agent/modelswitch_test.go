// This file tests what the agent records about the answer model: the
// model_switch line a session gets when its answers start to come from
// another model, the think setting of a model set, and the count of tool
// calls the model wrote that Meru couldn't run as written.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// switchLines returns the model_switch lines of a session, as "from>to".
func switchLines(lines []transcript.Line) []string {
	var out []string
	for _, l := range lines {
		if l.Type == transcript.TypeModelSwitch {
			out = append(out, l.Tier+":"+l.From+">"+l.To)
		}
	}
	return out
}

// TestModelSwitchLines checks that a session's first answer names its
// model, that later answers from the same model add nothing, and that an
// answer after a switch names both models, before the answer line.
func TestModelSwitchLines(t *testing.T) {
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	a := ollamaAgent(t, cfg, srv, "direct", nil)
	ctx := context.Background()

	evs, err := run(ctx, a, rpc.Request{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	id := evs[0].Session
	if _, err := run(ctx, a, rpc.Request{Text: "second", Session: id}); err != nil {
		t.Fatal(err)
	}
	a.SetMain("gemma4:26b-mxfp8", false)
	if _, err := run(ctx, a, rpc.Request{Text: "third", Session: id}); err != nil {
		t.Fatal(err)
	}

	lines := allLines(t, cfg, id)
	want := []string{"main:>main-model", "main:main-model>gemma4:26b-mxfp8"}
	if got := switchLines(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("model_switch lines = %v, want %v", got, want)
	}
	// Each model_switch line sits just before the answer it names.
	for i, l := range lines {
		if l.Type == transcript.TypeModelSwitch && (i+1 >= len(lines) || lines[i+1].Type != transcript.TypeAssistant) {
			t.Errorf("line %d is a model_switch not followed by an answer: %+v", i, lines)
		}
	}
	if m := transcript.LastModel(lines); m != "gemma4:26b-mxfp8" {
		t.Errorf("LastModel = %q, want gemma4:26b-mxfp8", m)
	}
}

// TestThinkOffReachesOllama checks that SetMain's noThink sends think:
// false with each answer call, and that without it the key stays out, so
// the model does what it does by default.
func TestThinkOffReachesOllama(t *testing.T) {
	for _, off := range []bool{false, true} {
		cfg := ollamaConfig(t)
		srv := fakeollama.Start(t, fakeollama.Config{})
		a := ollamaAgent(t, cfg, srv, "direct", nil)
		a.SetMain(cfg.Models.Main, off)
		if _, err := run(context.Background(), a, rpc.Request{Text: "hello"}); err != nil {
			t.Fatal(err)
		}
		var sent []map[string]any
		for _, r := range srv.Requests("/api/chat") {
			if r.Model != cfg.Models.Main {
				continue
			}
			var body map[string]any
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatal(err)
			}
			sent = append(sent, body)
		}
		if len(sent) != 1 {
			t.Fatalf("think off %v: %d answer calls, want 1", off, len(sent))
		}
		think, has := sent[0]["think"]
		if off && (!has || think != false) {
			t.Errorf("think off: body think = %v (present %v), want false", think, has)
		}
		if !off && has {
			t.Errorf("think left alone: body has think = %v, want no key", think)
		}
	}
}

// TestBadCallsCounted checks two kinds of malformed call through a whole
// turn: output Ollama couldn't read, and a tool the round didn't offer.
// Each adds to the answer line's bad_calls, the turn still answers, and
// the log names the model. The fake Ollama, like Ollama, sends arguments
// as an object, so TestIsObject covers the third kind.
func TestBadCallsCounted(t *testing.T) {
	unreadable := fakeollama.Reply{StreamError: xmlError}
	notOffered := fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "send_mail", Arguments: map[string]any{"to": "sam"}}}}
	good := fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "web_search", Arguments: map[string]any{"query": "go release"}}}}
	answer := fakeollama.Reply{Text: "Go 1.27 is out."}
	tests := []struct {
		name    string
		replies []fakeollama.Reply
		want    int
	}{
		{"a clean turn", []fakeollama.Reply{good, answer}, 0},
		{"output Ollama couldn't read, then a retry", []fakeollama.Reply{unreadable, good, answer}, 1},
		{"a tool the round didn't offer", []fakeollama.Reply{notOffered, answer}, 1},
		{"both", []fakeollama.Reply{unreadable, notOffered, answer}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, tt.replies...)
			tools := &fakeTools{
				specs:   []engine.ToolSpec{spec("web_search")},
				results: map[string]fakeResult{"web_search": {text: "1. Go 1.27 came out in August."}},
			}
			eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}},
				nil, tools, nil, nil, log)

			evs, err := run(context.Background(), a, rpc.Request{Text: "what's the latest go release"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := answerOf(evs); !strings.HasSuffix(got, "Go 1.27 is out.") {
				t.Errorf("answer = %q, want the turn to finish", got)
			}
			lines := readLines(t, cfg, evs[0].Session)
			last := lines[len(lines)-1]
			if last.Type != transcript.TypeAssistant || last.BadCalls != tt.want || last.Outcome != "" {
				t.Errorf("answer line = %+v, want bad_calls %d and a full answer", last, tt.want)
			}
			if got := strings.Count(logs.String(), `msg="malformed tool call"`); got != tt.want {
				t.Errorf("log has %d malformed-call lines, want %d:\n%s", got, tt.want, logs.String())
			}
			if tt.want > 0 && !strings.Contains(logs.String(), "model="+cfg.Models.Main) {
				t.Errorf("log doesn't name the model:\n%s", logs.String())
			}
		})
	}
}

// TestCapped checks that a turn which uses every round and writes no text
// is marked capped on its answer line, and that one which answers isn't.
func TestCapped(t *testing.T) {
	search := fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "web_search", Arguments: map[string]any{"query": "q"}}}}
	for _, tt := range []struct {
		name    string
		replies []fakeollama.Reply
		want    bool
	}{
		{"answers on the last round", []fakeollama.Reply{search, {Text: "Here it is."}}, false},
		{"nothing on the last round", []fakeollama.Reply{search, {Text: ""}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			cfg.Agent.MaxRounds = 2
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, tt.replies...)
			tools := &fakeTools{specs: []engine.ToolSpec{spec("web_search")},
				results: map[string]fakeResult{"web_search": {text: "1. a result"}}}
			a := ollamaAgent(t, cfg, srv, "tools", tools)
			evs, err := run(context.Background(), a, rpc.Request{Text: "look it up"})
			if err != nil {
				t.Fatal(err)
			}
			lines := readLines(t, cfg, evs[0].Session)
			if last := lines[len(lines)-1]; last.Capped != tt.want {
				t.Errorf("answer line = %+v, want capped %v", last, tt.want)
			}
		})
	}
}

// TestIsObject checks which arguments count as a JSON object.
func TestIsObject(t *testing.T) {
	tests := []struct {
		args string
		want bool
	}{
		{"", true},
		{`{}`, true},
		{` {"q": "x"} `, true},
		{`"just text"`, false},
		{`["a"]`, false},
		{`{"q": `, false},
	}
	for _, tt := range tests {
		if got := isObject([]byte(tt.args)); got != tt.want {
			t.Errorf("isObject(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
