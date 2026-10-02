// This file holds the rule for internal/render, web_fetch's page reader:
// it starts Chrome only through connectors.StartPiped, so it imports no
// os/exec or syscall and calls no os.StartProcess. One file,
// internal/connectors/run.go, then shows every program merud starts for a
// connector or a rendered page. See ARCHITECTURE.md, "Pages that need
// JavaScript".

package policy

import (
	"strconv"
	"strings"
	"testing"
)

// TestRenderStartsNoProgram fails when a file in internal/render imports
// os/exec, syscall or golang.org/x/sys, or calls os.StartProcess.
func TestRenderStartsNoProgram(t *testing.T) {
	root := moduleRoot(t)
	seen := 0
	for _, f := range moduleFiles(t, root) {
		if f.isTest || !inDirs(f.rel, []string{"internal/render"}) {
			continue
		}
		seen++
		for _, imp := range f.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "os/exec" || path == "syscall" || strings.HasPrefix(path, "golang.org/x/sys/") {
				t.Errorf("%s imports %s; internal/render starts Chrome only through connectors.StartPiped", f.where(imp.Pos()), path)
			}
		}
		for _, pos := range selectorUses(f, "os", "StartProcess") {
			t.Errorf("%s calls os.StartProcess; use connectors.StartPiped", pos)
		}
	}
	if seen == 0 {
		t.Fatal("found no files in internal/render; the scan is broken")
	}
}
