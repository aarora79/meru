// This file tests `meru model` against an in-process rpc server that plays
// merud: the table, the switch and the save, and the words it refuses.

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// modelsFixture is merud's models reply with the three sets of the
// comparison, the first in use.
var modelsFixture = rpc.ModelsInfo{
	Main: "qwen3.6:35b-a3b-mxfp8", Active: "qwen-moe",
	Sets: []rpc.ModelSet{
		{Name: "qwen-moe", Main: "qwen3.6:35b-a3b-mxfp8", ThinkOff: true, Bytes: 38_000_000_000, Pulled: true, Loaded: true, Active: true},
		{Name: "gemma-moe", Main: "gemma4:26b-mxfp8", ThinkOff: true, Bytes: 28_000_000_000, Pulled: true},
		{Name: "qwen-dense", Main: "qwen3.8:27b-mlx", ThinkOff: true},
	},
}

func TestModelCommand(t *testing.T) {
	table := `  MODEL SET    MAIN                      SIZE   THINK     STATE
→ qwen-moe     qwen3.6:35b-a3b-mxfp8    38 GB   off       loaded
  gemma-moe    gemma4:26b-mxfp8         28 GB   off       on disk
  qwen-dense   qwen3.8:27b-mlx              —   off       not pulled

` + modelNote + "\n"
	switched := modelsFixture
	switched.Main, switched.Active = "gemma4:26b-mxfp8", "gemma-moe"
	switched.Warning = "merud moves to the fast model gemma3:1b only after you save the set and restart merud."
	tests := []struct {
		name     string
		args     []string
		reply    rpc.ModelsInfo
		refuse   string
		wantReq  rpc.Request
		wantCode int
		wantOut  string // all of stdout, or for a switch, a piece of it
		wantErr  string
	}{
		{"table", []string{"model"}, modelsFixture, "", rpc.Request{Op: rpc.OpModels}, 0, table, ""},
		{"switch", []string{"model", "use", "gemma-moe"}, switched, "", rpc.Request{Op: rpc.OpModelUse, ID: "gemma-moe"}, 0,
			"gemma-moe answers now with gemma4:26b-mxfp8.\nnote: merud moves to the fast model", ""},
		{"switch with rebuild", []string{"model", "use", "new-embed", "--rebuild"}, switched, "",
			rpc.Request{Op: rpc.OpModelUse, ID: "new-embed", Rebuild: true}, 0, "answers now", ""},
		{"save", []string{"model", "save"}, modelsFixture, "", rpc.Request{Op: rpc.OpModelSave}, 0,
			"Saved: merud starts with qwen3.6:35b-a3b-mxfp8.", ""},
		{"merud refuses", []string{"model", "use", "qwen-dense"}, rpc.ModelsInfo{},
			"switch to qwen3.8:27b-mlx: Ollama doesn't have it yet; fetch it first with: ollama pull qwen3.8:27b-mlx",
			rpc.Request{Op: rpc.OpModelUse, ID: "qwen-dense"}, 1, "", "meru: switch to qwen3.8:27b-mlx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []rpc.Request
			sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
				got = append(got, req)
				if tt.refuse != "" {
					return emit(rpc.Event{Type: rpc.EventError, Error: tt.refuse})
				}
				reply := tt.reply
				return emit(rpc.Event{Type: rpc.EventModels, Models: &reply})
			})
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if len(got) != 1 || got[0] != tt.wantReq {
				t.Errorf("requests = %+v, want %+v", got, tt.wantReq)
			}
			if tt.name == "table" && out.String() != tt.wantOut {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.wantOut)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}

// TestModelCommandUsage checks that words `meru model` doesn't take fail
// before any request goes to merud.
func TestModelCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"model", "use"}, {"model", "use", "a", "b"}, {"model", "saved"}} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), append([]string{"-socket", "/nonexistent.sock"}, args...), &out, &errOut)
		if code != exitError || !strings.Contains(errOut.String(), modelUsage) {
			t.Errorf("%v: code %d, stderr %q; want the usage line", args, code, errOut.String())
		}
	}
}
