// Tests for peer.go: the peer's user ID and the socket's folder.

package rpc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestSamePeer connects to a socket from this process, so the peer is the
// same user, and checks samePeer lets it through with that user's ID where
// the system can say.
func TestSamePeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "meru-peer-") // short: macOS caps a socket path at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	uid, ok := samePeer(conn)
	if !ok {
		t.Fatalf("samePeer = %d, false; want this process's own user let through", uid)
	}
	if got, known := peerUID(conn); known && got != os.Getuid() {
		t.Errorf("peerUID = %d, want %d", got, os.Getuid())
	}
}

func TestCheckSocketDir(t *testing.T) {
	base := t.TempDir()

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	// os.Mkdir applies the umask, so widen the mode after.
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkSocketDir(open); err != nil {
		t.Fatalf("checkSocketDir(0755 folder) = %v", err)
	}
	if info, _ := os.Stat(open); info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode = %v, want 0700 after the check", info.Mode().Perm())
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(open, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if err := checkSocketDir(link); err == nil {
		t.Error("a folder that is a symbolic link passed")
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkSocketDir(file); err == nil {
		t.Error("a file passed as the socket's folder")
	}
	if err := checkSocketDir(filepath.Join(base, "missing")); err == nil {
		t.Error("a missing folder passed")
	}
}
