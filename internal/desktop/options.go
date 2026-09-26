// This file fills in the Bridge's options from the user's Meru home: where
// merud's socket is, which model writes the answers, the home folder and
// Meru's own folder.

package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/config"
)

// DefaultOptions returns the Options for the Meru home in ~/.meru. socket,
// when not "", overrides the socket path, as the -socket flag does.
//
// It reads config.toml only to name the answer model in the status block
// and to find the output folder, and never writes it. A config that doesn't load leaves the model out
// instead of stopping the app; merud reports config errors when it starts.
// It fails only when the home folder can't be found and no socket was
// given.
func DefaultOptions(socket string) (Options, error) {
	o := Options{Socket: socket}
	// A home folder the app can't find leaves source paths that start
	// with ~ unopenable, but nothing else.
	o.Home, _ = os.UserHomeDir()
	dir, err := config.DefaultDir()
	if err == nil {
		o.Dir = dir
	}
	if o.Socket == "" {
		if err != nil {
			return Options{}, err
		}
		o.Socket = filepath.Join(dir, "merud.sock")
	}
	if path, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(path); err == nil {
			o.Model, o.Fast, o.Embed = cfg.Models.Main, cfg.Models.Fast, cfg.Models.Embed
			o.OutputDir = expandTilde(cfg.Skills.OutputDir, o.Home)
		}
	}
	return o, nil
}

// expandTilde turns "~/x" into a path under home, and leaves any other
// path as it is. config.Load has checked that output_dir is absolute or
// starts with "~". With no home, a "~" path stays unexpanded, and no file
// matches it.
func expandTilde(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`)) {
		return filepath.Join(home, p[1:])
	}
	return p
}
