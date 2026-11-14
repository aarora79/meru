// This file checks the Windows reads in machine_windows.go on a Windows
// machine. Every Windows machine has a processor name in the registry and
// some main memory, so both must come back.

//go:build windows

package platform

import (
	"context"
	"testing"
)

// TestDescribeWindows checks that Describe reads the processor's name and
// main memory on Windows. The GPU stays unchecked: not every machine has
// an NVIDIA card.
func TestDescribeWindows(t *testing.T) {
	m := Describe(context.Background())
	if m.OS != "Windows" {
		t.Errorf("OS = %q, want Windows", m.OS)
	}
	if m.CPU == "" {
		t.Error("CPU is empty; the registry holds the processor's name")
	}
	if m.MemoryGB <= 0 {
		t.Errorf("MemoryGB = %d, want main memory", m.MemoryGB)
	}
	t.Logf("read: %+v", m)
}
