// This file holds the two rules for internal/platform: it imports only the
// standard library, so any package, client or daemon, may use it; and only
// run.go starts a program, so one file shows everything it can run. See
// ARCHITECTURE.md, "One table of differences".

package policy

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// platformRunFile is the one file in internal/platform that may import
// os/exec.
const platformRunFile = "internal/platform/run.go"

// platformWindowsFile is the one file in internal/platform that may import
// syscall: Windows keeps the processor's name in the registry and main
// memory behind kernel32's GlobalMemoryStatusEx, and Go reaches neither
// without it. It may call nothing in syscall that starts a program, and
// may load only the Windows functions in windowsProcs, so run.go stays the
// one file that can start one.
const platformWindowsFile = "internal/platform/machine_windows.go"

// windowsProcs lists the functions platformWindowsFile may load from a
// DLL with NewProc. CreateProcessW is one NewProc away, so the list is
// closed.
var windowsProcs = map[string]bool{"GlobalMemoryStatusEx": true}

// syscallStarts lists the syscall functions that start a program, on any
// system.
var syscallStarts = []string{"StartProcess", "ForkExec", "Exec", "CreateProcess", "CreateProcessAsUser"}

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
			case path == "syscall" && f.rel == platformWindowsFile:
				checkWindowsFile(t, f)
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

// checkWindowsFile fails when f, platformWindowsFile, calls a syscall
// function that starts a program, or loads a Windows function with NewProc
// that isn't in windowsProcs, or whose name isn't a string in the code,
// which the scan couldn't check.
func checkWindowsFile(t *testing.T, f sourceFile) {
	t.Helper()
	for _, name := range syscallStarts {
		for _, pos := range selectorUses(f, "syscall", name) {
			t.Errorf("%s calls syscall.%s; internal/platform starts programs only in %s", pos, name, platformRunFile)
		}
	}
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewProc" || len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		name := ""
		if ok && lit.Kind == token.STRING {
			name, _ = strconv.Unquote(lit.Value)
		}
		if !windowsProcs[name] {
			t.Errorf("%s loads a Windows function that isn't in windowsProcs; add it there only if it starts no program", f.where(call.Pos()))
		}
		return true
	})
}
