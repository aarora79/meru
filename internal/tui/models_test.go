// This file tests the model sets table and the /model command: the
// table's exact layout, the box /model opens, the switch and the save it
// sends to merud and what the screen says about each, and /usage by model.

package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

func TestModelTable(t *testing.T) {
	tests := []struct {
		name string
		info rpc.ModelsInfo
		want []string
	}{
		{"three sets", *modelsFixture, []string{
			"  MODEL SET    MAIN                      SIZE   THINK     STATE",
			"→ qwen-moe     qwen3.6:35b-a3b-mxfp8    38 GB   off       loaded",
			"  gemma-moe    gemma4:26b-mxfp8         28 GB   off       on disk",
			"  qwen-dense   qwen3.8:27b-mlx              —   off       not pulled",
		}},
		{"a set with a fast model, and none in use", rpc.ModelsInfo{Main: "gemma3:12b", Sets: []rpc.ModelSet{
			{Name: "small", Main: "gemma3:12b", Fast: "gemma3:1b", Bytes: 8_100_000_000, Pulled: true, Loaded: true},
		}}, []string{
			"  MODEL SET   MAIN           SIZE   THINK     STATE",
			"  small       gemma3:12b   8.1 GB   default   loaded",
			"              also fast: gemma3:1b",
			"",
			"No set is in use; gemma3:12b answers now.",
		}},
		{"no sets", rpc.ModelsInfo{Main: "m"}, []string{noSets}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ModelTable(tt.info)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("ModelTable =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestDiskSize(t *testing.T) {
	for n, want := range map[int64]string{
		512: "512 B", 274_000_000: "274 MB", 8_149_190_253: "8.1 GB", 38_000_000_000: "38 GB", 999_600_000: "1.0 GB",
	} {
		if got := DiskSize(n); got != want {
			t.Errorf("DiskSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSlashModelOpensBox(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventModels, Models: modelsFixture}, {Type: rpc.EventDone}}}
	m, cmd := update(t, testModel(merud.ask, newFakeSender()), typeText("/model"), press(tea.KeyEnter))
	if m.modelBox == nil || !m.modelBox.loading || cmd == nil {
		t.Fatalf("model box = %+v after /model, want it open and asking", m.modelBox)
	}
	m, _ = update(t, m, cmd())
	if len(merud.reqs) != 1 || merud.reqs[0].Op != rpc.OpModels {
		t.Errorf("requests = %+v, want one models request", merud.reqs)
	}
	if len(m.turns) != 0 || m.input.Value() != "" {
		t.Errorf("turns = %d, input = %q; /model must not ask the model", len(m.turns), m.input.Value())
	}
	view := m.View()
	for _, want := range []string{"Model sets", "→ qwen-moe", "not pulled", "/model save", "esc/q close"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	// The reply also tells the header which set is in use.
	if m.activeSet() != "qwen-moe" || !strings.Contains(m.header(), "qwen-moe") {
		t.Errorf("header = %q, want it to name qwen-moe", m.header())
	}
	m, _ = update(t, m, typeText("q"))
	if m.modelBox != nil {
		t.Error("q didn't close the /model box")
	}
}

// TestSlashModelSwitch checks /model <name>: it sends OpModelUse, says on
// the notice line what it does, and when merud answers, names the set and
// merud's warning, and puts the new set in the header.
func TestSlashModelSwitch(t *testing.T) {
	after := *modelsFixture
	after.Active, after.Main = "gemma-moe", "gemma4:26b-mxfp8"
	after.Warning = "Meru can't use tools with this model."
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventModels, Models: &after}, {Type: rpc.EventDone}}}
	m, cmd := update(t, testModel(merud.ask, newFakeSender()), typeText("/model gemma-moe"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "switching to gemma-moe") || cmd == nil {
		t.Fatalf("notice = %q, want the switch under way", m.notice)
	}
	m, _ = update(t, m, cmd())
	if len(merud.reqs) != 1 || merud.reqs[0] != (rpc.Request{Op: rpc.OpModelUse, ID: "gemma-moe"}) {
		t.Errorf("requests = %+v, want one model_use for gemma-moe", merud.reqs)
	}
	if want := "gemma-moe answers now with gemma4:26b-mxfp8 · Meru can't use tools"; !strings.HasPrefix(m.notice, want) {
		t.Errorf("notice = %q, want it to start %q", m.notice, want)
	}
	if h := m.header(); !strings.Contains(h, "gemma-moe") || !strings.Contains(h, "gemma4:26b-mxfp8") {
		t.Errorf("header = %q, want the new set and model", h)
	}
}

func TestSlashModelArguments(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   *rpc.Request // nil for no request
		notice string
	}{
		{"save", "/model save", &rpc.Request{Op: rpc.OpModelSave}, "saving"},
		{"rebuild", "/model new-embed --rebuild", &rpc.Request{Op: rpc.OpModelUse, ID: "new-embed", Rebuild: true}, "switching"},
		{"a stray word", "/model gemma-moe now", nil, "--rebuild after it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventModels, Models: modelsFixture}, {Type: rpc.EventDone}}}
			m, cmd := update(t, testModel(merud.ask, newFakeSender()), typeText(tt.line), press(tea.KeyEnter))
			if !strings.Contains(m.notice, tt.notice) {
				t.Errorf("notice = %q, want it to say %q", m.notice, tt.notice)
			}
			if tt.want == nil {
				if cmd != nil {
					cmd()
				}
				if len(merud.reqs) != 0 {
					t.Errorf("requests = %+v, want none", merud.reqs)
				}
				return
			}
			m, _ = update(t, m, cmd())
			if len(merud.reqs) != 1 || merud.reqs[0] != *tt.want {
				t.Errorf("requests = %+v, want %+v", merud.reqs, *tt.want)
			}
			if tt.name == "save" && !strings.Contains(m.notice, "saved: merud starts with qwen-moe (qwen3.6:35b-a3b-mxfp8)") {
				t.Errorf("notice after the save = %q", m.notice)
			}
		})
	}
}

// TestSlashModelWaitsForTheTurn checks that a switch while an answer
// streams sends nothing and says why.
func TestSlashModelWaitsForTheTurn(t *testing.T) {
	merud := &fakeMerud{block: true}
	m, _ := update(t, testModel(merud.ask, newFakeSender()), typeText("hello"), press(tea.KeyEnter))
	m, cmd := update(t, m, typeText("/model gemma-moe"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "wait for the answer to finish") || cmd != nil {
		t.Errorf("notice = %q, cmd %v; want the switch refused", m.notice, cmd)
	}
	m.stopTurn()
}

// TestSlashModelFails checks that a failed switch says why on the notice
// line and leaves the header as it was.
func TestSlashModelFails(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, modelsMsg{action: actionList, info: modelsFixture},
		modelsMsg{action: actionUse, name: "qwen-dense", err: errors.New("switch to qwen3.8:27b-mlx: Ollama doesn't have it yet")})
	if !strings.HasPrefix(m.notice, "couldn't switch to qwen-dense: switch to qwen3.8:27b-mlx") {
		t.Errorf("notice = %q", m.notice)
	}
	if m.activeSet() != "qwen-moe" {
		t.Errorf("set = %q after a failed switch, want qwen-moe", m.activeSet())
	}
}

// TestUsageByModel checks /usage by model: it asks merud for the windows
// by model, shows them in the box, and leaves the header's last-hour
// numbers alone.
func TestUsageByModel(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventUsage, Usage: byModelFixture}, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, _ = update(t, m, usageMsg{windows: usageFixture, answered: true})
	m, cmd := update(t, m, typeText("/usage by model"), press(tea.KeyEnter))
	if m.usageBox == nil || !m.usageBox.byModel {
		t.Fatalf("usage box = %+v, want it open by model", m.usageBox)
	}
	m, _ = update(t, m, cmd())
	if len(merud.reqs) != 1 || merud.reqs[0] != (rpc.Request{Op: rpc.OpUsage, Kind: rpc.UsageByModel}) {
		t.Errorf("requests = %+v, want usage by model", merud.reqs)
	}
	if lastHour(m.usage) == "" {
		t.Error("the reply by model replaced the header's windows")
	}
	if view := m.View(); !strings.Contains(view, "Usage by model") || !strings.Contains(view, "BAD CALLS") {
		t.Errorf("view lacks the table:\n%s", view)
	}
	// A plain usage reply doesn't fill a box that waits by model.
	m, _ = update(t, m, typeText("q"), typeText("/usage by model"), press(tea.KeyEnter),
		usageMsg{windows: usageFixture, answered: true})
	if !m.usageBox.loading {
		t.Error("a plain usage reply filled the box by model")
	}
	m, _ = update(t, m, typeText("q"), typeText("/usage for me"), press(tea.KeyEnter))
	if m.usageBox != nil || !strings.Contains(m.notice, "by model") {
		t.Errorf("/usage for me: box %+v, notice %q", m.usageBox, m.notice)
	}
}
