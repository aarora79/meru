//go:build unix

// This file tests Run against real programs: echo and testdata/talk.sh. It
// needs a Unix shell for the script, so the build line keeps it to Unix.

package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// talk returns the absolute path of testdata/talk.sh.
func talk(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "talk.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// talkCommand builds a command that runs talk.sh with fixed arguments.
func talkCommand(t *testing.T, timeout string, args ...string) Command {
	t.Helper()
	return mustCommand(t, config.Command{Name: "talk", Argv: append([]string{talk(t)}, args...), Timeout: timeout})
}

func TestRunEcho(t *testing.T) {
	testHome(t)
	c := mustCommand(t, config.Command{
		Name:   "echo",
		Argv:   []string{"echo", "{text}"},
		Params: map[string]config.CommandParam{"text": {Type: TypeString}},
	})
	argv, err := c.Render(map[string]string{"text": "hello; ls $HOME"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), c, argv)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// No shell ran, so the semicolon and $HOME reach echo as text.
	if res.Stdout != "hello; ls $HOME\n" || res.ExitCode != 0 || res.Stderr != "" || res.Truncated {
		t.Errorf("Run = %+v", res)
	}
	if want := "exit code: 0\nstdout:\nhello; ls $HOME\n"; res.Text() != want {
		t.Errorf("Text = %q, want %q", res.Text(), want)
	}
}

func TestRunScript(t *testing.T) {
	testHome(t)
	tests := []struct {
		name     string
		args     []string
		stdout   string
		stderr   string
		exitCode int
	}{
		{"ok", []string{"talk", "hi"}, "out: hi\n", "err: hi\n", 0},
		{"non-zero exit is a result", []string{"talk", "bye", "3"}, "out: bye\n", "err: bye\n", 3},
		{"arguments stay whole", []string{"args", "a b", `"c"`, "d;e"}, "[a b]\n[\"c\"]\n[d;e]\n", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := talkCommand(t, "", tt.args...)
			res, err := Run(context.Background(), c, c.Argv)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Stdout != tt.stdout || res.Stderr != tt.stderr || res.ExitCode != tt.exitCode {
				t.Errorf("Run = %+v", res)
			}
		})
	}

	c := talkCommand(t, "", "talk", "x", "2")
	res, _ := Run(context.Background(), c, c.Argv)
	want := "exit code: 2\nstdout:\nout: x\nstderr:\nerr: x\n"
	if res.Text() != want {
		t.Errorf("Text = %q, want %q", res.Text(), want)
	}
}

func TestRunTruncates(t *testing.T) {
	testHome(t)
	c := talkCommand(t, "", "flood")
	res, err := Run(context.Background(), c, c.Argv)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Stdout) != maxOutput || !res.Truncated {
		t.Errorf("stdout is %d bytes, truncated = %v; want %d and true", len(res.Stdout), res.Truncated, maxOutput)
	}
	if !strings.Contains(res.Text(), "more than 1 MiB") {
		t.Error("Text doesn't say it cut the output")
	}
}

// TestRunTimeoutKillsGroup runs a script that starts a child and waits. The
// timeout must kill both: the child is in the script's process group, and
// Run kills the group.
func TestRunTimeoutKillsGroup(t *testing.T) {
	testHome(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	c := talkCommand(t, "300ms", "hang", pidFile)
	start := time.Now()
	_, err := Run(context.Background(), c, c.Argv)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want a deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Run took %v after a 300ms timeout", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the script wrote no child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// Signal 0 checks that a process exists without touching it. The
	// killed child may linger a moment as a zombie until the system reaps
	// it, so poll.
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d outlived the timeout: an orphan", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunCancelled(t *testing.T) {
	testHome(t)
	c := talkCommand(t, "", "hang", filepath.Join(t.TempDir(), "pid"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Run(ctx, c, c.Argv); err == nil {
		t.Error("Run returned no error for an ended context")
	}
}

// TestRunEnvironment checks that only PATH, HOME, LANG and the allowed
// names reach the program.
func TestRunEnvironment(t *testing.T) {
	home := testHome(t)
	t.Setenv("MERU_TEST_SECRET", "hunter2")
	t.Setenv("MERU_TEST_ALLOWED", "yes")
	t.Setenv("LANG", "en_US.UTF-8")
	c := mustCommand(t, config.Command{
		Name: "env", Argv: []string{talk(t), "env"}, EnvAllowlist: []string{"MERU_TEST_ALLOWED"},
	})
	res, err := Run(context.Background(), c, c.Argv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "MERU_TEST_SECRET") || strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("a variable outside env_allowlist reached the program:\n%s", res.Stdout)
	}
	for _, want := range []string{"MERU_TEST_ALLOWED=yes", "HOME=" + home, "LANG=en_US.UTF-8", "PATH="} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("the program's environment lacks %s:\n%s", want, res.Stdout)
		}
	}
}

func TestRunCwd(t *testing.T) {
	home := testHome(t)
	c := talkCommand(t, "", "pwd")
	res, err := Run(context.Background(), c, c.Argv)
	if err != nil || strings.TrimSpace(res.Stdout) != home {
		t.Errorf("default cwd: %q, %v; want %s", res.Stdout, err, home)
	}
	c = mustCommand(t, config.Command{Name: "pwd", Argv: []string{talk(t), "pwd"}, Cwd: "~/repos"})
	res, err = Run(context.Background(), c, c.Argv)
	if err != nil || strings.TrimSpace(res.Stdout) != filepath.Join(home, "repos") {
		t.Errorf("cwd = %q, %v", res.Stdout, err)
	}
}

func TestRunMissingProgram(t *testing.T) {
	testHome(t)
	c := mustCommand(t, config.Command{Name: "gone", Argv: []string{"meru-no-such-program"}})
	if _, err := Run(context.Background(), c, c.Argv); err == nil || !strings.Contains(err.Error(), "meru-no-such-program") {
		t.Errorf("Run error = %v, want one naming the program", err)
	}
}
