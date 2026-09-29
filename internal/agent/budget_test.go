// This file tests the context budget: the history cap and the order of the
// system prompt's sections.

package agent

import (
	"regexp"
	"strings"
	"testing"
	"time"

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
// Ollama can reuse its work on the opening of the prompt. The note on the
// file tools, which only file turns get, sits between them.
func TestPromptOrder(t *testing.T) {
	a := New(testConfig(t), &fakeEngine{}, &fakeRouter{}, nil, nil, nil, nil, quietLog())
	msgs := a.prompt(t.Context(), nil, "q", sections{
		memories:    "MEMORIES",
		skillList:   "SKILL-LIST",
		skillBodies: "SKILL-BODIES",
		files:       "FILES",
		toolsNote:   toolsNote,
		fileTools:   fileToolsNote,
		web:         "WEB",
	})
	system := msgs[0].Content
	order := []string{whoIsWho, honestyRule, "Today is ", a.filesNote, "Meru's own tools", toolsNote, "SKILL-LIST", fileToolsNote, "MEMORIES", "SKILL-BODIES", "FILES", "WEB", "\n\nThe time now is "}
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
	// The clock changes every minute, so nothing may follow it.
	if !clockLine.MatchString(system) {
		t.Errorf("system prompt doesn't end with the clock line:\n%s", system)
	}
}

func TestToday(t *testing.T) {
	now := time.Date(2026, 9, 24, 15, 4, 0, 0, time.Local)
	got := today(now)
	if !strings.HasPrefix(got, "Today is Thursday, 24 September 2026. ") || !strings.Contains(got, "datetime tool") {
		t.Errorf("today = %q, want the date and a pointer to the datetime tool", got)
	}
	// The date line stays the same all day, so it must not hold the time.
	if strings.Contains(got, "15:04") {
		t.Errorf("today = %q, want no time of day", got)
	}
}

func TestClock(t *testing.T) {
	// FixedZone makes a zone from a name and an offset in seconds, so the
	// test doesn't depend on the zone of the machine it runs on.
	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"evening, four hours behind UTC", time.Date(2026, 9, 28, 20, 2, 59, 0, time.FixedZone("EDT", -4*3600)),
			"The time now is 20:02 EDT (UTC-04:00)."},
		{"morning, five and a half hours ahead", time.Date(2026, 1, 5, 7, 30, 0, 0, time.FixedZone("IST", 5*3600+1800)),
			"The time now is 07:30 IST (UTC+05:30)."},
		{"UTC itself", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			"The time now is 00:00 UTC (UTC+00:00)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clock(tt.now); got != tt.want {
				t.Errorf("clock = %q, want %q", got, tt.want)
			}
		})
	}
}

// clockLine matches the clock line at the very end of a system prompt.
var clockLine = regexp.MustCompile(`\n\nThe time now is \d\d:\d\d \S+ \(UTC[+-]\d\d:\d\d\)\.$`)

// withoutClock returns a system prompt with the clock line cut off its end,
// so a test can compare the rest without racing the minute.
func withoutClock(system string) string {
	return clockLine.ReplaceAllString(system, "")
}
