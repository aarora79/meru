// Tests for settings.go: the words for a tool's MCP hints.

package rpc

import "testing"

func TestHintOf(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name                  string
		readOnly, destructive *bool
		want                  string
	}{
		{"no annotations", nil, nil, ""},
		{"read-only", &yes, nil, HintReadOnly},
		{"read-only wins over destructive", &yes, &yes, HintReadOnly},
		{"destructive", &no, &yes, HintMayDelete},
		{"annotated, neither", &no, nil, HintChangesThings},
		{"annotated, not destructive", &no, &no, HintChangesThings},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HintOf(tc.readOnly, tc.destructive); got != tc.want {
				t.Errorf("HintOf = %q, want %q", got, tc.want)
			}
		})
	}
}
