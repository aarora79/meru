// This file holds Windows' error code for a refused connection. The
// syscall package's ECONNREFUSED is a made-up value on Windows that no
// socket call returns, so errors.Is would never match it there.

//go:build windows

package engine

import "syscall"

// connRefused is the error a dial returns when nothing listens on the
// port: WSAECONNREFUSED, code 10061 in Windows Sockets. syscall names
// some WSA codes but not this one, so the number stands here.
const connRefused = syscall.Errno(10061)
