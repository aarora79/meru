// This file tests schedule_job: the job it hands merud, the words the
// approval card shows, what it refuses, and that it always asks.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
)

func TestScheduleJob(t *testing.T) {
	var saved []config.Job
	tools := New("", config.Builtin{Tools: []string{ScheduleJob}}, config.Web{}, nil, "", nil, nil, nil)
	if tools.Confirm(ScheduleJob) != dispatch.ConfirmAlways {
		t.Error("schedule_job doesn't always ask")
	}
	if len(tools.Tools()) != 0 {
		t.Error("schedule_job offered with no scheduler")
	}
	tools.UseJobs(func(_ context.Context, j config.Job) error {
		if j.Name == "taken" {
			return errors.New(`a job named "taken" exists already`)
		}
		saved = append(saved, j)
		return nil
	})
	tools.now = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local) }

	args := `{"name":"go-news","schedule":"0 9 * * 2","every_weeks":2,"from":"2026-10-27","prompt":"Summarize the Go release notes.","skill":"web-research"}`
	// The approval card shows the schedule in words.
	audit := tools.AuditArgs(ScheduleJob, json.RawMessage(args))
	var shown map[string]any
	if err := json.Unmarshal(audit, &shown); err != nil {
		t.Fatal(err)
	}
	if w, _ := shown["when"].(string); !strings.Contains(w, "every other week") || shown["name"] != "go-news" {
		t.Errorf("audit = %s", audit)
	}
	if tools.AuditArgs(DateTime, json.RawMessage(`{}`)) != nil {
		t.Error("AuditArgs changed another tool's arguments")
	}

	res, err := tools.Call(context.Background(), ScheduleJob, json.RawMessage(args))
	if err != nil || res.IsError || !strings.Contains(res.Text, "Jobs folder") || !strings.Contains(res.Text, "next run Tue 27 Oct 2026 09:00") {
		t.Fatalf("Call = %+v, %v", res, err)
	}
	want := config.Job{Name: "go-news", Schedule: "0 9 * * 2", EveryWeeks: 2, From: "2026-10-27",
		Prompt: "Summarize the Go release notes.", Skill: "web-research"}
	if len(saved) != 1 || saved[0].Name != want.Name || saved[0].EveryWeeks != 2 || saved[0].Skill != want.Skill || len(saved[0].Allow) != 0 {
		t.Errorf("saved %+v", saved)
	}

	// Each refusal reaches the model as an error result, and saves nothing.
	for _, tt := range []struct{ args, want string }{
		{`{"name":"x","schedule":"0 25 * * *","prompt":"p"}`, "Nothing was saved"},
		{`{"schedule":"0 7 * * *","prompt":"p"}`, "give the job a name"},
		{`{"name":"x","schedule":"0 7 * * *","prompt":"p","allow":["write_file"]}`, "valid JSON object"},
		{`{"name":"taken","schedule":"0 7 * * *","prompt":"p"}`, "exists already"},
	} {
		res, err := tools.Call(context.Background(), ScheduleJob, json.RawMessage(tt.args))
		if err != nil || !res.IsError || !strings.Contains(res.Text, tt.want) {
			t.Errorf("%s: %+v, %v; want an error with %q", tt.args, res, err, tt.want)
		}
	}
	if len(saved) != 1 {
		t.Errorf("a refused call saved: %+v", saved)
	}
}
