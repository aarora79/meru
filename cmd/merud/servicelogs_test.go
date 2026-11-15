// Tests for tightenServiceLogs, which closes launchd's two logs to other
// accounts at startup.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTightenServiceLogs(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "merud.err.log")
	if err := os.WriteFile(open, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile applies the process umask, so set the wide mode after.
	if err := os.Chmod(open, 0o644); err != nil {
		t.Fatal(err)
	}
	// A link where a log would be is left alone, as is its target.
	target := filepath.Join(dir, "elsewhere.txt")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "merud.out.log")); err != nil {
		t.Fatal(err)
	}

	tightenServiceLogs(dir)

	if info, _ := os.Stat(open); info.Mode().Perm() != 0o600 {
		t.Errorf("merud.err.log is %v, want 0600", info.Mode().Perm())
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o644 {
		t.Errorf("the link's target became %v; a link must be left alone", info.Mode().Perm())
	}
	tightenServiceLogs(t.TempDir()) // no logs: nothing to do, no panic
}
