// This file reads the machine merud runs on: Describe gathers the facts,
// and one small parser per source turns a program's output or a /proc
// file into numbers. The parsers take text, so tests feed them samples
// from any system.

package platform

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Machine describes one computer. A field Describe couldn't read stays at
// its zero value: "" or 0.
type Machine struct {
	// OS is the system and its version, such as "macOS 26.0" or
	// "Ubuntu 24.04.4 LTS".
	OS string
	// Arch is Go's name for the processor family, such as "arm64".
	Arch string
	// CPU is the processor's name, such as "Apple M4 Max".
	CPU string
	// MemoryGB is main memory in whole gigabytes.
	MemoryGB int
	// GPU names the graphics card, such as "NVIDIA L40S", or "2 x NVIDIA
	// L40S" for two. It stays "" on a Mac, whose GPU is part of CPU.
	GPU string
	// GPUMemoryGB is the GPU's own memory in whole gigabytes, summed over
	// every card, since Ollama splits a model across them. It stays 0 on
	// a Mac, whose GPU uses main memory.
	GPUMemoryGB int
}

// ModelMemoryGB returns the memory a model loads into, in whole
// gigabytes: the GPU's own memory when the machine has a GPU with memory
// of its own, and main memory otherwise, as on a Mac, whose GPU shares
// it, or on a machine where Ollama runs models on the processor.
func (m Machine) ModelMemoryGB() int {
	if m.GPUMemoryGB > 0 {
		return m.GPUMemoryGB
	}
	return m.MemoryGB
}

// Describe reads this machine's facts. It never fails: each fact it
// can't read stays empty, because every caller works without it.
func Describe(ctx context.Context) Machine {
	m := Machine{Arch: runtime.GOARCH}
	switch runtime.GOOS {
	case "darwin":
		m.OS = "macOS"
		if v := run(ctx, "sw_vers", "-productVersion"); v != "" {
			m.OS += " " + v
		}
		m.CPU = run(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
		if n, err := strconv.ParseInt(run(ctx, "sysctl", "-n", "hw.memsize"), 10, 64); err == nil {
			m.MemoryGB = int(n >> 30) // >> 30 divides by 1024 cubed.
		}
	case "linux":
		m.OS = osRelease(readFile("/etc/os-release"))
		m.CPU = cpuInfo(readFile("/proc/cpuinfo"))
		m.MemoryGB = memInfo(readFile("/proc/meminfo"))
		m.GPU, m.GPUMemoryGB = nvidiaSMI(run(ctx, "nvidia-smi",
			"--query-gpu=name,memory.total", "--format=csv,noheader,nounits"))
	default:
		m.OS = For(runtime.GOOS).Name
	}
	return m
}

// readFile returns a file's text, or "" when it can't be read.
func readFile(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- fixed system paths
	if err != nil {
		return ""
	}
	return string(b)
}

// osRelease returns PRETTY_NAME from the text of /etc/os-release, such as
// "Ubuntu 24.04.4 LTS", or "Linux" when it isn't there.
func osRelease(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			if v = strings.Trim(v, `"`); v != "" {
				return v
			}
		}
	}
	return "Linux"
}

// cpuInfo returns the first "model name" in the text of /proc/cpuinfo, or
// "" when there is none, as on some arm64 boards.
func cpuInfo(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// memInfo returns MemTotal from the text of /proc/meminfo in whole
// gigabytes, rounded up: the kernel keeps some memory for itself, so a
// 32 GB machine reports a little under 32. It returns 0 when MemTotal
// is missing.
func memInfo(text string) int {
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			if err == nil {
				return int((kb + 1<<20 - 1) >> 20)
			}
		}
	}
	return 0
}

// nvidiaSMI reads nvidia-smi's CSV output, one "name, MiB" line per card,
// and returns the cards' name and their memory summed, in whole gigabytes
// rounded to nearest: an L40S reports 46068 MiB, which is 45 GB. It
// returns "" and 0 for empty output, which is what a machine without
// nvidia-smi gives.
func nvidiaSMI(text string) (string, int) {
	var names []string
	var mib int64
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		name, size, ok := strings.Cut(l, ",")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
		if err != nil {
			continue
		}
		names = append(names, strings.TrimSpace(name))
		mib += n
	}
	switch len(names) {
	case 0:
		return "", 0
	case 1:
		return names[0], int((mib + 512) >> 10)
	}
	// Several cards: name the first and count them. A machine with mixed
	// cards is rare enough that the first name will do.
	return fmt.Sprintf("%d x %s", len(names), names[0]), int((mib + 512) >> 10)
}
