// This file tests the small helpers in main.go. The server itself is the
// fakeollama package, which has its own tests.

package main

import (
	"strings"
	"testing"
)

// TestCheckLoopback checks which listen addresses the tool accepts.
func TestCheckLoopback(t *testing.T) {
	tests := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:0", false},
		{"127.0.0.1:11434", false},
		{"[::1]:0", false},
		{"localhost:0", false},
		{"0.0.0.0:11434", true},
		{"192.168.1.5:11434", true},
		{":11434", true},
		{"no-port", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := checkLoopback(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkLoopback(%q) = %v, want error: %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

// TestSplitList checks the comma-list flag parser.
func TestSplitList(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"a", "a"},
		{" a , b ,, c ", "a|b|c"},
	}
	for _, tt := range tests {
		if got := strings.Join(splitList(tt.in), "|"); got != tt.want {
			t.Errorf("splitList(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
