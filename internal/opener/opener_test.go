// This file tests which URLs the opener accepts and the program each
// system runs. No test starts a browser: Open runs only with a refused URL.

package opener

import (
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCheck checks which URLs open: http, https and file, and nothing that
// starts with "-" or holds a space.
func TestCheck(t *testing.T) {
	tests := []struct {
		url string
		ok  bool
	}{
		{"https://example.com/notes/garden-plan", true},
		{"http://example.com/", true},
		{"HTTPS://example.com/", true},
		{"file:///Users/dana/Notes/lisbon.md", true},
		{"javascript:alert(1)", false},
		{"mailto:dana@example.com", false},
		{"ftp://example.com/seeds.txt", false},
		{"wails://wails/index.html", false},
		{"-https://example.com/", false},
		{"--help", false},
		{"https://example.com/a b", false},
		{"https://example.com/\x00", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := Check(tt.url)
			if (err == nil) != tt.ok {
				t.Errorf("Check(%q) = %v, want ok %v", tt.url, err, tt.ok)
			}
		})
	}
}

// TestOpenRefuses checks that Open runs no program for a URL Check
// refuses, and that the error cuts a long URL.
func TestOpenRefuses(t *testing.T) {
	long := "javascript:" + strings.Repeat("a", 200)
	err := Open(long)
	if err == nil || !strings.HasPrefix(err.Error(), "won't open javascript:") {
		t.Fatalf("Open = %v, want a refusal", err)
	}
	if len(err.Error()) > 150 {
		t.Errorf("error is %d bytes long; want the URL cut", len(err.Error()))
	}
}

// TestOpenMissingFile checks that a file link to nothing fails with the
// missing path named, and runs no program.
func TestOpenMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "made up.html")
	err := Open((&url.URL{Scheme: "file", Path: filepath.ToSlash(missing)}).String())
	if err == nil || !strings.Contains(err.Error(), "no such file") || !strings.Contains(err.Error(), "made up.html") {
		t.Errorf("Open = %v, want the missing file named", err)
	}
}

// TestOpenMissingHomeFile checks a ~ link, as models write them: Open
// reads ~ as the home folder and names the full path of the missing file.
func TestOpenMissingHomeFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)        // where os.UserHomeDir looks on macOS and Linux
	t.Setenv("USERPROFILE", home) // and on Windows
	err := Open("file:///~/made%20up.html")
	want := filepath.Join(home, "made up.html")
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Open = %v, want it to name %s", err, want)
	}
}

// TestCommand checks the program each system runs, with the URL as one
// argument of its own.
func TestCommand(t *testing.T) {
	const u = "https://example.com/notes/garden-plan?a=1&b=2"
	tests := []struct {
		goos string
		want []string
	}{
		{"darwin", []string{"open", u}},
		{"linux", []string{"xdg-open", u}},
		{"freebsd", []string{"xdg-open", u}},
		{"windows", []string{"rundll32", "url.dll,FileProtocolHandler", u}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			if got := command(tt.goos, u); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("command(%s) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}
