// This file tests the version parser behind the Ollama version gate.

package engine

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    [3]int
		wantErr bool
	}{
		{"0.12.11", [3]int{0, 12, 11}, false},
		{"v0.34.0", [3]int{0, 34, 0}, false},
		{"0.13.0-rc1", [3]int{0, 13, 0}, false},
		{"1.2", [3]int{1, 2, 0}, false},
		{"1.2.3+dirty", [3]int{1, 2, 3}, false},
		{"", [3]int{}, true},
		{"1.2.3.4", [3]int{}, true},
		{"1.x.3", [3]int{}, true},
		{"1.-2.3", [3]int{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseVersion(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("parseVersion(%q) = %v, %v; want %v, err=%v", tt.in, got, err, tt.want, tt.wantErr)
			}
		})
	}
}
