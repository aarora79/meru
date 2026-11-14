// This file holds the stand-ins for machine_windows.go on every other
// system, so Describe compiles everywhere. Describe calls them only on
// Windows.

//go:build !windows

package platform

// windowsCPU returns ""; only Windows reads the processor's name this way.
func windowsCPU() string { return "" }

// windowsMemoryGB returns 0; only Windows reads main memory this way.
func windowsMemoryGB() int { return 0 }
