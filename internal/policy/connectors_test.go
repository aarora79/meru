// This file holds the rule for how internal/connectors starts programs:
// only run.go may, through os/exec, by absolute path and with no shell.
// Every npm, uv and docker command goes through its Runner, so one file
// shows everything the package can run. See ARCHITECTURE.md, "The
// runtime folder".

package policy

import (
	"strconv"
	"strings"
	"testing"
)

// connectorsRunFile is the one file in internal/connectors that may
// import os/exec.
const connectorsRunFile = "internal/connectors/run.go"

// TestConnectorsRunOnlyThroughRun fails when a file in internal/connectors
// other than run.go imports os/exec, when any of its files imports
// syscall, or when one calls os.StartProcess: each is a way to start a
// program around the Runner. It also fails when run.go no longer imports
// os/exec, since then the scan would prove nothing.
func TestConnectorsRunOnlyThroughRun(t *testing.T) {
	root := moduleRoot(t)
	seen, runSeen := 0, false
	for _, f := range moduleFiles(t, root) {
		if f.isTest || !inDirs(f.rel, []string{"internal/connectors"}) {
			continue
		}
		seen++
		for _, imp := range f.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case path == "os/exec" && f.rel == connectorsRunFile:
				runSeen = true
			case path == "os/exec":
				t.Errorf("%s imports os/exec; internal/connectors starts programs only in %s", f.where(imp.Pos()), connectorsRunFile)
			case path == "syscall" || strings.HasPrefix(path, "golang.org/x/sys/"):
				t.Errorf("%s imports %s; internal/connectors starts programs only through os/exec in %s", f.where(imp.Pos()), path, connectorsRunFile)
			}
		}
		for _, pos := range selectorUses(f, "os", "StartProcess") {
			t.Errorf("%s calls os.StartProcess; use the Runner in %s", pos, connectorsRunFile)
		}
	}
	if seen == 0 {
		t.Fatal("found no files in internal/connectors; the scan is broken")
	}
	if !runSeen {
		t.Errorf("%s doesn't import os/exec; the rule has lost its one exec site", connectorsRunFile)
	}
}
