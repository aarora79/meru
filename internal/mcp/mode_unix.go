// This file holds the file-mode check on a saved sign-in for Unix systems
// (macOS and Linux). The build tag below keeps it out of Windows builds,
// where mode_windows.go takes its place.

//go:build !windows

package mcp

import (
	"fmt"
	"io/fs"
)

// checkTokenMode fails when group or others may read or write the token
// file at path. The low six bits of the mode (0o077) are those
// permissions; any of them set means another account on the machine could
// read the token.
func checkTokenMode(path string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: other users can read it; run chmod 600", path)
	}
	return nil
}
