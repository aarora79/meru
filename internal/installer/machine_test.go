// This file tests the Mac check: reading the facts through a fake Runner,
// and the model choices it offers for each amount of memory.

package installer

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// fakeMac answers the three programs the Mac check runs as a Mac with
// memGB of memory and 200 GB free would.
func fakeMac(memGB int64) *fakeRunner {
	return &fakeRunner{answer: func(program string, args []string, _ func(string)) (string, error) {
		switch {
		case program == "sysctl" && args[1] == "hw.memsize":
			return strconv.FormatInt(memGB<<30, 10) + "\n", nil
		case program == "sysctl":
			return "Apple M4 Max\n", nil
		case program == "sw_vers":
			return "26.0\n", nil
		case program == "df":
			return "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s1 1000000000 700000000 209715200 78% /System/Volumes/Data\n", nil
		}
		return "", errors.New("unexpected program " + program)
	}}
}

// TestCheckMachine checks the facts and the suggested models by memory.
func TestCheckMachine(t *testing.T) {
	tests := []struct {
		memGB       int64
		choices     int
		suggestMain string
		warn        bool
	}{
		{8, 1, "", true},
		{16, 1, "", false},
		{32, 1, "", false},
		{48, 2, "qwen3.6:35b-a3b-mxfp8", false},
		{64, 2, "qwen3.6:35b-a3b-mxfp8", false},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatInt(tt.memGB, 10)+" GB", func(t *testing.T) {
			m, err := CheckMachine(context.Background(), fakeMac(tt.memGB).run, "/Users/dana")
			if err != nil {
				t.Fatal(err)
			}
			if m.MemoryGB != int(tt.memGB) || m.Chip != "Apple M4 Max" || m.MacOS != "26.0" || m.FreeDiskGB != 200 {
				t.Errorf("facts = %+v", m)
			}
			if len(m.Choices) != tt.choices {
				t.Fatalf("%d choices, want %d", len(m.Choices), tt.choices)
			}
			if got := m.Choices[m.Recommended].rec.Main; got != tt.suggestMain {
				t.Errorf("suggested main = %q, want %q", got, tt.suggestMain)
			}
			memWarn := false
			for _, w := range m.Warnings {
				if strings.Contains(w, "memory") {
					memWarn = true
				}
			}
			if memWarn != tt.warn {
				t.Errorf("memory warning = %v, want %v: %v", memWarn, tt.warn, m.Warnings)
			}
		})
	}
}

// TestCheckMachineNeedsMemory checks that the step fails when sysctl can't
// say how much memory there is: the model choice depends on it.
func TestCheckMachineNeedsMemory(t *testing.T) {
	r := &fakeRunner{answer: func(string, []string, func(string)) (string, error) { return "", errors.New("no sysctl") }}
	if _, err := CheckMachine(context.Background(), r.run, "/Users/dana"); err == nil {
		t.Error("CheckMachine passed with no memory size")
	}
}

// TestFreeGB checks the df parser on good and bad output.
func TestFreeGB(t *testing.T) {
	tests := map[string]int{
		"Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 100 50 10485760 50% /\n": 10,
		"":        0,
		"garbage": 0,
	}
	for in, want := range tests {
		if got := freeGB(in); got != want {
			t.Errorf("freeGB(%q) = %d, want %d", in, got, want)
		}
	}
}
