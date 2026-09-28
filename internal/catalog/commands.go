// This file adds a [[commands]] entry to config.toml without touching the
// rest of the file, the way AppendServer adds a server. The Mac installer
// uses it to turn on the sample commands the user ticks.

package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/config"
)

// AppendCommand adds block, the text of one [[commands]] entry with its
// params, to the end of the config file at configPath, after a blank
// line. Every other line stays as it was.
//
// It writes through writeChecked, so the file changes only when the result
// loads and holds the same commands as before plus this one, in order. It
// fails when block doesn't hold exactly one command, when config already
// has a command with its name, or when the result doesn't load.
func AppendCommand(configPath, block string) error {
	name, err := commandName(block)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	before, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("fix config.toml before adding a command: %w", err)
	}
	names := commandNames(before)
	if slices.Contains(names, name) {
		return fmt.Errorf("config %s already has a command named %q", configPath, name)
	}

	text := string(old)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	text += block
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	want := append(names, name)
	return writeChecked(configPath, text, func(next config.Config) error {
		if got := commandNames(next); !slices.Equal(got, want) {
			return fmt.Errorf("adding %q would leave the commands %v, want %v; edit %s by hand", name, got, want, configPath)
		}
		return nil
	})
}

// commandName parses block on its own and returns the name of the one
// command it holds. It fails when block isn't valid TOML or holds no
// command, or more than one.
func commandName(block string) (string, error) {
	var parsed struct {
		Commands []config.Command `toml:"commands"`
	}
	if _, err := toml.Decode(block, &parsed); err != nil {
		return "", fmt.Errorf("command block: %w", err)
	}
	if n := len(parsed.Commands); n != 1 {
		return "", fmt.Errorf("command block holds %d commands, want 1", n)
	}
	if parsed.Commands[0].Name == "" {
		return "", errors.New("command block has no name")
	}
	return parsed.Commands[0].Name, nil
}

// commandNames lists cfg's commands by name, in file order.
func commandNames(cfg config.Config) []string {
	var names []string
	for _, c := range cfg.Commands {
		names = append(names, c.Name)
	}
	return names
}
