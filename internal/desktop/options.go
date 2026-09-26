// This file fills in the Bridge's options from the user's Meru home: where
// merud's socket is, which model writes the answers, and the home folder.

package desktop

import (
	"os"
	"path/filepath"

	"github.com/aarora79/meru/internal/config"
)

// DefaultOptions returns the Options for the Meru home in ~/.meru. socket,
// when not "", overrides the socket path, as the -socket flag does.
//
// It reads config.toml only to name the answer model in the status block,
// and never writes it. A config that doesn't load leaves the model out
// instead of stopping the app; merud reports config errors when it starts.
// It fails only when the home folder can't be found and no socket was
// given.
func DefaultOptions(socket string) (Options, error) {
	o := Options{Socket: socket}
	// A home folder the app can't find leaves source paths that start
	// with ~ unopenable, but nothing else.
	o.Home, _ = os.UserHomeDir()
	if o.Socket == "" {
		dir, err := config.DefaultDir()
		if err != nil {
			return Options{}, err
		}
		o.Socket = filepath.Join(dir, "merud.sock")
	}
	if path, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(path); err == nil {
			o.Model = cfg.Models.Main
		}
	}
	return o, nil
}
