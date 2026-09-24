//go:build e2e

// Package e2e runs Meru the way a user does: it builds the real merud, meru
// and fakeollama binaries, starts them as separate processes, and checks what
// comes out of stdout, stderr, the socket and the files on disk.
//
// The build tag above keeps these tests out of `go test ./...`. Run them with
// `make e2e` or `go test -tags e2e ./test/e2e/...`. The tests in
// integration_test.go also need the `integration` tag and a real Ollama. See
// docs/coding-notes/e2e.md.
//
// This file holds TestMain, which builds the binaries once for every test.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// binDir is the directory that holds the freshly built binaries. TestMain
// sets it once, before any test runs, and nothing changes it afterwards; a
// package-level variable is the usual way for TestMain to hand setup to the
// tests, because TestMain can't pass arguments to them.
var binDir string

// TestMain runs instead of the tests when a package defines it. It builds the
// binaries, runs every test with m.Run, cleans up and exits with m.Run's
// status.
func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

// runMain does TestMain's work and returns the exit status. Keeping it
// separate lets the deferred RemoveAll run before os.Exit, which would skip
// deferred calls.
func runMain(m *testing.M) int {
	if runtime.GOOS == "windows" {
		// The tests stop processes with SIGINT and SIGTERM, which Windows
		// doesn't deliver to other processes.
		fmt.Println("e2e: skipped on Windows, which can't send SIGINT or SIGTERM to a child process")
		return 0
	}
	dir, err := os.MkdirTemp("", "meru-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	// defer runs os.RemoveAll when runMain returns, after every test.
	defer os.RemoveAll(dir)

	if err := buildBinaries(dir); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	binDir = dir
	return m.Run()
}

// buildBinaries compiles merud, meru and fakeollama into dir with `go build`.
// When the tests run under -race, the binaries get the race detector too, so
// a data race inside merud fails the run (see checkNoRace).
func buildBinaries(dir string) error {
	// `go env GOMOD` prints the path of go.mod; its directory is the module
	// root, where ./cmd/... resolves.
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return fmt.Errorf("find module root: %w", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))

	args := []string{"build", "-o", dir + string(filepath.Separator)}
	args = append(args, raceBuildFlags...) // ... spreads the slice into separate arguments
	args = append(args, "./cmd/merud", "./cmd/meru", "./cmd/fakeollama", "./cmd/fakemcp")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return nil
}

// bin returns the path of one built binary, such as bin("merud").
func bin(name string) string {
	return filepath.Join(binDir, name)
}
