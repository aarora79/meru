// This file holds the checks on who may reach merud's socket: the user ID
// of each connecting process, and the folder the socket sits in. The
// socket file is mode 0600 already; these close the gaps around it. A
// program running as the user passes every one, as it would read the
// user's files directly (docs/design/security.md). peerUID and ownerUID,
// which differ by system, live in peer_darwin.go, peer_linux.go and
// peer_other.go.

package rpc

import (
	"fmt"
	"io/fs"
	"net"
	"os"
)

// samePeer reports whether conn comes from a process of the user who runs
// merud, and the peer's user ID when the system says. On a system that
// can't say, it reports true: the folder's mode is the guard there. Root
// is refused like any other user: nothing in Meru runs as root, and a
// root process can read the user's files without merud.
func samePeer(conn net.Conn) (uid int, ok bool) {
	uid, known := peerUID(conn)
	if !known {
		return 0, true
	}
	return uid, uid == os.Getuid()
}

// checkSocketDir checks the folder that holds the socket, dir, before
// Listen creates the socket there. It fails when dir is a symbolic link,
// which could move the socket somewhere other accounts reach, or when
// another user owns it. A folder that group or other accounts may open
// gets mode 0700, since os.MkdirAll never changes the mode of a folder
// that exists.
func checkSocketDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check the socket's folder %s: %w", dir, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("the socket's folder %s is a symbolic link; point merud at the folder itself, with --socket or [dir] in config", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("the socket's folder %s isn't a folder", dir)
	}
	if owner, ok := ownerUID(info); ok && owner != os.Getuid() {
		return fmt.Errorf("the socket's folder %s belongs to another user (uid %d); merud keeps its socket only in a folder of its own user's", dir, owner)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a folder: 0700 is owner-only, and the execute bit lets the owner open it
		return fmt.Errorf("close the socket's folder %s to other accounts: %w", dir, err)
	}
	return nil
}
