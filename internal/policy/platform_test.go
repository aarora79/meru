// This file holds the two rules for internal/platform: it imports only the
// standard library, so any package, client or daemon, may use it; and only
// run.go starts a program, so one file shows everything it can run. See
// ARCHITECTURE.md, "One table of differences".

package policy

import (
	"strconv"
	"strings"
	"testing"
)

// platformRunFile is the one file in internal/platform that may import
// os/exec.
const platformRunFile = "internal/platform/run.go"

// TestPlatformRules fails when a file in internal/platform imports a
// package from outside the standard library, imports os/exec anywhere but
// run.go, imports syscall, or calls os.StartProcess. It also fails when
// run.go no longer imports os/exec, since then the scan would prove
// nothing.
func TestPlatformRules(t *testing.T) {
	root := moduleRoot(t)
	seen, runSeen := 0, false
	for _, f := range moduleFiles(t, root) {
		if f.isTest || !inDirs(f.rel, []string{"internal/platform"}) {
			continue
		}
		seen++
		for _, imp := range f.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			// A standard library path has no dot in its first element;
			// every module path, such as github.com/..., has one.
			case strings.Contains(strings.Split(path, "/")[0], "."):
				t.Errorf("%s imports %s; internal/platform imports only the standard library", f.where(imp.Pos()), path)
			case path == "os/exec" && f.rel == platformRunFile:
				runSeen = true
			case path == "os/exec":
				t.Errorf("%s imports os/exec; internal/platform starts programs only in %s", f.where(imp.Pos()), platformRunFile)
			case path == "syscall":
				t.Errorf("%s imports syscall; internal/platform starts programs only through os/exec in %s", f.where(imp.Pos()), platformRunFile)
			}
		}
		for _, pos := range selectorUses(f, "os", "StartProcess") {
			t.Errorf("%s calls os.StartProcess; use run in %s", pos, platformRunFile)
		}
	}
	if seen == 0 {
		t.Fatal("found no files in internal/platform; the scan is broken")
	}
	if !runSeen {
		t.Errorf("%s doesn't import os/exec; the rule has lost its one exec site", platformRunFile)
	}
}
