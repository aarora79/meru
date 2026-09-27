// This file replays the turn that led to about_meru: asked "which model
// are you using", on a question the router sent direct, the model must be
// able to call about_meru, and the tool's answer must reach its next round.

package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

func TestAboutMeruOnDirect(t *testing.T) {
	cfg := ollamaConfig(t)
	cfg.Models.Main = "qwen3.6:35b"
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: builtin.AboutMeru}}},
		fakeollama.Reply{Text: "I'm qwen3.6:35b, running in Ollama on your computer."})

	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()},
		config.Web{}, nil, "", nil, nil, nil)
	tools.UseAbout(func(context.Context) builtin.About {
		return builtin.About{Profile: "lite", Main: cfg.Models.Main, Fast: cfg.Models.Fast, Embed: cfg.Models.Embed}
	})
	a := ollamaAgent(t, cfg, srv, "direct", dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{}))

	evs, err := run(context.Background(), a, rpc.Request{Text: "which model are you using"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	bodies := chatBodies(t, srv, cfg.Models.Main)
	if len(bodies) != 2 {
		t.Fatalf("main-model calls = %d, want 2", len(bodies))
	}
	var offered []string
	for _, tl := range bodies[0].Tools {
		offered = append(offered, tl.Function.Name)
	}
	if !slices.Equal(offered, []string{builtin.DateTime, builtin.AboutMeru}) {
		t.Errorf("a direct turn offered %v, want datetime and about_meru", offered)
	}
	if system := bodies[0].Messages[0].Content; !strings.Contains(system, selfNote) {
		t.Errorf("the system prompt lacks the line that points at about_meru:\n%s", system)
	}
	// The second round's last message is the tool's answer.
	m := bodies[1].Messages
	result := m[len(m)-1]
	if result.Role != "tool" || !strings.Contains(result.Content, "Answer model (main): qwen3.6:35b.") {
		t.Errorf("round 2 reads %s %q, want about_meru's answer", result.Role, result.Content)
	}
	if got := answerOf(evs); !strings.Contains(got, "qwen3.6:35b") {
		t.Errorf("answer = %q", got)
	}
}
