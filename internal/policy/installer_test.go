// This file holds the rules for the one Meru program that starts other
// programs before merud exists: the Mac installer (cmd/meru-installer with
// internal/installer). It may run only the fixed allowlist in
// internal/installer/run.go, with no shell. The import rules it shares
// with the clients live in layout_test.go.

package policy

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/installer"
)

// installerDirs are the folders that hold the installer's code.
var installerDirs = []string{"cmd/meru-installer", "internal/installer"}

// installerRunFile is the one installer file that may import os/exec.
const installerRunFile = "internal/installer/run.go"

// TestInstallerRunsOnlyThroughRun fails when an installer file other than
// run.go imports os/exec, when any installer file imports syscall, or when
// one calls os.StartProcess: each is a way to start a program around the
// allowlist.
func TestInstallerRunsOnlyThroughRun(t *testing.T) {
	root := moduleRoot(t)
	seen := 0
	for _, f := range moduleFiles(t, root) {
		if f.isTest || !inDirs(f.rel, installerDirs) {
			continue
		}
		seen++
		for _, imp := range f.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case path == "os/exec" && f.rel != installerRunFile:
				t.Errorf("%s imports os/exec; the installer starts programs only in %s, from its allowlist", f.where(imp.Pos()), installerRunFile)
			case path == "syscall" || strings.HasPrefix(path, "golang.org/x/sys/"):
				t.Errorf("%s imports %s; the installer starts programs only through os/exec in %s", f.where(imp.Pos()), path, installerRunFile)
			}
		}
		for _, pos := range selectorUses(f, "os", "StartProcess") {
			t.Errorf("%s calls os.StartProcess; use the Runner in %s", pos, installerRunFile)
		}
	}
	if seen == 0 {
		t.Fatalf("found no files in %v; the scan is broken", installerDirs)
	}
}

// TestInstallerAllowlist fails when the allowlist names a shell, an
// interpreter or a downloader, or a path that isn't absolute. With no shell
// on the list, no argument can grow into a second command.
func TestInstallerAllowlist(t *testing.T) {
	denied := []string{
		"sh", "bash", "zsh", "dash", "ksh", "csh", "tcsh", "fish", "env", "sudo", "osascript",
		"python", "python3", "perl", "ruby", "node", "curl", "wget",
	}
	programs := installer.Programs()
	if len(programs) == 0 {
		t.Fatal("the allowlist is empty; the check would pass by finding nothing")
	}
	for name, paths := range programs {
		if slices.Contains(denied, name) {
			t.Errorf("the installer's allowlist names %s; no shell, interpreter or downloader belongs there", name)
		}
		for _, p := range paths {
			if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~/") {
				t.Errorf("the allowlist's %s path %q isn't absolute; the installer never searches PATH", name, p)
			}
			if slices.Contains(denied, filepath.Base(p)) {
				t.Errorf("the allowlist's %s path %q runs %s", name, p, filepath.Base(p))
			}
		}
	}
}
