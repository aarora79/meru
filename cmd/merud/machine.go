// This file describes the computer merud runs on, in one line for the
// system prompt: the operating system and its version, the processor,
// memory, the GPU, the shell and the time zone. A model otherwise guesses,
// and answered a question about GPU use on an Apple silicon Mac with
// nvidia-smi. internal/platform reads the facts; this file only words them.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/platform"
)

// machineLine returns the line, such as "The user's computer: macOS 26.0
// (arm64), Apple M4 Max, 64 GB memory; shell zsh; time zone
// America/New_York." It leaves out any part it can't read rather than
// fail: the line helps answers, and merud runs without it. It holds no
// hostname or user name; the model needs neither. m is the machine as
// platform.Describe read it at startup.
func machineLine(m platform.Machine) string {
	return describeLine(m, filepath.Base(os.Getenv("SHELL")), timeZone())
}

// describeLine words m, the shell's name and the time zone as the line
// machineLine returns. It takes the facts as arguments so a test can
// check the wording for a GPU on a machine without one.
func describeLine(m platform.Machine, shell, tz string) string {
	parts := []string{m.OS + " (" + m.Arch + ")"}
	if m.CPU != "" {
		parts = append(parts, m.CPU)
	}
	if m.MemoryGB > 0 {
		parts = append(parts, fmt.Sprintf("%d GB memory", m.MemoryGB))
	}
	if m.GPU != "" {
		gpu := m.GPU
		if m.GPUMemoryGB > 0 {
			gpu += fmt.Sprintf(" with %d GB GPU memory", m.GPUMemoryGB)
		}
		parts = append(parts, gpu)
	}
	line := "The user's computer: " + strings.Join(parts, ", ")
	// filepath.Base gives "." for an empty SHELL.
	if shell != "" && shell != "." {
		line += "; shell " + shell
	}
	if tz != "" {
		line += "; time zone " + tz
	}
	return line + "."
}

// timeZone returns the local zone's name, such as "America/New_York": from
// TZ when set, else from where /etc/localtime points, else the zone's
// abbreviation.
func timeZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			return name
		}
	}
	name, _ := time.Now().Zone()
	return name
}
