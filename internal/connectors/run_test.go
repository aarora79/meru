// This file tests ExecRunner by running the test binary itself as the
// child program, the standard library's own way to test os/exec: the
// child sees MERU_HELPER_PROCESS and plays a small program instead of
// running the tests.

package connectors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestHelperProcess is the child program. It does nothing in a normal
// test run. As a child it prints MERU_SEEN and MERU_TEST_SECRET from its
// environment, then its arguments after the first, one per line, and
// exits with code 3 when the first argument is "fail".
func TestHelperProcess(t *testing.T) {
	if os.Getenv("MERU_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	fmt.Printf("seen=%s\n", os.Getenv("MERU_SEEN"))
	fmt.Printf("secret=%s\n", os.Getenv("MERU_TEST_SECRET"))
	for _, a := range args[1:] {
		fmt.Fprintln(os.Stderr, a)
	}
	if args[0] == "fail" {
		os.Exit(3)
	}
	os.Exit(0)
}

// helperCmd returns a Cmd that runs TestHelperProcess with args.
func helperCmd(args ...string) Cmd {
	return Cmd{
		Path: os.Args[0],
		Args: append([]string{"-test.run=^TestHelperProcess$", "--"}, args...),
		Env:  []string{"MERU_HELPER_PROCESS=1", "MERU_SEEN=yes"},
	}
}

// TestExecRunner checks the lines, the tail, the exit code, the trimmed
// environment and the refusal of a relative path.
func TestExecRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the connectors run on macOS and Linux first")
	}
	t.Setenv("MERU_TEST_SECRET", "hunter2")
	run := ExecRunner()
	ctx := context.Background()

	var lines []string
	tail, err := run(ctx, helperCmd("ok", "one; rm -rf /", "two"), func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"seen=yes", "secret=", "one; rm -rf /", "two"} {
		if !strings.Contains(tail, want) {
			t.Errorf("tail = %q, want it to hold %q", tail, want)
		}
	}
	if strings.Contains(tail, "hunter2") {
		t.Error("the child saw a variable its Cmd didn't list")
	}
	// A coverage run adds a warning line of its own at the end, so only
	// the first four lines are the child's.
	if want := []string{"seen=yes", "secret=", "one; rm -rf /", "two"}; len(lines) < 4 || !slices.Equal(lines[:4], want) {
		t.Errorf("lines = %q, want %q first", lines, want)
	}

	tail, err = run(ctx, helperCmd("fail", "the last words"), nil)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(tail, "the last words") {
		t.Errorf("run fail: %v, tail %q", err, tail)
	}

	if _, err := run(ctx, Cmd{Path: "node"}, nil); !errors.Is(err, ErrNotAbsolute) {
		t.Errorf("run node: %v, want ErrNotAbsolute", err)
	}
}
