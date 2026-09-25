// This file describes the computer merud runs on, in one line for the
// system prompt: the operating system and its version, the processor,
// memory, the shell and the time zone. A model otherwise guesses, and
// answered a question about GPU use on an Apple silicon Mac with
// nvidia-smi.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// machineLine returns the line, such as "The user's computer: macOS 26.0
// (arm64), Apple M4 Max, 64 GB memory; shell zsh; time zone
// America/New_York." It leaves out any part it can't read rather than
// fail: the line helps answers, and merud runs without it. It holds no
// hostname or user name; the model needs neither.
func machineLine(ctx context.Context) string {
	var parts []string
	osName := osDescription(ctx)
	parts = append(parts, osName+" ("+runtime.GOARCH+")")
	if cpu := cpuName(ctx); cpu != "" {
		parts = append(parts, cpu)
	}
	if gb := memoryGB(ctx); gb > 0 {
		parts = append(parts, fmt.Sprintf("%d GB memory", gb))
	}
	line := "The user's computer: " + strings.Join(parts, ", ")
	if sh := filepath.Base(os.Getenv("SHELL")); sh != "" && sh != "." {
		line += "; shell " + sh
	}
	if tz := timeZone(); tz != "" {
		line += "; time zone " + tz
	}
	return line + "."
}

// osDescription names the operating system and its version: "macOS 26.0",
// the PRETTY_NAME of /etc/os-release on Linux, or the GOOS name alone.
func osDescription(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		if v := runQuiet(ctx, "sw_vers", "-productVersion"); v != "" {
			return "macOS " + v
		}
		return "macOS"
	case "linux":
		b, err := os.ReadFile("/etc/os-release")
		if err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
					return strings.Trim(v, `"`)
				}
			}
		}
		return "Linux"
	case "windows":
		return "Windows"
	}
	return runtime.GOOS
}

// cpuName returns the processor's name, such as "Apple M4 Max", or "".
func cpuName(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		return runQuiet(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
	case "linux":
		b, err := os.ReadFile("/proc/cpuinfo")
		if err != nil {
			return ""
		}
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// memoryGB returns the installed memory in whole gigabytes, or 0.
func memoryGB(ctx context.Context) int {
	switch runtime.GOOS {
	case "darwin":
		n, err := strconv.ParseInt(runQuiet(ctx, "sysctl", "-n", "hw.memsize"), 10, 64)
		if err == nil {
			return int(n >> 30)
		}
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
				kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
				if err == nil {
					return int((kb + 1<<20 - 1) >> 20)
				}
			}
		}
	}
	return 0
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

// runQuiet runs a program with no shell, for at most two seconds, and returns
// its output trimmed, or "" when it fails.
func runQuiet(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed program names and arguments
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
