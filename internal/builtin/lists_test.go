// This file tests what the desktop app's settings need from the built-in
// tools: new [builtin] lists that take effect at once, and a line on what
// each tool does.

package builtin

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
)

// TestSetLists checks that new [builtin] lists take effect at once: a tool
// taken out of tools is no longer offered or run, and one put in confirm
// asks.
func TestSetLists(t *testing.T) {
	tools := New("config.toml", config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	offered := func() []string {
		var names []string
		for _, s := range tools.Tools() {
			names = append(names, s.Name)
		}
		return names
	}
	if !slices.Contains(offered(), WebFetch) || tools.Confirm(WebFetch) != dispatch.ConfirmNever {
		t.Fatalf("before: offered %v, web_fetch confirm %v", offered(), tools.Confirm(WebFetch))
	}
	tools.SetLists(config.Builtin{Tools: []string{DateTime, WebFetch}, Confirm: []string{WebFetch}})
	if got := offered(); !slices.Equal(got, []string{DateTime, WebFetch}) {
		t.Errorf("after: offered %v, want datetime and web_fetch", got)
	}
	if tools.Confirm(WebFetch) != dispatch.ConfirmAsk {
		t.Error("web_fetch doesn't ask after it went into confirm")
	}
	if _, err := tools.Call(context.Background(), Configure, json.RawMessage(`{}`)); err == nil {
		t.Error("configure ran after it left [builtin] tools")
	}
}

// TestSummary checks every built-in has a line for the desktop app.
func TestSummary(t *testing.T) {
	for _, name := range config.BuiltinTools() {
		if Summary(name) == "" {
			t.Errorf("no summary for %s", name)
		}
	}
	if Summary("nope") != "" {
		t.Error("a summary for a tool that isn't built in")
	}
}
