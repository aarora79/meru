// This file holds the URL check and the call to the system's opener: open
// on macOS, xdg-open on Linux and rundll32 on Windows.

package opener

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// timeout bounds how long the opener may run. open, xdg-open and rundll32
// hand the URL to the browser and return; one stuck on a missing display
// shouldn't wait for ever.
const timeout = 10 * time.Second

// shownRunes is how many characters of a refused URL an error shows.
const shownRunes = 60

// Check returns an error when u isn't a URL Meru opens. It must be http,
// https or file, hold no space or control character, and not start with
// "-". The opener gets the URL as an argument, and a program reads an
// argument that starts with "-" as an option, so such a URL could change
// what the program does.
func Check(u string) error {
	if strings.HasPrefix(u, "-") || !allowed(u) {
		return fmt.Errorf("won't open %s: only http, https and file links open", cut(u))
	}
	return nil
}

// allowed reports whether u parses as a URL with an http, https or file
// scheme and holds no space or control character.
func allowed(u string) bool {
	if u == "" {
		return false
	}
	for _, r := range u {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "file":
		return true
	}
	return false
}

// Open checks u with Check, then opens it with the program command picks
// for this machine. It fails when the check fails, or when the program
// can't start, exits with an error or runs past the timeout.
func Open(u string) error {
	if err := Check(u); err != nil {
		return err
	}
	argv := command(runtime.GOOS, u)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// defer runs cancel when this function returns, which frees the timer.
	defer cancel()
	// exec starts the program directly, with no shell, and passes u as one
	// argument, so nothing in it can run as a command. Check has already
	// refused a URL that starts with "-".
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- a fixed program per platform, no shell
	// Standard output and error stay unset, so they go to the null device,
	// and Run doesn't wait on a browser that the opener leaves running.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	return nil
}

// command returns the program, with its arguments, that opens u on goos:
// open on macOS, rundll32 with url.dll's FileProtocolHandler on Windows,
// and xdg-open on Linux and the rest. Each hands the URL to the default
// program for it, as a double-click would.
func command(goos, u string) []string {
	switch goos {
	case "darwin":
		return []string{"open", u}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", u}
	}
	return []string{"xdg-open", u}
}

// cut shortens u for an error message, ending it with "…" when it had to
// cut.
func cut(u string) string {
	if utf8.RuneCountInString(u) <= shownRunes {
		return u
	}
	return string([]rune(u)[:shownRunes-1]) + "…"
}
