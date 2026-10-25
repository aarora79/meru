// This file tests the Bridge's job calls against a fake merud.

package desktop

import (
	"context"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestJobsBridge(t *testing.T) {
	var got []rpc.Request
	brief := rpc.JobInfo{JobSpec: rpc.JobSpec{Name: "morning-brief"}, When: "7:00 on weekdays"}
	b, _ := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		got = append(got, req)
		return emit(rpc.Event{Type: rpc.EventJobs, Jobs: []rpc.JobInfo{brief}})
	}))
	defer b.ServiceShutdown()
	ctx := context.Background()

	list, err := b.Jobs(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "morning-brief" {
		t.Fatalf("Jobs = %+v, %v", list, err)
	}
	if _, err := b.RunJob(ctx, "morning-brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CancelJob(ctx, "morning-brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RunJob(ctx, ""); err == nil {
		t.Error("RunJob with no name went to merud")
	}
	if _, err := b.RemoveJob(ctx, "morning-brief"); err != nil {
		t.Fatal(err)
	}
	want := []rpc.Op{rpc.OpJobs, rpc.OpJobRun, rpc.OpJobCancel, rpc.OpJobRemove}
	if len(got) != len(want) {
		t.Fatalf("requests = %+v", got)
	}
	for i, op := range want {
		if got[i].Op != op || (i > 0 && got[i].ID != "morning-brief") {
			t.Errorf("request %d = %+v, want %s", i, got[i], op)
		}
	}

	// A save carries the spec and the old name; a check is a dry run.
	got = nil
	spec := rpc.JobSpec{Name: "brief", Schedule: "0 7 * * 1-5", Prompt: "Brief me.", Paused: true}
	if _, err := b.SaveJob(ctx, "morning-brief", spec); err != nil {
		t.Fatal(err)
	}
	if j, err := b.CheckJob(ctx, "", spec); err != nil || j.When != "7:00 on weekdays" {
		t.Fatalf("CheckJob = %+v, %v", j, err)
	}
	if len(got) != 2 || got[0].Op != rpc.OpJobSet || got[0].ID != "morning-brief" || got[0].JobSpec == nil ||
		got[0].JobSpec.Name != "brief" || !got[0].JobSpec.Paused || got[0].DryRun || !got[1].DryRun {
		t.Errorf("save and check = %+v", got)
	}
}
