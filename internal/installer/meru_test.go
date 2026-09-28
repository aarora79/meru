// This file tests the Install Meru step with a fake ditto that copies
// with Go: where each part goes, the quarantine choice, the PATH line, and
// a second run.

package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyRunner is a fake Runner whose ditto copies a file or a folder, as
// the real one does, and whose other programs do nothing.
func copyRunner() *fakeRunner {
	return &fakeRunner{answer: func(program string, args []string, _ func(string)) (string, error) {
		if program != "ditto" {
			return "", nil
		}
		src, dst := args[0], args[1]
		info, err := os.Stat(src)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", os.CopyFS(dst, os.DirFS(src))
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(dst, data, 0o644)
	}}
}

// payload makes an installer payload: meru, merud and Meru.app.
func payload(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meru"), "meru v2")
	writeFile(t, filepath.Join(dir, "merud"), "merud v2")
	writeFile(t, filepath.Join(dir, "Meru.app", "Contents", "Info.plist"), "<plist/>")
	return dir
}

// TestInstallMeru installs over an older copy and checks each part, the
// quarantine calls for each choice, and the PATH line.
func TestInstallMeru(t *testing.T) {
	for _, clear := range []bool{true, false} {
		name := map[bool]string{true: "clear the mark", false: "keep the mark"}[clear]
		t.Run(name, func(t *testing.T) {
			p := Paths{Home: t.TempDir()}
			apps := filepath.Join(p.Home, "Applications")
			src := payload(t)
			writeFile(t, filepath.Join(p.Bin(), "merud"), "merud v1")
			writeFile(t, filepath.Join(apps, "Meru.app", "Contents", "old"), "old")

			if MeruFound(p, src, apps) != "" {
				t.Error("found an install before the step ran")
			}
			r := copyRunner()
			msg, err := InstallMeru(context.Background(), r.run, p, src, apps, MeruInput{ClearQuarantine: clear, AddPath: true}, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(p.Bin(), "merud")); got != "merud v2" {
				t.Errorf("merud = %q", got)
			}
			if info, _ := os.Stat(filepath.Join(p.Bin(), "meru")); info.Mode().Perm() != 0o755 {
				t.Errorf("meru mode = %v", info.Mode().Perm())
			}
			if _, err := os.Stat(filepath.Join(apps, "Meru.app", "Contents", "old")); err == nil {
				t.Error("the old Meru.app's files stayed")
			}
			if _, err := os.Stat(filepath.Join(apps, "Meru.app", "Contents", "Info.plist")); err != nil {
				t.Error("the new Meru.app isn't there")
			}
			xattrs := 0
			for _, c := range r.called() {
				if strings.HasPrefix(c, "xattr -d -r com.apple.quarantine ") {
					xattrs++
				}
			}
			if want := map[bool]int{true: 3, false: 0}[clear]; xattrs != want {
				t.Errorf("xattr ran %d times, want %d: %v", xattrs, want, r.called())
			}
			if !clear && !strings.Contains(msg, "right-click Meru.app") {
				t.Errorf("result doesn't say how to open the app: %q", msg)
			}
			if MeruFound(p, src, apps) == "" {
				t.Error("a second run wouldn't see this install")
			}
		})
	}
}

// TestEnsurePath checks the PATH line: added once, not added when the user
// says no, and left alone when ~/.zshrc already has it.
func TestEnsurePath(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	rc := filepath.Join(p.Home, ".zshrc")
	writeFile(t, rc, "alias ll='ls -l'\n")

	if msg, err := ensurePath(p, false); err != nil || !strings.Contains(msg, pathLine) {
		t.Errorf("no: %q, %v", msg, err)
	}
	if strings.Contains(readFile(t, rc), pathLine) {
		t.Error("added the line after a no")
	}
	for range 2 {
		if _, err := ensurePath(p, true); err != nil {
			t.Fatal(err)
		}
	}
	text := readFile(t, rc)
	if strings.Count(text, pathLine) != 1 || !strings.HasPrefix(text, "alias ll='ls -l'\n") {
		t.Errorf(".zshrc =\n%s", text)
	}
}

// TestInstallMeruMissingPayload checks that a broken disk image fails the
// step with a message that says what to do.
func TestInstallMeruMissingPayload(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	_, err := InstallMeru(context.Background(), copyRunner().run, p, t.TempDir(), t.TempDir(), MeruInput{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "download the disk image again") {
		t.Errorf("err = %v", err)
	}
}
