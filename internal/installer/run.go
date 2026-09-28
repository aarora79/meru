// This file is the one place the installer starts other programs. It holds
// the allowlist, a fixed map from each program's name to the absolute
// paths it may live at, and the Runner that runs one of them with no
// shell. internal/policy checks that no other installer file imports
// os/exec and that the allowlist holds no shell or interpreter.

package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotAllowed means a step asked for a program the allowlist doesn't
// name. It is a programmer mistake, and a test catches it.
var ErrNotAllowed = errors.New("the installer doesn't run this program")

// ErrMissing means an allowed program isn't at any of its paths on this
// Mac, such as docker before Docker Desktop is installed.
var ErrMissing = errors.New("not installed")

// Programs returns the allowlist: each program the installer may run, with
// the absolute paths it looks for it at, in order. A path that starts with
// "~/" sits under the home folder. The installer never searches PATH: an
// app opened from Finder gets a short PATH that holds neither Homebrew nor
// Docker, and a fixed list means no file dropped earlier on PATH can stand
// in for a program.
//
// It builds a new map on each call, so no caller can change the list.
func Programs() map[string][]string {
	return map[string][]string{
		// Homebrew installs Ollama and uv when the user has it.
		"brew": {"/opt/homebrew/bin/brew", "/usr/local/bin/brew"},
		// docker pulls and runs the SearXNG container. Docker Desktop,
		// OrbStack and colima each put the same command in one of these.
		"docker": {
			"/usr/local/bin/docker", "/opt/homebrew/bin/docker", "~/.orbstack/bin/docker",
			"~/.docker/bin/docker", "/Applications/Docker.app/Contents/Resources/bin/docker",
		},
		// launchctl loads the launchd jobs for merud and the Google server.
		"launchctl": {"/bin/launchctl"},
		// open starts Ollama.app and Meru.app, and opens config.toml and
		// the help links.
		"open": {"/usr/bin/open"},
		// ditto copies Meru.app and unpacks the Ollama zip, keeping each
		// bundle whole.
		"ditto": {"/usr/bin/ditto"},
		// xattr clears the quarantine mark, when the user says yes.
		"xattr": {"/usr/bin/xattr"},
		// codesign checks the Ollama.app signature after the download.
		"codesign": {"/usr/bin/codesign"},
		// sysctl, sw_vers and df describe the Mac in the first step.
		"sysctl":  {"/usr/sbin/sysctl"},
		"sw_vers": {"/usr/bin/sw_vers"},
		"df":      {"/bin/df"},
	}
}

// Locate returns the first path of program that exists on this Mac, with
// "~/" expanded against home. It fails with ErrNotAllowed for a program
// the allowlist doesn't name, and with ErrMissing when none of its paths
// exists.
func Locate(program, home string) (string, error) {
	paths, ok := Programs()[program]
	if !ok {
		return "", fmt.Errorf("%s: %w", program, ErrNotAllowed)
	}
	for _, p := range paths {
		p = expandHome(p, home)
		// os.Stat follows links: Homebrew's and Docker's commands are
		// links into their own folders.
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", program, ErrMissing)
}

// Runner runs one program from the allowlist with args and waits for it.
// line, when not nil, gets each line the program prints, stdout and
// stderr together, as it comes, so a screen can show live progress. It
// returns the output's last lines, for an error message, and fails when
// the program isn't allowed or installed, can't start, or exits with an
// error.
//
// A Runner is a function type: any function with this signature is one.
// The tests pass a fake that records each call; ExecRunner returns the
// real one.
type Runner func(ctx context.Context, program string, args []string, line func(string)) (string, error)

// tailLines is how many lines of output a Runner keeps for its result.
const tailLines = 40

// ExecRunner returns the Runner that starts programs for real, for the
// user whose home folder is home.
//
// It passes the program a small environment of its own: HOME, USER,
// TMPDIR, LANG and a PATH that holds Homebrew's and Docker's folders, so
// docker finds its credential helper and brew finds its own tools. Nothing
// else from the installer's environment goes along.
func ExecRunner(home string) Runner {
	// A function literal like this one is a closure: it keeps home from
	// the call that made it.
	return func(ctx context.Context, program string, args []string, line func(string)) (string, error) {
		path, err := Locate(program, home)
		if err != nil {
			return "", err
		}
		// exec.CommandContext runs path itself, with args as separate
		// strings; no shell sees them, so no argument can add a command.
		// The child is killed when ctx ends.
		cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- path comes from the fixed allowlist
		cmd.Env = childEnv(home)
		cmd.Dir = home
		out, err := runLines(cmd, line)
		if err != nil {
			return out, fmt.Errorf("%s %s: %w", program, strings.Join(args, " "), err)
		}
		return out, nil
	}
}

// childEnv builds the environment ExecRunner passes each program.
func childEnv(home string) []string {
	dirs := []string{
		"/opt/homebrew/bin", "/usr/local/bin", expandHome("~/.orbstack/bin", home),
		expandHome("~/.docker/bin", home), "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}
	env := []string{
		"HOME=" + home,
		"PATH=" + strings.Join(dirs, ":"),
		"LANG=en_US.UTF-8",
		// Homebrew skips its tips and asks no questions.
		"HOMEBREW_NO_ENV_HINTS=1",
		"NONINTERACTIVE=1",
	}
	for _, k := range []string{"USER", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// runLines starts cmd, hands each line of its output to line, and waits.
// It returns the last tailLines lines.
func runLines(cmd *exec.Cmd, line func(string)) (string, error) {
	// io.Pipe joins a writer to a reader: what the program writes to
	// stdout and stderr comes out of r, in order.
	r, w := io.Pipe()
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		return "", err
	}

	// The goroutine reads lines while the program runs; the WaitGroup
	// lets this function wait for it to finish before returning.
	var (
		wg   sync.WaitGroup
		tail []string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			text := strings.TrimRight(sc.Text(), "\r")
			if line != nil {
				line(text)
			}
			tail = append(tail, text)
			if len(tail) > tailLines {
				tail = tail[1:]
			}
		}
		// Drain the rest if the scanner stopped on a very long line, so
		// the program never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, r)
	}()
	err := cmd.Wait()
	// Closing the writer ends the reader's loop.
	_ = w.Close()
	wg.Wait()
	return strings.Join(tail, "\n"), err
}

// expandHome turns a path that starts with "~/" into one under home.
func expandHome(p, home string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
