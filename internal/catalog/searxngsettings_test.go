// This file tests the SearXNG settings file: what it holds, its mode, and
// that a second write keeps the first.

package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearXNGSettings checks the settings file: JSON on, the address on
// loopback, the limiter off, and a new random secret each time.
func TestSearXNGSettings(t *testing.T) {
	dir := t.TempDir()
	wrote, err := WriteSearXNGSettings(dir)
	if err != nil || !wrote {
		t.Fatalf("WriteSearXNGSettings = %v, %v", wrote, err)
	}
	path := filepath.Join(dir, "settings.yml")
	text := readText(t, path)
	for _, want := range []string{
		"use_default_settings: true",
		"  formats:\n    - html\n    - json\n",
		`base_url: "http://127.0.0.1:8888/"`,
		"limiter: false",
		"docker restart meru-searxng",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings.yml lacks %q:\n%s", want, text)
		}
	}
	secret := secretOf(t, text)
	if len(secret) != 64 {
		t.Errorf("secret %q isn't 64 hex digits", secret)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("settings.yml mode = %v, want 0600", info.Mode().Perm())
	}

	// A second write keeps the file and its secret.
	wrote, err = WriteSearXNGSettings(dir)
	if err != nil || wrote {
		t.Fatalf("second WriteSearXNGSettings = %v, %v; want the file kept", wrote, err)
	}
	if secretOf(t, readText(t, path)) != secret {
		t.Error("a second write changed the secret")
	}

	// Another install gets another secret.
	other, _ := NewSecret()
	if other == secret {
		t.Error("two secrets matched")
	}
}

// secretOf returns the secret_key value in settings text.
func secretOf(t *testing.T, text string) string {
	t.Helper()
	for line := range strings.Lines(text) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "secret_key: "); ok {
			return strings.Trim(v, `"`)
		}
	}
	t.Fatal("no secret_key line")
	return ""
}
