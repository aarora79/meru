// This file tests a "search" turn that reads a whole file: the fake engine
// calls read_file, the real built-in tools answer through dispatch, and
// the model's next round reads the file's text.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

func TestSearchTurnReadsAWholeFile(t *testing.T) {
	cfg := testConfig(t)
	folder := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "accounts.md"), []byte("# Accounts\n\nOrchard Co\nRiver Mill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Index.Folders = []string{folder}
	ix, err := index.New(cfg.Index, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{}, nil, "", ix, nil, nil)
	disp := dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{})

	args, _ := json.Marshal(map[string]string{"path": "accounts.md"})
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{{Name: builtin.ReadFile, Arguments: args}}},
		{pieces: []string{"Two accounts: Orchard Co and River Mill."}},
	}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}}, nil, disp, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "list the accounts in accounts.md"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var result *rpc.ToolEvent
	for _, ev := range evs {
		if ev.Type == rpc.EventToolResult {
			result = ev.Tool
		}
	}
	if result == nil || result.Name != builtin.ReadFile || result.Kind != dispatch.KindBuiltin || result.Outcome != dispatch.OutcomeOK {
		t.Fatalf("tool_result = %+v, want read_file, builtin, ok", result)
	}

	// The first round offered only the three file tools.
	var offered []string
	for _, s := range eng.calls[0].tools {
		offered = append(offered, s.Name)
	}
	if strings.Join(offered, " ") != "read_file list_folder grep" {
		t.Errorf("offered %v, want the three file tools", offered)
	}
	// The second round reads the whole file.
	msgs := eng.lastCall().msgs
	last := msgs[len(msgs)-1]
	if last.Role != engine.RoleTool || !strings.Contains(last.Content, "Orchard Co\nRiver Mill") {
		t.Errorf("last message = %+v, want read_file's result with the file's text", last)
	}
}
