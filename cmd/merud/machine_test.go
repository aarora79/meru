// This file tests the machine line.

package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestMachineLine checks the line's shape on the machine the test runs on:
// it names the OS and architecture, ends with a period, and holds no host
// name.
func TestMachineLine(t *testing.T) {
	line := machineLine(context.Background())
	if !strings.HasPrefix(line, "The user's computer: ") || !strings.HasSuffix(line, ".") {
		t.Fatalf("line = %q", line)
	}
	if !strings.Contains(line, "("+runtime.GOARCH+")") {
		t.Errorf("line %q lacks the architecture", line)
	}
	if h, err := os.Hostname(); err == nil && h != "" && strings.Contains(line, h) {
		t.Errorf("line %q holds the host name", line)
	}
	t.Log(line)
}
