// This file tests the hint dispatch adds to a call refused for missing
// arguments.

package dispatch

import (
	"strings"
	"testing"
)

func TestRetryHint(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{"one missing argument", `calling "tools/call": invalid params: validating "arguments": validating root: required: missing properties: ["project_id"]`,
			" Call it again with project_id."},
		{"two", `required: missing properties: ["owner", "repo"]`, " Call it again with owner, repo."},
		{"another error", "connection refused", ""},
		{"empty brackets", "missing properties: []", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retryHint(tt.text)
			if tt.want == "" && got != "" || tt.want != "" && !strings.HasPrefix(got, tt.want) {
				t.Errorf("retryHint = %q, want it to start with %q", got, tt.want)
			}
		})
	}
}
