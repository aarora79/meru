// This file tests the table and the parsers. Both take plain data, so
// every case runs on every system.

package platform

import (
	"context"
	"runtime"
	"slices"
	"testing"
)

// TestFor checks the fields each system's row must get right.
func TestFor(t *testing.T) {
	tests := []struct {
		goos, name, here, open, clip, service string
		notes, shared                         bool
	}{
		{"darwin", "macOS", "this Mac", "open", "pbcopy", "launchd", true, true},
		{"linux", "Linux", "this computer", "xdg-open", "wl-copy", "systemd", false, false},
		{"windows", "Windows", "this computer", "rundll32", "clip.exe", "", false, false},
		// An unknown system gets the Linux programs under its own name.
		{"freebsd", "freebsd", "this computer", "xdg-open", "wl-copy", "systemd", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			p := For(tt.goos)
			if p.GOOS != tt.goos || p.Name != tt.name || p.Here != tt.here {
				t.Errorf("For(%q) = %q, %q, %q; want %q, %q, %q", tt.goos, p.GOOS, p.Name, p.Here, tt.goos, tt.name, tt.here)
			}
			if p.Open[0] != tt.open {
				t.Errorf("Open = %v, want %s first", p.Open, tt.open)
			}
			if p.Clipboard[0][0] != tt.clip {
				t.Errorf("Clipboard = %v, want %s first", p.Clipboard, tt.clip)
			}
			if p.AppleNotes != tt.notes || p.SharedGPUMemory != tt.shared {
				t.Errorf("AppleNotes, SharedGPUMemory = %v, %v; want %v, %v", p.AppleNotes, p.SharedGPUMemory, tt.notes, tt.shared)
			}
			if p.Service != tt.service {
				t.Errorf("Service = %q, want %q", p.Service, tt.service)
			}
			if p.OllamaHint == "" {
				t.Error("OllamaHint is empty")
			}
		})
	}
}

// TestForCopies checks that a caller who changes a row's slices leaves
// the table as it was.
func TestForCopies(t *testing.T) {
	p := For("darwin")
	p.ProgramDirs[0] = "/changed"
	if For("darwin").ProgramDirs[0] == "/changed" {
		t.Error("changing a returned row changed the table")
	}
}

// TestProgramDirs checks that Linux looks in the system's own folder
// first and macOS keeps Homebrew ahead of it, as findDocker expects.
func TestProgramDirs(t *testing.T) {
	if got := For("linux").ProgramDirs[0]; got != "/usr/bin" {
		t.Errorf("linux first folder = %q, want /usr/bin", got)
	}
	mac := For("darwin").ProgramDirs
	if slices.Index(mac, "/opt/homebrew/bin") > slices.Index(mac, "/usr/bin") {
		t.Errorf("macOS folders %v put /usr/bin ahead of Homebrew", mac)
	}
}

// TestOSRelease checks PRETTY_NAME and the fallback.
func TestOSRelease(t *testing.T) {
	text := "NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\nVERSION_ID=\"24.04\"\n"
	if got := osRelease(text); got != "Ubuntu 24.04.4 LTS" {
		t.Errorf("osRelease = %q", got)
	}
	if got := osRelease(""); got != "Linux" {
		t.Errorf("osRelease(\"\") = %q, want Linux", got)
	}
}

// TestCPUInfo checks the first model name wins and a file without one
// gives "".
func TestCPUInfo(t *testing.T) {
	text := "processor\t: 0\nmodel name\t: AMD EPYC 7R13 Processor\nprocessor\t: 1\nmodel name\t: AMD EPYC 7R13 Processor\n"
	if got := cpuInfo(text); got != "AMD EPYC 7R13 Processor" {
		t.Errorf("cpuInfo = %q", got)
	}
	if got := cpuInfo("processor\t: 0\nBogoMIPS\t: 50.00\n"); got != "" {
		t.Errorf("cpuInfo without a model name = %q, want \"\"", got)
	}
}

// TestMemInfo checks MemTotal rounds up to whole gigabytes.
func TestMemInfo(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"MemTotal:       32014412 kB\nMemFree: 1 kB\n", 31}, // 30.5 GiB rounds up to 31
		{"MemTotal:       16777216 kB\n", 16},
		{"MemFree: 1 kB\n", 0},
	}
	for _, tt := range tests {
		if got := memInfo(tt.text); got != tt.want {
			t.Errorf("memInfo(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

// TestNvidiaSMI checks one card, two cards, and no output.
func TestNvidiaSMI(t *testing.T) {
	tests := []struct {
		text     string
		wantName string
		wantGB   int
	}{
		{"NVIDIA L40S, 46068\n", "NVIDIA L40S", 45},
		{"NVIDIA GeForce RTX 4090, 24564\nNVIDIA GeForce RTX 4090, 24564\n", "2 x NVIDIA GeForce RTX 4090", 48},
		{"", "", 0},
		{"NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.", "", 0},
	}
	for _, tt := range tests {
		name, gb := nvidiaSMI(tt.text)
		if name != tt.wantName || gb != tt.wantGB {
			t.Errorf("nvidiaSMI(%q) = %q, %d; want %q, %d", tt.text, name, gb, tt.wantName, tt.wantGB)
		}
	}
}

// TestDescribe reads this machine. It can only check what every machine
// has: the processor family and a name for the system.
func TestDescribe(t *testing.T) {
	m := Describe(context.Background())
	if m.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", m.Arch, runtime.GOARCH)
	}
	if m.OS == "" {
		t.Error("OS is empty")
	}
}
