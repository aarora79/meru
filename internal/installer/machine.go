// This file holds the first step: it reads what kind of Mac this is with
// sysctl, sw_vers and df, and picks the models to suggest from the table
// in internal/config.

package installer

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/aarora79/meru/internal/config"
)

// Machine describes this Mac, for the first screen.
type Machine struct {
	Chip     string `json:"chip"`     // such as "Apple M4 Max"
	Arch     string `json:"arch"`     // "arm64" on Apple silicon
	MacOS    string `json:"macos"`    // such as "26.0"
	MemoryGB int    `json:"memoryGB"` // whole gigabytes
	// FreeDiskGB is the free space, in whole gigabytes, on the disk that
	// holds the home folder, where Ollama keeps the models.
	FreeDiskGB int `json:"freeDiskGB"`
	// Warnings lists what may go wrong on this Mac, in words for people.
	Warnings []string `json:"warnings,omitempty"`
	// Choices are the model sets that fit this memory, and Recommended
	// the index of the one the installer suggests.
	Choices     []Choice `json:"choices"`
	Recommended int      `json:"recommended"`
}

// Choice is one model set the user can pick in the first step.
type Choice struct {
	Label    string   `json:"label"`
	Why      string   `json:"why"`
	Download string   `json:"download"`
	Models   []string `json:"models"`
	// rec is the table row it came from. It has no JSON name, so it stays
	// on the Go side.
	rec config.Recommendation
}

// gb is one gigabyte in bytes, as macOS counts memory: 1024 cubed.
const gb = 1 << 30

// minMemoryGB and minDiskGB are the least memory and free disk the lite
// models need, with room for macOS and the other apps.
const (
	minMemoryGB = 16
	minDiskGB   = 10
)

// CheckMachine reads this Mac's chip, macOS version, memory and free disk
// through run, and fills in the model choices. It fails when sysctl can't
// say how much memory there is; the other facts are only shown, so a
// failure there leaves them empty.
func CheckMachine(ctx context.Context, run Runner, home string) (Machine, error) {
	m := Machine{Arch: runtime.GOARCH}
	mem, err := run(ctx, "sysctl", []string{"-n", "hw.memsize"}, nil)
	if err != nil {
		return Machine{}, fmt.Errorf("read the memory size: %w", err)
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(mem), 10, 64)
	if err != nil {
		return Machine{}, fmt.Errorf("read the memory size: sysctl said %q", strings.TrimSpace(mem))
	}
	m.MemoryGB = int(bytes / gb)

	if out, err := run(ctx, "sysctl", []string{"-n", "machdep.cpu.brand_string"}, nil); err == nil {
		m.Chip = strings.TrimSpace(out)
	}
	if out, err := run(ctx, "sw_vers", []string{"-productVersion"}, nil); err == nil {
		m.MacOS = strings.TrimSpace(out)
	}
	// df -k prints sizes in kilobytes; -P keeps each disk on one line.
	if out, err := run(ctx, "df", []string{"-k", "-P", home}, nil); err == nil {
		m.FreeDiskGB = freeGB(out)
	}

	if m.Arch != "arm64" {
		m.Warnings = append(m.Warnings, "This Mac has an Intel chip. Meru.app runs only on Apple silicon; meru and merud still work in Terminal.")
	}
	if m.MemoryGB < minMemoryGB {
		m.Warnings = append(m.Warnings, fmt.Sprintf("This Mac has %d GB of memory. Meru's smallest models want 16 GB, so answers may be slow.", m.MemoryGB))
	}
	if m.FreeDiskGB > 0 && m.FreeDiskGB < minDiskGB {
		m.Warnings = append(m.Warnings, fmt.Sprintf("Only %d GB of disk is free. The models need 2 GB or more, and Ollama needs room to unpack them.", m.FreeDiskGB))
	}
	m.Choices, m.Recommended = choicesFor(m.MemoryGB)
	return m, nil
}

// choicesFor returns the model sets that fit memGB, smallest first, and
// the index of the one config.Recommend picks.
func choicesFor(memGB int) ([]Choice, int) {
	best := config.Recommend(memGB)
	var choices []Choice
	pick := 0
	for _, r := range config.Recommendations() {
		if memGB < r.MinMemoryGB {
			continue
		}
		if r == best {
			pick = len(choices)
		}
		choices = append(choices, Choice{Label: r.Label, Why: r.Why, Download: r.Download, Models: r.Models(), rec: r})
	}
	return choices, pick
}

// freeGB reads the free space from df -k -P output: the fourth field of
// the last line, in kilobytes. It returns 0 when the output doesn't parse.
func freeGB(out string) int {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0
	}
	return int(kb * 1024 / gb)
}
