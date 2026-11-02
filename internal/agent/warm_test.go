// This file tests the answer model's warm-up: a local model gets the
// warm-up prompt, and an endpoint's model gets nothing, since that prompt
// holds the profile and memories.

package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aarora79/meru/internal/engine"
)

// generateCounter is a fakeEngine that counts Generate calls. Embedding
// fakeEngine (naming its type with no field name) gives generateCounter
// all of fakeEngine's methods; the Generate below replaces that one.
type generateCounter struct {
	fakeEngine
	n atomic.Int32
}

func (g *generateCounter) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	g.n.Add(1)
	return engine.Completion{}, nil
}

func TestWarmSkipsAnEndpoint(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int32
	}{
		{"qwen3.6:35b", 1},
		{"@lab", 0},
	} {
		eng := &generateCounter{}
		a := New(testConfig(t), eng, &fakeRouter{}, nil, nil, nil, nil, quietLog())
		a.SetMain(tc.model, true)
		a.StartWarm()(context.Background())
		if got := eng.n.Load(); got != tc.want {
			t.Errorf("warming %s made %d calls, want %d", tc.model, got, tc.want)
		}
	}
}
