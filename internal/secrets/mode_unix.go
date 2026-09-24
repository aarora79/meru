// This file holds the file-mode check for Unix systems (macOS and Linux).
// The build tag below keeps it out of Windows builds, where mode_windows.go
// takes its place.

//go:build !windows

package secrets

import (
	"fmt"
	"io/fs"
)

// checkMode fails when group or others may read or write the file. The low
// six bits of the mode (0o077) are those permissions; any of them set means
// another account on the machine could read your keys.
func checkMode(path string, info fs.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("secrets %s has mode %#o, so other users can read it; run chmod 600 %s", path, perm, path)
	}
	return nil
}
