//go:build e2e

// This file tests schedule_job end to end: the model asks to schedule a
// job, the approval card shows the schedule in words, and the job the
// user approves lands in config.toml and in the jobs list.

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

func TestScheduleJob(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	s.fake.enqueue(t, fastModel, toolsRoute())
	s.fake.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "schedule_job", Arguments: map[string]any{
			"name": "morning-brief", "schedule": "0 7 * * 1-5", "prompt": "Brief me on today's calendar.",
		}}}},
		fakeollama.Reply{Text: "Done: a brief at 7:00 on weekdays."})

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var card *rpc.Approval
	approve := func(_ context.Context, a rpc.Approval) (rpc.Choice, error) {
		card = &a
		return rpc.ChoiceOnce, nil
	}
	for ev, err := range rpc.Do(ctx, s.home.socket, rpc.Request{Op: rpc.OpAsk, Text: "every weekday at 7, brief me on my calendar"}, approve) {
		if err != nil {
			t.Fatalf("ask: %v\nmerud.log:\n%s", err, s.home.log())
		}
		if ev.Type == rpc.EventError {
			t.Fatalf("ask: %s", ev.Error)
		}
	}
	if card == nil || card.Name != "schedule_job" || !strings.Contains(string(card.Args), "7:00 on weekdays") {
		t.Fatalf("approval card = %+v", card)
	}
	for _, c := range card.Choices {
		if c == rpc.ChoiceSession {
			t.Errorf("schedule_job offered a session approval: %v", card.Choices)
		}
	}

	jobs := eventOfType(t, op(t, s.home.socket, rpc.Request{Op: rpc.OpJobs}), rpc.EventJobs).Jobs
	if len(jobs) != 1 || jobs[0].Name != "morning-brief" || jobs[0].When != "7:00 on weekdays" {
		t.Errorf("jobs = %+v", jobs)
	}
	b, err := os.ReadFile(s.home.config)
	if err != nil || !strings.Contains(string(b), `name        = "morning-brief"`) {
		t.Errorf("config.toml lacks the job (%v):\n%s", err, b)
	}
}

// eventOfType returns the first event of type typ in evs, or fails.
func eventOfType(t *testing.T, evs []rpc.Event, typ rpc.EventType) rpc.Event {
	t.Helper()
	for _, ev := range evs {
		if ev.Type == typ {
			return ev
		}
	}
	t.Fatalf("no %s event in %+v", typ, evs)
	return rpc.Event{}
}
