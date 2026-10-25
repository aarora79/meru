// This file tests a scheduled job's run through Handle: the run lands in
// the Jobs folder with the job on its user line and turns row, and a named
// skill loads without the pick call.

package agent

import (
	"context"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// turnRows keeps the turn rows Handle writes.
type turnRows struct{ rows []store.Turn }

func (r *turnRows) InsertTurn(_ context.Context, t store.Turn) error {
	r.rows = append(r.rows, t)
	return nil
}

func TestJobRun(t *testing.T) {
	cfg := testConfig(t)
	// The picker would choose nothing; the job names explainer instead.
	eng := &pickEngine{fakeEngine: &fakeEngine{pieces: []string{"Here is your brief."}}, answer: func(string) string { return "none" }}
	rows := &turnRows{}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, rows, nil, quietLog())
	reg := builtinRegistry(t)
	a.UseSkills(fixedSkills{reg: reg})

	evs, err := run(context.Background(), a, rpc.Request{Text: "Brief me.", Source: rpc.SourceJob, Job: "morning-brief", Skill: "explainer"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if gens := eng.generates(); len(gens) != 0 {
		t.Errorf("the pick ran %d times; a named skill skips it", len(gens))
	}
	var route rpc.Event
	for _, ev := range evs {
		if ev.Type == rpc.EventRoute {
			route = ev
		}
	}
	if len(route.Skills) != 1 || route.Skills[0].Name != "explainer" {
		t.Errorf("route event skills = %+v, want [explainer]", route.Skills)
	}

	lines := allLines(t, cfg, evs[0].Session)
	if meta := transcript.MetaOf(lines); meta.Folder != config.JobsFolder {
		t.Errorf("folder = %q, want %q", meta.Folder, config.JobsFolder)
	}
	var user transcript.Line
	for _, l := range lines {
		if l.Type == transcript.TypeUser {
			user = l
		}
	}
	if user.Job != "morning-brief" {
		t.Errorf("user line job = %q", user.Job)
	}
	if len(rows.rows) != 1 || rows.rows[0].Job != "morning-brief" || rows.rows[0].Source != string(rpc.SourceJob) {
		t.Errorf("turn rows = %+v", rows.rows)
	}
}
