// This file tests a turn whose answer model can't call tools, as
// gemma3:12b can't: the turn must offer the model no tools, since Ollama
// refuses the request otherwise, and say so under the answer when the
// question could have used one.

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

func TestNoToolsModel(t *testing.T) {
	yes := func(context.Context, string) (bool, error) { return true, nil }
	no := func(context.Context, string) (bool, error) { return false, nil }
	broken := func(context.Context, string) (bool, error) { return false, errors.New("ollama is down") }
	tests := []struct {
		name    string
		route   string
		check   func(context.Context, string) (bool, error)
		offered bool // the model call carried tools
		notice  bool
	}{
		{"a model with tools", "tools", yes, true, false},
		{"no tools on a tools route", "tools", no, false, true},
		{"no tools on a direct question", "direct", no, false, false},
		{"a check that fails leaves the tools", "tools", broken, true, false},
		{"no check at all", "tools", nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ollamaConfig(t)
			srv := fakeollama.Start(t, fakeollama.Config{})
			srv.Enqueue(cfg.Models.Main, fakeollama.Reply{Text: "Lisbon is the capital of Portugal."})
			tools := &fakeTools{specs: []engine.ToolSpec{
				spec(builtin.DateTime), spec(builtin.AboutMeru), spec(builtin.WebSearch),
			}}
			a := ollamaAgent(t, cfg, srv, tt.route, tools)
			if tt.check != nil {
				a.UseToolCheck(tt.check)
			}

			evs, err := run(context.Background(), a, rpc.Request{Text: "what is the capital of Portugal?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			bodies := chatBodies(t, srv, cfg.Models.Main)
			if len(bodies) != 1 {
				t.Fatalf("main-model calls = %d, want 1", len(bodies))
			}
			if got := len(bodies[0].Tools) > 0; got != tt.offered {
				t.Errorf("tools offered = %v, want %v", got, tt.offered)
			}
			i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
			if got := i >= 0; got != tt.notice {
				t.Fatalf("notice sent = %v, want %v", got, tt.notice)
			}
			lines := readLines(t, cfg, evs[0].Session)
			answer := lines[len(lines)-1]
			if !tt.notice {
				if answer.Notice != "" {
					t.Errorf("assistant line notice = %q, want none", answer.Notice)
				}
				return
			}
			want := noToolsText(cfg.Models.Main)
			if evs[i].Text != want || answer.Notice != want {
				t.Errorf("notice = %q, line = %q; want %q", evs[i].Text, answer.Notice, want)
			}
			if !strings.HasPrefix(want, "main-model can't call tools") {
				t.Errorf("the notice doesn't name the model: %q", want)
			}
		})
	}
}
