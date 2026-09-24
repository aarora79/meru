// This file adds a server block to config.toml without touching the rest of
// the file, and checks the result before it replaces the original.
// RemoveServer, in remove.go, uses the same safe write.

package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/config"
)

// AppendServer adds block, one [[mcp.servers]] entry from Block, to the end
// of the config file at configPath. Everything already in the file, comments
// included, stays as it is: AppendServer adds a blank line and the block as
// plain text. It creates the file (and its folder) when missing.
//
// It writes through writeChecked, which loads the new text and checks the
// server entries before it replaces the file. So a block that would break
// config, or a crash halfway, leaves the original file as it was.
//
// It fails when block doesn't hold exactly one server, when config already
// has a server with that name, or when the result doesn't load.
func AppendServer(configPath, block string) error {
	name, err := blockName(block)
	if err != nil {
		return err
	}

	// Read the file as it is now. A missing file counts as empty.
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("fix config.toml before adding a server: %w", err)
	}
	for _, s := range cfg.MCP.Servers {
		if s.Name == name {
			return fmt.Errorf("config %s already has an MCP server named %q", configPath, name)
		}
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

	return writeChecked(configPath, text, func(next config.Config) error {
		return CheckServers(next.MCP.Servers)
	})
}

// writeChecked replaces the config file at configPath with text, safely.
// It writes text to a temporary file next to the original, loads that with
// config.Load, runs check on the result, and only then renames it over the
// original with mode 0600. So text that would break config, or a crash
// halfway, leaves the original file as it was. It creates the folder when
// missing.
func writeChecked(configPath, text string, check func(config.Config) error) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// The temporary file sits in the same folder as config.toml, because a
	// rename replaces a file in one step only within one disk. CreateTemp
	// makes it with mode 0600.
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// defer runs os.Remove when writeChecked returns. After a successful
	// rename the temporary name is gone and Remove does nothing.
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close() // the write error is the one worth reporting
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	next, err := config.Load(tmp.Name())
	if err != nil {
		return fmt.Errorf("the change would break config.toml: %w", err)
	}
	if err := check(next); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), configPath); err != nil {
		return fmt.Errorf("write config %s: %w", configPath, err)
	}
	return nil
}

// blockName parses block on its own and returns the name of the one server
// it holds. It fails when block isn't valid TOML or holds no server, or
// more than one.
func blockName(block string) (string, error) {
	var parsed struct {
		MCP config.MCP `toml:"mcp"`
	}
	if _, err := toml.Decode(block, &parsed); err != nil {
		return "", fmt.Errorf("server block: %w", err)
	}
	if n := len(parsed.MCP.Servers); n != 1 {
		return "", fmt.Errorf("server block holds %d servers, want 1", n)
	}
	return parsed.MCP.Servers[0].Name, nil
}

// CheckServers checks the rules for [[mcp.servers]] entries that a bad
// entry is most likely to break: a usable name, exactly one of command
// and url, no two entries with one name, no wildcards, and every confirm entry
// also in allow. merud checks the full set when it starts (internal/mcp);
// the thin client can't import that package, so this repeats the rules
// that matter before a write. It returns every problem, joined.
func CheckServers(servers []config.MCPServer) error {
	var errs []error
	seen := map[string]bool{}
	for _, s := range servers {
		if err := CheckName(s.Name); err != nil {
			errs = append(errs, err)
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("mcp server %q: two servers share this name", s.Name))
		}
		seen[s.Name] = true
		if (s.Command == "") == (s.URL == "") {
			errs = append(errs, fmt.Errorf("mcp server %q: set exactly one of command and url", s.Name))
		}
		allowed := map[string]bool{}
		for _, t := range s.Allow {
			if strings.Contains(t, "*") {
				errs = append(errs, fmt.Errorf("mcp server %q: allow %q: wildcards aren't supported; name each tool", s.Name, t))
			}
			allowed[t] = true
		}
		for _, t := range s.Confirm {
			if !allowed[t] {
				errs = append(errs, fmt.Errorf("mcp server %q: confirm %q is not in allow", s.Name, t))
			}
		}
	}
	return errors.Join(errs...)
}

// CheckName fails unless name is 1 to 64 ASCII letters, digits, '-' and
// '_', the rule internal/mcp applies. The name prefixes every tool name, so
// a '.' in it would make "<server>.<tool>" ambiguous.
func CheckName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("mcp server name %q must be 1 to 64 characters", name)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("mcp server name %q: use only letters, digits, '-' and '_'", name)
		}
	}
	return nil
}
