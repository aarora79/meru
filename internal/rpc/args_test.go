// This file tests the helpers that show a tool call's arguments.

package rpc

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestArgsLines(t *testing.T) {
	tests := []struct {
		name string
		args string
		max  int
		want []string
	}{
		{"empty", "", 5, nil},
		{"indented", `{"query":"garden","limit":3}`, 5, []string{"{", `  "query": "garden",`, `  "limit": 3`, "}"}},
		{"cut", `{"a":1,"b":2,"c":3,"d":4}`, 4, []string{"{", `  "a": 1,`, `  "b": 2,`, "… 3 more lines"}},
		{"not JSON", "plain words", 5, []string{"plain words"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ArgsLines(json.RawMessage(tt.args), tt.max); !slices.Equal(got, tt.want) {
				t.Errorf("ArgsLines = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArgsLine(t *testing.T) {
	tests := []struct {
		name  string
		args  string
		width int
		want  string
	}{
		{"empty", "", 20, ""},
		{"compact", "{\n  \"query\": \"garden\"\n}", 40, `{"query":"garden"}`},
		{"cut", `{"query":"a long question about the garden"}`, 16, `{"query":"a lon…`},
		{"not JSON keeps one line", "one\ntwo", 20, "one two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ArgsLine(json.RawMessage(tt.args), tt.width); got != tt.want {
				t.Errorf("ArgsLine = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArgvLine(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"git", "-C", "/home/sam/repos/meru", "log", "--since={since}"}, "git -C /home/sam/repos/meru log --since={since}"},
		{[]string{"rg", "--", "two words", ""}, `rg -- "two words" ""`},
		{[]string{"echo", `say "hi"`, `a\b`, "it's", "tab\there"}, `echo "say \"hi\"" "a\\b" "it's" "tab\there"`},
	}
	for _, tt := range tests {
		if got := ArgvLine(tt.argv); got != tt.want {
			t.Errorf("ArgvLine(%q) = %s, want %s", tt.argv, got, tt.want)
		}
	}
}

func TestCut(t *testing.T) {
	if got := Cut("café au lait", 5); got != "café…" {
		t.Errorf("Cut = %q, want %q", got, "café…")
	}
	if got := Cut("short", 10); got != "short" {
		t.Errorf("Cut = %q, want it unchanged", got)
	}
}
