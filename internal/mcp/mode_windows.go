// This file holds the file-mode check on a saved sign-in for Windows,
// which has no Unix mode bits. Go reports every Windows file as 0666 or
// 0444, so the Unix check would refuse every token file. Windows guards
// the file with the access list of the user's profile folder instead, as
// it does secrets.toml (internal/secrets/mode_windows.go).

//go:build windows

package mcp

import "io/fs"

// checkTokenMode accepts every file on Windows; see the file comment.
func checkTokenMode(string, fs.FileInfo) error {
	return nil
}
