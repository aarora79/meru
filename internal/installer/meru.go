// This file holds the "Install Meru" step: it copies meru and merud to
// ~/.local/bin and Meru.app to the Applications folder, from the payload
// inside the installer's own bundle, clears the quarantine mark when the
// user says yes, and adds ~/.local/bin to PATH in ~/.zshrc when asked.

package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MeruInput is what the user chose on the Install Meru screen.
type MeruInput struct {
	// ClearQuarantine clears macOS's quarantine mark from what this step
	// installs, so macOS doesn't stop Meru.app and merud the first time.
	ClearQuarantine bool `json:"clearQuarantine"`
	// AddPath adds ~/.local/bin to PATH in ~/.zshrc.
	AddPath bool `json:"addPath"`
}

// pathLine is the line AddPath appends to ~/.zshrc. $HOME stays as it is,
// for zsh to expand.
const pathLine = `export PATH="$HOME/.local/bin:$PATH"`

// quarantine is the extended attribute macOS puts on files that came from
// the internet, which makes Gatekeeper check them on first open.
const quarantine = "com.apple.quarantine"

// programNames returns the two command-line programs in the payload.
func programNames() []string { return []string{"meru", "merud"} }

// AppsDir returns the Applications folder Meru.app goes in:
// /Applications when this user can write there, as an administrator can,
// and ~/Applications otherwise.
func AppsDir(home string) string {
	if writable("/Applications") {
		return "/Applications"
	}
	return filepath.Join(home, "Applications")
}

// writable reports whether this process can create a file in dir.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".meru-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// MeruFound says what of this step is already in place, for a second run:
// "" when nothing is, or a sentence for the screen.
func MeruFound(p Paths, payload, apps string) string {
	var have []string
	for _, name := range programNames() {
		if sameFile(filepath.Join(payload, name), filepath.Join(p.Bin(), name)) {
			have = append(have, name)
		}
	}
	if len(have) == len(programNames()) {
		msg := "meru and merud from this installer are in " + p.Tilde(p.Bin()) + "."
		if _, err := os.Stat(filepath.Join(apps, "Meru.app")); err == nil {
			msg += " Meru.app is in " + p.Tilde(apps) + "."
		}
		return msg
	}
	return ""
}

// sameFile reports whether a and b both exist and hold the same bytes.
func sameFile(a, b string) bool {
	x, err := os.ReadFile(a) // #nosec G304 -- the installer's own payload
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b) // #nosec G304 -- the program this installer put there
	return err == nil && bytes.Equal(x, y)
}

