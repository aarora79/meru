// This file tests the Bridge's job calls against a fake merud.

package desktop

import (
	"context"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestJobsBridge(t *testing.T) {
	var got []rpc.Request
	brief := rpc.JobInfo{Name: "morning-brief", When: "7:00 on weekdays"}
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
	want := []rpc.Op{rpc.OpJobs, rpc.OpJobRun, rpc.OpJobCancel}
	if len(got) != 3 {
		t.Fatalf("requests = %+v", got)
	}
	for i, op := range want {
		if got[i].Op != op || (i > 0 && got[i].ID != "morning-brief") {
			t.Errorf("request %d = %+v, want %s", i, got[i], op)
		}
	}
}
