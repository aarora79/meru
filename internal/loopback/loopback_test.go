// This file tests the loopback check that keeps model and telemetry traffic
// on this machine.

package loopback

import "testing"

func TestCheckURL(t *testing.T) {
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
			err := CheckURL(tt.url)
			if (err == nil) != tt.wantOK {
				t.Errorf("CheckURL(%q) = %v, want ok=%v", tt.url, err, tt.wantOK)
			}
			if IsURL(tt.url) != tt.wantOK {
				t.Errorf("IsURL(%q) = %v, want %v", tt.url, !tt.wantOK, tt.wantOK)
			}
		})
	}
}
