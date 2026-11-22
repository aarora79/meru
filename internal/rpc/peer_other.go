//go:build !darwin && !linux

// This file is peerUID and ownerUID for the other systems, Windows among
// them, where Go has no call for a Unix socket's peer. See peer.go.

package rpc

import (
	"io/fs"
	"net"
)

// peerUID reports false: the system can't say who opened conn, so the
// check passes it, and the folder's access rules are the guard.
func peerUID(net.Conn) (int, bool) { return 0, false }

// ownerUID reports false: the check leaves the folder's owner alone.
func ownerUID(fs.FileInfo) (int, bool) { return 0, false }
