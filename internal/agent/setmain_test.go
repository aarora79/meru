// This file tests SetMain: after the desktop app switches the answer
// model, the next turn answers with the new one, with no restart.

package agent

import (
	"context"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

func TestSetMain(t *testing.T) {
	cfg := ollamaConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	a := ollamaAgent(t, cfg, srv, "direct", nil)
	if got := a.Main(); got != "main-model" {
		t.Fatalf("Main = %q, want config's main-model", got)
	}

	tests := []struct {
		name string
		set  string // "" leaves the model as it is
		want string
	}{
		{"config's model", "", "main-model"},
		{"after a switch", "gemma3:12b", "gemma3:12b"},
		{"and back", "main-model", "main-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set != "" {
				a.SetMain(tt.set)
			}
			before := len(chatBodies(t, srv, tt.want))
			if _, err := run(context.Background(), a, rpc.Request{Text: "hello"}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := len(chatBodies(t, srv, tt.want)); got != before+1 {
				t.Errorf("%s answered %d times in this turn, want once", tt.want, got-before)
			}
			if got := a.Main(); got != tt.want {
				t.Errorf("Main = %q, want %q", got, tt.want)
			}
		})
	}
}
