// This file takes one [[mcp.servers]] block out of config.toml and leaves
// the rest of the file, comments included, as it was.

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

// RemoveServer deletes the [[mcp.servers]] block named name from the config
// file at configPath and returns the text it took out. The block runs from
// its [[mcp.servers]] line to its last key before the next table header,
// and takes with it the comment lines right above it, such as the two Block
// writes, up to a blank line or a bare "#". Comment lines after its last
// key stay: they belong to the next table, or, in a file written from the
// config template, they are the commented examples that follow a server.
// Every other line stays as it is.
//
// The TOML library can't write a file back with its comments, so this
// works on lines of text. To guard against a line it misreads, it writes
// through writeChecked and checks that the result loads and holds the same
// servers, in the same order, minus name. It fails when config doesn't load
// or has no server called name.
func RemoveServer(configPath, name string) (string, error) {
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s doesn't exist, so it has no server %q", configPath, name)
	}
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", configPath, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("fix config.toml before removing a server: %w", err)
	}
	var want []string // the server names that should remain, in order
	found := false
	for _, s := range cfg.MCP.Servers {
		if s.Name == name {
			found = true
			continue
		}
		want = append(want, s.Name)
	}
	if !found {
		return "", fmt.Errorf("%s has no MCP server named %q", configPath, name)
	}

	text, removed, err := cutServer(string(old), name)
	if err != nil {
		return "", err
	}
	err = writeChecked(configPath, text, func(next config.Config) error {
		var got []string
		for _, s := range next.MCP.Servers {
			got = append(got, s.Name)
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("removing %q would leave servers %v, want %v; edit %s by hand", name, got, want, configPath)
		}
		return CheckServers(next.MCP.Servers)
	})
	if err != nil {
		return "", err
	}
	return removed, nil
}

// cutServer returns text without the [[mcp.servers]] block named name, and
// the lines it took out. It fails when no block has that name.
func cutServer(text, name string) (rest, removed string, err error) {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // SplitAfter leaves "" after a final "\n"
	}

	// Walk the headers of the top-level tables. A server block starts at a
	// [[mcp.servers]] line; [mcp.servers.env] and the like stay part of it.
	start, end := -1, len(lines)
	for i, line := range lines {
		key, ok := header(line)
		if !ok || strings.HasPrefix(key, "mcp.servers.") {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if key == "mcp.servers" && blockNamed(lines[i:], name) {
			start = i
		}
	}
	if start < 0 {
		return "", "", fmt.Errorf("no [[mcp.servers]] block named %q", name)
	}

	// Leave every comment after the block's last key in place: walk back
	// over comment and blank lines, in any mix.
	for end > start+1 && (isComment(lines[end-1]) || isBlank(lines[end-1])) {
		end--
	}
	// Take the block's own comments with it. A bare "#" ends them: the
	// config template uses one to part a server's comments from the text
	// above, and Block never writes one.
	for start > 0 && isComment(lines[start-1]) && strings.TrimSpace(lines[start-1]) != "#" {
		start--
	}

	removed = strings.Join(lines[start:end], "")
	kept := append(slices.Clone(lines[:start]), lines[end:]...)
	// Don't leave two blank lines where the block was, or blank lines at
	// the end of the file.
	if start > 0 && start < len(kept) && isBlank(kept[start-1]) && isBlank(kept[start]) {
		kept = slices.Delete(kept, start, start+1)
	}
	for len(kept) > 0 && isBlank(kept[len(kept)-1]) {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, ""), removed, nil
}

// blockNamed reports whether lines, which start at a [[mcp.servers]]
// header, hold the server called name. It parses the lines up to the next
// header with the TOML library, so any way of writing the name counts.
func blockNamed(lines []string, name string) bool {
	var b strings.Builder
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		if _, ok := header(line); ok {
			break
		}
		b.WriteString(line)
	}
	var parsed struct {
		MCP config.MCP `toml:"mcp"`
	}
	if _, err := toml.Decode(b.String(), &parsed); err != nil {
		return false
	}
	return len(parsed.MCP.Servers) == 1 && parsed.MCP.Servers[0].Name == name
}

// header returns the key of a table header line such as "[index]" or
// "[[mcp.servers]]", with spaces taken out, and false for any other line.
// A line inside a multi-line array, such as `["a"],`, doesn't count, since
// a header holds only a key and maybe a comment.
func header(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if i := strings.Index(s, "#"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return "", false
	}
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	key := strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "\t", "")
	for _, r := range key {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.'
		if !ok {
			return "", false
		}
	}
	return key, key != ""
}

// isComment reports whether line holds only a comment.
func isComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// isBlank reports whether line holds nothing but spaces.
func isBlank(line string) bool {
	return strings.TrimSpace(line) == ""
}
