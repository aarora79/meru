// This file holds the file-mode check for Windows, which has no Unix mode
// bits. Go reports every Windows file as 0666 or 0444, so the Unix check
// would refuse every file. Windows guards the file with the access list of
// the user's profile folder instead.

//go:build windows

package secrets

import "io/fs"

// checkMode accepts every file on Windows; see the file comment.
func checkMode(string, fs.FileInfo) error {
	return nil
}
