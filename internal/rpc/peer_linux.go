// This file reads, on Linux, the user ID of the process at the other end of
// a Unix socket connection, and the owner of a folder. See peer.go.

package rpc

import (
	"io/fs"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID returns the user ID of the process that opened conn, from the
// kernel's SO_PEERCRED, and false when conn isn't a Unix socket or the
// kernel won't say.
func peerUID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid int
	var got bool
	// Control runs the function with the socket's file descriptor, while
	// the connection can't be closed under it.
	_ = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil {
			uid, got = int(cred.Uid), true
		}
	})
	return uid, got
}

// ownerUID returns the user ID that owns the file info describes, and
// false when the system doesn't say.
func ownerUID(info fs.FileInfo) (int, bool) {
	// Sys holds the system's own record of the file: a *syscall.Stat_t on
	// Linux, whose Uid is the owner.
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
