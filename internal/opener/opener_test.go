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

func TestFilePath(t *testing.T) {
	tests := map[string]string{
		"file:///Users/dana/a%20b.md": "/Users/dana/a b.md",
		"file:///C:/Users/dana/a.md":  "C:/Users/dana/a.md",
	}
	for in, want := range tests {
		if got, ok := filePath(in); !ok || got != want {
			t.Errorf("filePath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := filePath("https://example.com/a"); ok {
		t.Error("filePath took an https link")
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
