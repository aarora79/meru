// This file tests the context budget: the history cap and the order of the
// system prompt's sections.

package agent

import (
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
)

func TestTrimHistory(t *testing.T) {
	msg := func(role engine.Role, n int) engine.Message {
		return engine.Message{Role: role, Content: strings.Repeat("x", n)}
	}
	u, a := engine.RoleUser, engine.RoleAssistant
	tests := []struct {
		name        string
		history     []engine.Message
		max         int
		wantKept    int
		wantDropped int
	}{
		{"fits", []engine.Message{msg(u, 10), msg(a, 10)}, 100, 2, 0},
		{"drops the oldest pair", []engine.Message{msg(u, 50), msg(a, 50), msg(u, 10), msg(a, 10)}, 40, 2, 2},
		{"drops pairs until it fits", []engine.Message{msg(u, 30), msg(a, 30), msg(u, 30), msg(a, 30), msg(u, 5), msg(a, 5)}, 20, 2, 4},
		{"a turn too long for the cap goes too", []engine.Message{msg(u, 10), msg(a, 500)}, 100, 0, 2},
		{"empty", nil, 100, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, dropped := trimHistory(tt.history, tt.max)
			if len(kept) != tt.wantKept || dropped != tt.wantDropped {
				t.Fatalf("kept %d, dropped %d; want %d and %d", len(kept), dropped, tt.wantKept, tt.wantDropped)
			}
			if len(kept) > 0 && kept[0].Role != engine.RoleUser {
				t.Errorf("history starts with a %s message; want the user's", kept[0].Role)
			}
		})
	}
}

// TestPromptOrder checks that the parts of the system prompt that stay the
// same from turn to turn come before the parts each question changes, so
// Ollama can reuse its work on the opening of the prompt.
func TestPromptOrder(t *testing.T) {
	a := New(testConfig(t), &fakeEngine{}, &fakeRouter{}, nil, nil, nil, nil, quietLog())
	msgs := a.prompt(t.Context(), nil, "q", sections{
		memories:    "MEMORIES",
		skillList:   "SKILL-LIST",
		skillBodies: "SKILL-BODIES",
		files:       "FILES",
		tools:       true,
	})
	system := msgs[0].Content
	order := []string{whoIsWho, a.filesNote, toolsNote, "SKILL-LIST", "MEMORIES", "SKILL-BODIES", "FILES"}
	last := -1
	for _, part := range order {
		i := strings.Index(system, part)
		if i < 0 {
			t.Fatalf("system prompt lacks %q:\n%s", part, system)
		}
		if i < last {
			t.Errorf("%q comes too early; want the order %q", part, order)
		}
		last = i
	}
}
