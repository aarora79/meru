// This file tests loading, resolving, redacting and setting secrets.

package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFile writes body to a secrets file with the given mode and returns
// its path.
func writeFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode passes through the umask; Chmod sets it exactly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		mode    os.FileMode
		wantErr string // "" means Load succeeds
	}{
		{"good file", "obsidian_api_key = \"fake-key-0123456789\"\n", 0o600, ""},
		{"empty file", "", 0o600, ""},
		{"not a string", "port = 27124\n", 0o600, "quoted string"},
		{"bad name", "\"bad name\" = \"x\"\n", 0o600, "letters, digits"},
		{"not toml", "this is not toml", 0o600, "read secrets"},
		{"group can read", "k = \"v\"\n", 0o640, "chmod 600"},
		{"others can read", "k = \"v\"\n", 0o604, "chmod 600"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(tt.wantErr, "chmod") {
				t.Skip("Windows has no Unix mode bits")
			}
			_, err := Load(writeFile(t, tt.body, tt.mode))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Load: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Load error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Has("anything") {
		t.Error("an empty Secrets says it has an entry")
	}
}

func TestResolve(t *testing.T) {
	s, err := Load(writeFile(t, "obsidian_api_key = \"fake-key-0123456789\"\nempty = \"\"\n", 0o600))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"secret:obsidian_api_key", "fake-key-0123456789", false},
		{"plain value", "plain value", false},
		{"", "", false},
		{"secret:missing", "", true},
		{"secret:empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := s.Resolve(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve(%q) error = %v, want error %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if err != nil && !strings.Contains(err.Error(), "secret") {
				t.Errorf("error %q doesn't name the secret", err)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	s := &Secrets{values: map[string]string{
		"long":  "fake-key-0123456789",
		"outer": "fake-key-0123456789-and-more",
		"short": "abc", // under 8 characters: left alone
	}}
	tests := []struct {
		in, want string
	}{
		{"key fake-key-0123456789 here", "key [secret:long] here"},
		{"fake-key-0123456789-and-more", "[secret:outer]"},
		{"abc stays", "abc stays"},
		{"nothing to hide", "nothing to hide"},
	}
	for _, tt := range tests {
		if got := s.Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	var zero Secrets
	if got := zero.Redact("text"); got != "text" {
		t.Errorf("zero Secrets Redact = %q", got)
	}
}

func TestSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	path := Path(dir)

	if err := Set(path, "first", "fake-value-1111"); err != nil {
		t.Fatalf("Set on a missing file: %v", err)
	}
	if err := Set(path, "second", "  fake-value-2222\n"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Set(path, "first", "fake-value-3333"); err != nil {
		t.Fatalf("Set to replace: %v", err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Set: %v", err)
	}
	for name, want := range map[string]string{"first": "fake-value-3333", "second": "fake-value-2222"} {
		if got, _ := s.Resolve(Prefix + name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %#o, want 0600", perm)
		}
	}
	// No temporary file stays behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("folder holds %d files, want only secrets.toml", len(entries))
	}
}

func TestSetRefuses(t *testing.T) {
	path := Path(t.TempDir())
	tests := []struct{ name, value string }{
		{"has space", "v"},
		{"", "v"},
		{"dot.name", "v"},
		{"ok", "   "},
	}
	for _, tt := range tests {
		if err := Set(path, tt.name, tt.value); err == nil {
			t.Errorf("Set(%q, %q) succeeded, want an error", tt.name, tt.value)
		}
	}
}

func TestSetRepairsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix mode bits")
	}
	path := writeFile(t, "old = \"fake-value-0000\"\n", 0o644)
	if err := Set(path, "new", "fake-value-1111"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Set: %v", err)
	}
	if !s.Has("old") || !s.Has("new") {
		t.Error("Set lost an entry")
	}
}
