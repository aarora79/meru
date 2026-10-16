// This file tests the Ollama version floor: the version parser, the
// comparison, and the sentence every client shows.

package config

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    [3]int
		wantErr bool
	}{
		{"0.40.5", [3]int{0, 40, 5}, false},
		{"v0.34.0", [3]int{0, 34, 0}, false},
		{"0.13.0-rc1", [3]int{0, 13, 0}, false},
		{" 1.2 ", [3]int{1, 2, 0}, false},
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

func TestOllamaAtLeast(t *testing.T) {
	tests := []struct {
		have    string
		want    bool
		wantErr bool
	}{
		{"0.40.5", true, false},
		{"v0.40.5", true, false},
		{"0.40.5-rc1", true, false},
		{"0.40.6", true, false},
		{"0.41", true, false},
		{"1.0.0", true, false},
		{"0.40.4", false, false},
		{"0.34.0", false, false},
		{"0.12.11", false, false},
		{"dev", false, true},
		{"0.40.5.1", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.have, func(t *testing.T) {
			got, err := OllamaAtLeast(tt.have)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("OllamaAtLeast(%q) = %v, %v; want %v, err=%v", tt.have, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestOllamaTooOld(t *testing.T) {
	want := "Ollama 0.34.0 is too old for Meru's search model. Update it from ollama.com/download."
	if got := OllamaTooOld("0.34.0"); got != want {
		t.Errorf("OllamaTooOld = %q, want %q", got, want)
	}
}
