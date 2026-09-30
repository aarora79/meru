// This file tests the allowlist and the Runner. The Runner test starts
// this test binary itself as the child program, so no real program runs.

package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestProgramsAllowlist checks every entry: an absolute path or one under
// "~/", and no shell or interpreter among the programs.
func TestProgramsAllowlist(t *testing.T) {
	shells := []string{"sh", "bash", "zsh", "fish", "env", "python", "python3", "perl", "ruby", "node", "osascript", "curl"}
	for name, paths := range Programs() {
		if slices.Contains(shells, name) {
			t.Errorf("the allowlist names %s", name)
		}
		for _, p := range paths {
			if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~/") {
				t.Errorf("%s: path %q isn't absolute", name, p)
			}
			if base := filepath.Base(p); base != name {
				t.Errorf("%s: path %q runs %s", name, p, base)
			}
		}
	}
}

// TestLocate checks the two ways a program isn't found.
func TestLocate(t *testing.T) {
	if _, err := Locate("bash", t.TempDir()); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("Locate(bash) = %v, want ErrNotAllowed", err)
	}
	// launchctl is at /bin/launchctl on every Mac, and nowhere on Linux.
	if got, err := Locate("launchctl", t.TempDir()); err == nil && got != "/bin/launchctl" {
		t.Errorf("Locate(launchctl) = %q", got)
	} else if err != nil && !errors.Is(err, ErrMissing) {
		t.Errorf("Locate(launchctl) = %v, want ErrMissing where it isn't", err)
	}
	// The connectors are merud's: the installer runs no docker or uv.
	for _, gone := range []string{"docker", "uv", "uvx", "npm", "npx"} {
		if _, err := Locate(gone, t.TempDir()); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Locate(%s) = %v, want ErrNotAllowed", gone, err)
		}
	}
	run := ExecRunner(t.TempDir())
	if _, err := run(context.Background(), "rm", []string{"-rf", "/"}, nil); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("ExecRunner ran rm: %v", err)
	}
}

// TestRunLines starts this test binary as a child that prints three lines
// and fails, and checks that each line arrives and the tail comes back.
func TestRunLines(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperChild")
	cmd.Env = append(os.Environ(), "MERU_INSTALLER_CHILD=1")
	var got []string
	out, err := runLines(cmd, func(l string) { got = append(got, l) })
	if err == nil {
		t.Error("the child's failure didn't come back")
	}
	if !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Errorf("lines = %q", got)
	}
	if out != "one\ntwo\nthree" {
		t.Errorf("tail = %q", out)
	}
}

// TestHelperChild is the child TestRunLines starts. Run as a normal test,
// it does nothing.
func TestHelperChild(t *testing.T) {
	if os.Getenv("MERU_INSTALLER_CHILD") != "1" {
		return
	}
	fmt.Println("one")
	fmt.Fprintln(os.Stderr, "two")
	fmt.Println("three")
	os.Exit(3)
}
