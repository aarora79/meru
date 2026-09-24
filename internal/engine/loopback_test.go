// This file tests the loopback check that keeps model and telemetry traffic
// on this machine, and the version parser behind the Ollama version gate.

package engine

import "testing"

func TestCheckLoopbackURL(t *testing.T) {
	tests := []struct {
		url    string
		wantOK bool
	}{
		{"http://127.0.0.1:11434", true},
		{"http://127.0.0.1", true},
		{"https://127.255.0.1:4318/v1", true},
		{"http://[::1]:11434", true},
		{"http://[::ffff:127.0.0.1]:11434", true}, // IPv4 loopback written as IPv6
		{"http://localhost:11434", true},
		{"http://LOCALHOST:11434", true},
		{"http://10.0.0.1:11434", false},
		{"http://[::ffff:10.0.0.1]:11434", false},
		{"http://[2001:db8::1]:11434", false},
		{"http://0.0.0.0:11434", false},
		{"http://localhost.example.com:11434", false},
		{"http://ollama.local:11434", false},
		{"ftp://127.0.0.1", false},
		{"127.0.0.1:11434", false}, // no scheme
		{"http://", false},
		{"://bad", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := CheckLoopbackURL(tt.url)
			if (err == nil) != tt.wantOK {
				t.Errorf("CheckLoopbackURL(%q) = %v, want ok=%v", tt.url, err, tt.wantOK)
			}
			if IsLoopbackURL(tt.url) != tt.wantOK {
				t.Errorf("IsLoopbackURL(%q) = %v, want %v", tt.url, !tt.wantOK, tt.wantOK)
			}
		})
	}
}

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
