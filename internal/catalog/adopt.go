// This file holds the two config.toml edits behind Adopt and its undo.
// AdoptServer comments out a hand-added [[mcp.servers]] entry between two
// marker lines and puts a [connectors.<id>] table right after it;
// UnadoptServer takes the table out and puts the entry back, byte for
// byte. Like RemoveServer, both work on lines of text, so every comment
// stays, and both write through writeChecked. internal/connectors decides
// what the table holds; see ARCHITECTURE.md, "Moving to connectors".

package catalog

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// adoptedHeader is the marker line above an entry Adopt commented out,
// such as "# adopted by merud on 2026-09-30; meru mcp unadopt obsidian
// restores it".
func adoptedHeader(id string, day time.Time) string {
	return "# adopted by merud on " + day.Format("2006-01-02") + "; meru mcp unadopt " + id + " restores it\n"
}

// adoptedFooter is the marker line below the commented-out entry. A second
// form says the entry's last line had no line break, which was then the
// file's end, so the undo can leave it without one again.
func adoptedFooter(id string, noNewline bool) string {
	if noNewline {
		return "# end of the adopted " + id + " entry, which ended the file with no line break\n"
	}
	return "# end of the adopted " + id + " entry\n"
}

// isAdoptedHeader reports whether line is adoptedHeader(id, some day).
func isAdoptedHeader(line, id string) bool {
	rest, ok := strings.CutPrefix(line, "# adopted by merud on ")
	if !ok || len(rest) < len("2006-01-02") {
		return false
	}
	if _, err := time.Parse("2006-01-02", rest[:len("2006-01-02")]); err != nil {
		return false
	}
	return rest[len("2006-01-02"):] == "; meru mcp unadopt "+id+" restores it\n"
}

// commentLine turns one line of the entry into a comment: "# " in front,
// or "#" alone for an empty line, so that uncommentLine gets the line back
// as it was.
func commentLine(line string) string {
	if line == "\n" {
		return "#\n"
	}
	return "# " + line
}

// uncommentLine undoes commentLine. ok is false for a line commentLine
// can't have written.
func uncommentLine(line string) (string, bool) {
	if line == "#\n" {
		return "\n", true
	}
	return strings.CutPrefix(line, "# ")
}

// ConnectorTable returns the [connectors.<id>] table Adopt writes:
// enabled = true, then each value in values as a string and each list in
// lists, in key order. Every key takes one line, which UnadoptServer
// relies on.
func ConnectorTable(id string, values map[string]string, lists map[string][]string) string {
	var b strings.Builder
	b.WriteString("[connectors." + id + "]\n")
	b.WriteString("enabled = true\n")
	keys := make([]string, 0, len(values)+len(lists))
	for k := range values {
		keys = append(keys, k)
	}
	for k := range lists {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if items, ok := lists[k]; ok {
			b.WriteString(k + " = " + list(items) + "\n")
			continue
		}
		b.WriteString(k + " = " + quote(values[k]) + "\n")
	}
	return b.String()
}

// AdoptServer comments out the [[mcp.servers]] entry named id in the
// config file at configPath, with its own comments, between an
// adoptedHeader and an adoptedFooter line dated day, and puts table, a
// [connectors.<id>] table from ConnectorTable, right after it. Every
// other line stays as it was. It returns the lines it commented out.
//
// It writes once, through writeChecked, and checks that the result loads,
// that the servers are the same ones in the same order but for id, and
// that [connectors.<id>] turns the connector on. It fails when config
// doesn't load, has no entry named id, or already has a [connectors.<id>]
// table.
func AdoptServer(configPath, id string, day time.Time, table string) (string, error) {
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", configPath, err)
	}
	before, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("fix config.toml before adopting a server: %w", err)
	}
	if _, ok := before.Connectors[id]; ok {
		return "", fmt.Errorf("%s already has [connectors.%s]; take it out, then adopt again", configPath, id)
	}
	lines := splitLines(string(old))
	start, end, ok := serverSpan(lines, id)
	if !ok {
		return "", fmt.Errorf("%s has no [[mcp.servers]] entry named %q", configPath, id)
	}

	block := lines[start:end]
	last := block[len(block)-1]
	noNewline := !strings.HasSuffix(last, "\n")
	out := []string{adoptedHeader(id, day)}
	for i, line := range block {
		if i == len(block)-1 && noNewline {
			line += "\n"
		}
		out = append(out, commentLine(line))
	}
	out = append(out, adoptedFooter(id, noNewline), table)
	text := strings.Join(slices.Concat(lines[:start], out, lines[end:]), "")

	var want []string // the server names that should remain, in order
	for _, s := range before.MCP.Servers {
		if s.Name != id {
			want = append(want, s.Name)
		}
	}
	err = writeChecked(configPath, text, func(next config.Config) error {
		if got := serverNames(next); !slices.Equal(got, want) {
			return fmt.Errorf("adopting %q would leave servers %v, want %v; edit %s by hand", id, got, want, configPath)
		}
		if on, _ := next.Connectors[id].Enabled(); !on {
			return fmt.Errorf("adopting %q didn't turn [connectors.%s] on; edit %s by hand", id, id, configPath)
		}
		return CheckServers(next.MCP.Servers)
	})
	if err != nil {
		return "", err
	}
	return strings.Join(block, ""), nil
}