// InstallMeru copies meru, merud and Meru.app from payload, the folder
// inside the installer's bundle that holds them, and returns what it did
// in a few lines for the screen. apps is the Applications folder
// (AppsDir). say gets a line of news for each part.
//
// ditto copies each one, because it copies an app bundle whole, and it
// keeps the quarantine mark a downloaded disk image puts on everything
// inside it. So the mark goes only when the user ticks the box, and then
// xattr clears it from what this step installed and nothing else.
//
// It fails when the payload is missing a part, or a copy fails.
func InstallMeru(ctx context.Context, run Runner, p Paths, payload, apps string, in MeruInput, say func(string)) (string, error) {
	var done []string
	bin := p.Bin()
	if err := os.MkdirAll(bin, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", bin, err)
	}
	var installed []string
	for _, name := range programNames() {
		src := filepath.Join(payload, name)
		if _, err := os.Stat(src); err != nil {
			return "", fmt.Errorf("the installer is missing %s; download the disk image again: %w", name, err)
		}
		dst := filepath.Join(bin, name)
		say("Copying " + name + " to " + p.Tilde(bin))
		// The copy takes a new name and then replaces the old file in one
		// step. Writing over a program that runs, such as a merud started
		// earlier, would make macOS stop it.
		next := dst + ".new"
		if _, err := run(ctx, "ditto", []string{src, next}, nil); err != nil {
			return "", err
		}
		if err := os.Chmod(next, 0o755); err != nil { // #nosec G302 -- a program must be runnable
			return "", fmt.Errorf("make %s runnable: %w", dst, err)
		}
		if err := os.Rename(next, dst); err != nil {
			return "", fmt.Errorf("put %s in place: %w", dst, err)
		}
		installed = append(installed, dst)
	}
	done = append(done, "Put meru and merud in "+p.Tilde(bin)+".")

	appSrc := filepath.Join(payload, "Meru.app")
	if _, err := os.Stat(appSrc); err == nil {
		if err := os.MkdirAll(apps, 0o750); err != nil {
			return "", fmt.Errorf("create %s: %w", apps, err)
		}
		dst := filepath.Join(apps, "Meru.app")
		say("Copying Meru.app to " + p.Tilde(apps))
		if err := replaceApp(ctx, run, appSrc, dst); err != nil {
			return "", err
		}
		installed = append(installed, dst)
		done = append(done, "Put Meru.app in "+p.Tilde(apps)+".")
	}

	if in.ClearQuarantine {
		say("Clearing the quarantine mark")
		for _, path := range installed {
			// -d removes one attribute and -r walks into the app bundle.
			// xattr fails when the mark isn't there, which is fine.
			_, _ = run(ctx, "xattr", []string{"-d", "-r", quarantine, path}, nil)
		}
		done = append(done, "Cleared the quarantine mark, so macOS opens them without asking.")
	} else {
		done = append(done, "Left the quarantine mark. The first time, right-click Meru.app and choose Open. "+
			"If merud won't start, run: xattr -d "+quarantine+" "+p.Tilde(filepath.Join(bin, "merud")))
	}

	if msg, err := ensurePath(p, in.AddPath); err != nil {
		return "", err
	} else if msg != "" {
		done = append(done, msg)
	}
	return strings.Join(done, "\n"), nil
}

// replaceApp copies the app at src to dst, over any older copy. It copies
// to a new name beside dst first and swaps it in, so a failed copy leaves
// the old app as it was.
func replaceApp(ctx context.Context, run Runner, src, dst string) error {
	next := dst + ".new"
	old := dst + ".old"
	_ = os.RemoveAll(next)
	if _, err := run(ctx, "ditto", []string{src, next}, nil); err != nil {
		_ = os.RemoveAll(next)
		return err
	}
	_ = os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.RemoveAll(next)
		return fmt.Errorf("move the old Meru.app aside: %w", err)
	}
	if err := os.Rename(next, dst); err != nil {
		_ = os.Rename(old, dst) // put the old one back
		return fmt.Errorf("put Meru.app in place: %w", err)
	}
	_ = os.RemoveAll(old)
	return nil
}

// ensurePath adds pathLine to ~/.zshrc when add is true and the file
// doesn't already put ~/.local/bin on PATH. It returns a line for the
// screen, or "" when there is nothing to say.
func ensurePath(p Paths, add bool) (string, error) {
	rc := filepath.Join(p.Home, ".zshrc")
	text, err := os.ReadFile(rc) // #nosec G304 -- the user's own ~/.zshrc
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", rc, err)
	}
	if strings.Contains(string(text), ".local/bin") {
		return "", nil
	}
	if !add {
		return "To use meru in Terminal, add this line to ~/.zshrc: " + pathLine, nil
	}
	// O_APPEND adds to the end and never overwrites what is there.
	f, err := os.OpenFile(rc, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644) // #nosec G302 G304 -- ~/.zshrc, readable as zsh expects
	if err != nil {
		return "", fmt.Errorf("open %s: %w", rc, err)
	}
	prefix := "\n"
	if len(text) == 0 || bytes.HasSuffix(text, []byte("\n\n")) {
		prefix = ""
	}
	_, werr := f.WriteString(prefix + "# Meru's programs, meru and merud, added by the Meru installer.\n" + pathLine + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write %s: %w", rc, werr)
	}
	return "Added ~/.local/bin to PATH in ~/.zshrc. Open a new Terminal window to use meru.", nil
}
