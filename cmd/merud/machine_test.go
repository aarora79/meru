// This file tests the machine line.

package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/platform"
)

// TestMachineLine checks the line's shape on the machine the test runs on:
// it names the OS and architecture, ends with a period, and holds no host
// name.
func TestMachineLine(t *testing.T) {
	line := machineLine(platform.Describe(context.Background()))
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

// TestDescribeLine checks the wording for a Mac, whose GPU shares main
// memory, and for Linux with an NVIDIA card.
func TestDescribeLine(t *testing.T) {
	tests := []struct {
		name  string
		m     platform.Machine
		shell string
		want  string
	}{
		{
			name:  "mac",
			m:     platform.Machine{OS: "macOS 26.0", Arch: "arm64", CPU: "Apple M4 Max", MemoryGB: 64},
			shell: "zsh",
			want:  "The user's computer: macOS 26.0 (arm64), Apple M4 Max, 64 GB memory; shell zsh; time zone UTC.",
		},
		{
			name: "linux with a GPU",
			m: platform.Machine{OS: "Ubuntu 24.04.4 LTS", Arch: "amd64", CPU: "AMD EPYC 7R13 Processor",
				MemoryGB: 31, GPU: "NVIDIA L40S", GPUMemoryGB: 45},
			shell: "bash",
			want:  "The user's computer: Ubuntu 24.04.4 LTS (amd64), AMD EPYC 7R13 Processor, 31 GB memory, NVIDIA L40S with 45 GB GPU memory; shell bash; time zone UTC.",
		},
		{
			name:  "nothing read",
			m:     platform.Machine{OS: "Windows", Arch: "amd64"},
			shell: ".",
			want:  "The user's computer: Windows (amd64); time zone UTC.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeLine(tt.m, tt.shell, "UTC"); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
