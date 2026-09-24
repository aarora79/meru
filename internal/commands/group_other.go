//go:build !unix

// This file holds Run's process handling where Unix process groups don't
// exist, chiefly Windows: a timeout kills the program itself, which is
// exec.CommandContext's default. A child the program started may outlive
// it there.

package commands

import "os/exec"

// otherEnv lists the variables Windows programs need to start: SystemRoot,
// without which many fail, and USERPROFILE, Windows' home folder.
var otherEnv = []string{"SystemRoot", "USERPROFILE"}

// ownGroup does nothing here; CommandContext kills the program on its own.
func ownGroup(cmd *exec.Cmd) {}
