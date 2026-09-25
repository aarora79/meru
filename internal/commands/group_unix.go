//go:build unix

// This file holds the Unix side of Run's process handling: each program
// gets its own process group, and a timeout kills the whole group. The
// build line above keeps it to Unix systems; group_other.go covers the
// rest.

package commands

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// otherEnv lists the extra variables a program gets on Unix: none.
var otherEnv []string

// ownGroup makes cmd start in a new process group and makes cancelling it
// kill that group. Without the group, a timeout would kill only the program
// itself, and a child it started, such as the pager git runs, would live
// on as an orphan.
func ownGroup(cmd *exec.Cmd) {
	// Setpgid puts the child in a new group whose ID is the child's own
	// process ID.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancel runs when the context ends. A negative PID tells kill to
	// signal every process in that group.
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// The group is gone already: everything in it exited.
			return os.ErrProcessDone
		}
		return err
	}
}
