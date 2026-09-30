// This file holds the settings.yml Meru writes for the SearXNG it runs
// in Docker, and the function that writes it once, before merud's
// SearXNG connector first starts its container. It lives here, next to
// CheckSearXNG, because the Mac installer, which may not import
// internal/connectors, reads the same address. See ARCHITECTURE.md,
// "SearXNG and Ollama".

package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The container Meru runs SearXNG in, and where it answers.
const (
	// SearXNGContainer is the container's name, so a second start finds
	// it. The connector's manifest ID, searxng, makes the same name:
	// "meru-" and the ID.
	SearXNGContainer = "meru-searxng"
	// SearXNGURL is where Meru reaches it: the default [web] searxng_url.
	// Docker publishes the container on this machine's loopback address
	// only, so no other machine on the network can use it.
	SearXNGURL = "http://127.0.0.1:8888"
)

// NewSecret returns 32 random bytes as 64 hex digits, for SearXNG's
// secret_key, which signs its cookies. It fails only when the system's
// random source does.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make a secret key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// SearXNGSettings returns the settings.yml Meru writes, with secret as the
// secret_key. use_default_settings keeps SearXNG's own defaults for
// everything this file doesn't name.
func SearXNGSettings(secret string) string {
	return `# SearXNG settings, written by Meru.
# SearXNG reads this file as /etc/searxng/settings.yml in its container.
# Change it, then restart the container: docker restart ` + SearXNGContainer + `
use_default_settings: true

general:
  instance_name: "Meru web search"

search:
  # 1 filters adult results. 0 turns the filter off, 2 makes it strict.
  safe_search: 1
  autocomplete: ""
  # Meru asks for JSON. SearXNG answers only HTML unless json is here.
  formats:
    - html
    - json

server:
  # Docker publishes the container on 127.0.0.1:8888 only.
  base_url: "` + SearXNGURL + `/"
  # Signs SearXNG's cookies. Meru made it at random.
  secret_key: "` + secret + `"
  # The limiter guards a public server from bots. Only this computer uses
  # this one, and the limiter would need a Valkey database besides.
  limiter: false
  public_instance: false
  image_proxy: false
`
}

// WriteSearXNGSettings writes settings.yml into dir with a new secret,
// mode 0600, unless the file is already there. A second call keeps the
// file, and with it the secret and any change the user made. It reports
// whether it wrote the file.
func WriteSearXNGSettings(dir string) (bool, error) {
	path := filepath.Join(dir, "settings.yml")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("check %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	secret, err := NewSecret()
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(SearXNGSettings(secret)), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
