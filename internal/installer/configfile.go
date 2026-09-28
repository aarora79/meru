// This file holds where things live under the home folder, and the one
// function that creates config.toml from the template when it doesn't
// exist. Every later change to config.toml goes through internal/catalog,
// which edits one list or string and keeps the comments.

package installer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/config"
)

// Paths lists where the installer puts things for the user whose home
// folder is Home. Tests point Home at a temporary folder.
type Paths struct {
	Home string
}

// MeruDir is Meru's home, ~/.meru.
func (p Paths) MeruDir() string { return filepath.Join(p.Home, ".meru") }

// Config is ~/.meru/config.toml.
func (p Paths) Config() string { return filepath.Join(p.MeruDir(), "config.toml") }

// Socket is merud's socket, ~/.meru/merud.sock.
func (p Paths) Socket() string { return filepath.Join(p.MeruDir(), "merud.sock") }

// Memory is the memory folder, ~/.meru/memory.
func (p Paths) Memory() string { return filepath.Join(p.MeruDir(), "memory") }

// SearXNG is the folder that holds SearXNG's settings.yml, ~/.meru/searxng.
func (p Paths) SearXNG() string { return filepath.Join(p.MeruDir(), "searxng") }

// Bin is where meru and merud go, ~/.local/bin.
func (p Paths) Bin() string { return filepath.Join(p.Home, ".local", "bin") }

// LaunchAgents is the user's launchd folder, ~/Library/LaunchAgents.
func (p Paths) LaunchAgents() string { return filepath.Join(p.Home, "Library", "LaunchAgents") }

// Workspace is where the Google server's start script lives,
// ~/.config/workspace-mcp, as docs/google-setup.md puts it.
func (p Paths) Workspace() string { return filepath.Join(p.Home, ".config", "workspace-mcp") }

// Tilde writes path with "~" for the home folder, as config.toml and the
// screens show paths: /Users/dana/notes becomes ~/notes.
func (p Paths) Tilde(path string) string {
	if path == p.Home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, p.Home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}

// EnsureConfig writes the config template to path when no file is there,
// and reports whether it wrote one. The template holds every key with its
// default and its comments, so the user has one file that explains
// itself. An existing file stays as it is. It fails when the file can't be
// read or written, or when an existing one doesn't load: every later edit
// needs a config that loads, and the message names the bad key.
func EnsureConfig(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		if _, err := config.Load(path); err != nil {
			return false, fmt.Errorf("%s has a problem; fix it or move it away, then try again: %w", path, err)
		}
		return false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("check %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	// The template goes to a temporary file first and has to load before
	// it takes the real name, as every config write in Meru does.
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return false, fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(config.Template()); err != nil {
		_ = tmp.Close() // the write error is the one to report
		return false, fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("write config: %w", err)
	}
	if _, err := config.Load(tmp.Name()); err != nil {
		return false, fmt.Errorf("the config template doesn't load: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}

// loadConfig makes sure config.toml exists and returns what it holds.
func loadConfig(path string) (config.Config, error) {
	if _, err := EnsureConfig(path); err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
}
