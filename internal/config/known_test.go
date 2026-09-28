// This file tests the list of models we tried as the answer model.

package config

import (
	"slices"
	"testing"
)

// TestKnownModels checks that each known model has every field the
// Settings shows, that the names are the ones we tried, smallest first,
// that each profile's main model is on the list, and that a
// caller's change to the list doesn't reach the next caller.
func TestKnownModels(t *testing.T) {
	list := KnownModels()
	var names []string
	for _, m := range list {
		names = append(names, m.Name)
		if m.Label == "" || m.Size == "" || m.Good == "" || m.Bad == "" {
			t.Errorf("%s: a field is empty: %+v", m.Name, m)
		}
		if !slices.Contains(m.Capabilities, "completion") {
			t.Errorf("%s: capabilities %v lack completion", m.Name, m.Capabilities)
		}
	}
	want := []string{"hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M", "gemma3:12b", "gemma4:26b-a4b-it-qat",
		"qwen3.6:35b", "gemma4:26b-mxfp8", "qwen3.6:35b-a3b-mxfp8"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	// Each profile's main model must stay on the list, so Settings can
	// switch back to it.
	for _, p := range []string{"lite", "full"} {
		if !slices.Contains(names, profiles[p].Main) {
			t.Errorf("the %s main model %q isn't known", p, profiles[p].Main)
		}
	}

	list[0].Name = "changed"
	if KnownModels()[0].Name == "changed" {
		t.Error("a change to one list reached the next")
	}
}

// TestFindKnownModel checks the lookup by name, for known models and for
// names that aren't known.
func TestFindKnownModel(t *testing.T) {
	tests := []struct {
		name  string
		found bool
		tools bool
	}{
		{"qwen3.6:35b", true, true},
		{"qwen3.6:35b-a3b-mxfp8", true, true},
		{"gemma4:26b-mxfp8", true, true},
		{"gemma4:26b-a4b-it-qat", true, true},
		{"gemma3:12b", true, false},
		{"hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M", true, true},
		{"qwen3.8:27b", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := FindKnownModel(tt.name)
			if ok != tt.found {
				t.Fatalf("found = %v, want %v", ok, tt.found)
			}
			if got := slices.Contains(m.Capabilities, "tools"); got != tt.tools {
				t.Errorf("tools = %v, want %v", got, tt.tools)
			}
		})
	}
}
