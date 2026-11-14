// This file holds the error code for a refused connection on macOS, Linux
// and every other system but Windows, which has its own in
// refused_windows.go.

//go:build !windows

package engine

import "syscall"

// connRefused is the error a dial returns when nothing listens on the
// port.
const connRefused = syscall.ECONNREFUSED
