// This file puts text on the system clipboard for /copy and Ctrl-Y. It runs
// the platform's clipboard program, and falls back to OSC 52, an escape
// code that asks the terminal itself to set the clipboard.

package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// copyFunc puts text on the clipboard. It returns how the text got there,
// such as "pbcopy" or "OSC 52", for the notice. The model holds one, so
// tests can pass a fake that touches no real clipboard.
type copyFunc func(text string) (via string, err error)

// copyTimeout bounds how long a clipboard program may run. They finish in
// milliseconds; one stuck waiting on a display server shouldn't hang the
// copy for ever.
const copyTimeout = 5 * time.Second

// clipboard is what systemCopy needs from the machine, as fields so tests
// can swap each one.
type clipboard struct {
	goos string
	// lookPath finds a program on PATH, like exec.LookPath.
	lookPath func(name string) (string, error)
	// run starts argv with text on its standard input and waits for it.
	run func(argv []string, text string) error
	// terminal is where the OSC 52 fallback writes: the terminal.
	terminal io.Writer
}

// systemClipboard returns the clipboard of the machine meru runs on.
func systemClipboard() clipboard {
	return clipboard{goos: runtime.GOOS, lookPath: exec.LookPath, run: runProgram, terminal: os.Stdout}
}

// copy puts text on the clipboard through the first clipboard program it
// finds (clipboardCommand), or, with none installed, through OSC 52. It
// returns the program's name or "OSC 52", and fails when the program
// fails. OSC 52 can't fail from here: the terminal gives no answer, and
// some terminals ignore it, which is why the notice says it was used.
func (c clipboard) copy(text string) (string, error) {
	argv := clipboardCommand(c.goos, c.lookPath)
	if argv == nil {
		if _, err := io.WriteString(c.terminal, osc52(text)); err != nil {
			return "", fmt.Errorf("write OSC 52: %w", err)
		}
		return "OSC 52", nil
	}
	name := programName(argv[0])
	if err := c.run(argv, text); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return name, nil
}

// clipboardCommand returns the program, with its arguments, that copies
// its standard input to the clipboard on goos: pbcopy on macOS, clip.exe
// on Windows, and on Linux and the rest the first of wl-copy (Wayland),
// xclip and xsel (X11) that lookPath finds. The first element is the full
// path lookPath returned. It returns nil when none is installed.
func clipboardCommand(goos string, lookPath func(string) (string, error)) []string {
	var candidates [][]string
	switch goos {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "windows":
		candidates = [][]string{{"clip.exe"}}
	default:
		candidates = [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "-b", "-i"},
		}
	}
	for _, c := range candidates {
		if path, err := lookPath(c[0]); err == nil {
			return append([]string{path}, c[1:]...)
		}
	}
	return nil
}

// programName returns the file name of the program at path, such as
// "pbcopy", for the notice.
func programName(path string) string {
	// The path may use either separator, whatever system the tests run on.
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// runProgram starts argv with text on its standard input and waits for it
// to finish. It starts the program directly, with no shell, so nothing in
// text can run as a command.
func runProgram(argv []string, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
	defer cancel()
	// argv[0] is a clipboard program clipboardCommand found on PATH from
	// its fixed list; the model's text only goes to its standard input.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- a fixed program list, no shell
	cmd.Stdin = strings.NewReader(text)
	// Standard output and error stay unset, so they go to the null device.
	// xclip and wl-copy leave a child running to hold the clipboard; had
	// they a pipe to write to, Run would wait for that child to exit.
	return cmd.Run()
}

// osc52 returns the OSC 52 escape code that asks the terminal to put text
// on the clipboard: ESC ] 52 ; c ; then the text in base64, then BEL. "c"
// names the clipboard, as opposed to the X11 primary selection. It works
// over SSH too, since the terminal on the user's side does the copying,
// but only in terminals that allow it.
func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}