// ErrNotAdopted means config.toml holds no entry that AdoptServer
// commented out for the connector.
var ErrNotAdopted = errors.New("config.toml holds no adopted entry for it")

// UnadoptServer undoes AdoptServer for connector id in the config file at
// configPath: it takes out the [connectors.<id>] table after the
// commented-out entry, up to its last key, and puts the entry back in its
// place as it was, without the marker lines. After an adopt and an
// unadopt with no edit between them, the file is the same, byte for byte.
// It returns the lines it restored.
//
// It writes through writeChecked and checks that the entry named id is
// back, the other servers stay in order, and [connectors.<id>] is gone.
// It fails with ErrNotAdopted when there is no adoptedHeader for id, and
// with another error when config doesn't load or the marked lines have
// been edited so that it can't tell what to put back.
func UnadoptServer(configPath, id string) (string, error) {
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", configPath, err)
	}
	before, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("fix config.toml before restoring a server: %w", err)
	}
	lines := splitLines(string(old))
	start := slices.IndexFunc(lines, func(l string) bool { return isAdoptedHeader(l, id) })
	if start < 0 {
		return "", ErrNotAdopted
	}
	plain, noBreak := adoptedFooter(id, false), adoptedFooter(id, true)
	foot := -1
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == plain || lines[i] == noBreak {
			foot = i
			break
		}
	}
	if foot < 0 {
		return "", fmt.Errorf("%s has the line that marks the adopted %s entry, but not the one that ends it; restore the entry by hand", configPath, id)
	}
	var block []string
	for _, line := range lines[start+1 : foot] {
		back, ok := uncommentLine(line)
		if !ok {
			return "", fmt.Errorf("the adopted %s entry in %s has a line that isn't a comment: %q; restore the entry by hand", id, configPath, strings.TrimSpace(line))
		}
		block = append(block, back)
	}
	if lines[foot] == noBreak && len(block) > 0 {
		block[len(block)-1] = strings.TrimSuffix(block[len(block)-1], "\n")
	}

	// The table Adopt wrote follows the footer. It runs to the next
	// header; the comments and blank lines at its end were there before
	// Adopt and stay.
	after := foot + 1
	if after < len(lines) {
		if key, ok := header(lines[after]); ok && key == "connectors."+id && !strings.HasPrefix(strings.TrimSpace(lines[after]), "[[") {
			end := len(lines)
			for i := after + 1; i < len(lines); i++ {
				if _, ok := header(lines[i]); ok {
					end = i
					break
				}
			}
			for end > after+1 && (isComment(lines[end-1]) || isBlank(lines[end-1])) {
				end--
			}
			after = end
		}
	}
	text := strings.Join(slices.Concat(lines[:start], block, lines[after:]), "")

	err = writeChecked(configPath, text, func(next config.Config) error {
		if !slices.Contains(serverNames(next), id) {
			return fmt.Errorf("restoring %q left no server by that name; edit %s by hand", id, configPath)
		}
		if _, ok := next.Connectors[id]; ok {
			return fmt.Errorf("restoring %q left [connectors.%s] in place; edit %s by hand", id, id, configPath)
		}
		var others []string
		for _, n := range serverNames(next) {
			if n != id {
				others = append(others, n)
			}
		}
		if !slices.Equal(others, serverNames(before)) {
			return fmt.Errorf("restoring %q would change the other servers; edit %s by hand", id, configPath)
		}
		return CheckServers(next.MCP.Servers)
	})
	if err != nil {
		return "", err
	}
	return strings.Join(block, ""), nil
}

// serverNames lists the [[mcp.servers]] entries by name, in order.
func serverNames(cfg config.Config) []string {
	var names []string
	for _, s := range cfg.MCP.Servers {
		names = append(names, s.Name)
	}
	return names
}
