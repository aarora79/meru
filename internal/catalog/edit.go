// This file changes one list in config.toml without touching the rest of
// the file: a server's or agent's allow and confirm lists, and the lists
// in a plain table such as [index] folders or [skills] disabled. merud
// uses it for the desktop app's settings. Like AppendServer, it writes
// through writeChecked, so a change that would break config leaves the
// file as it was.

package catalog

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/config"
)

// The array tables whose entries SetEntryLists can change, by the header
// that starts each entry.
const (
	TableMCP = "mcp.servers" // [[mcp.servers]]
	TableA2A = "a2a.agents"  // [[a2a.agents]]
)

// SetEntryLists sets list keys, such as allow and confirm, in the entry
// named name of the array table table (TableMCP or TableA2A). lists maps
// each key to its new value; an empty list writes "key = []". A key the
// entry lacks goes in after its last key.
//
// Every other line stays as it was, comments and all: the TOML library
// can't write a file back with its comments, so this edits lines of text.
// To guard against a line it misreads, it writes through writeChecked and
// checks that the result loads, that the entry holds exactly the lists
// asked for, and that the servers and agents are the same ones, in the
// same order. It fails when config doesn't load or has no such entry.
func SetEntryLists(configPath, table, name string, lists map[string][]string) error {
	if table != TableMCP && table != TableA2A {
		return fmt.Errorf("can't set lists in [[%s]]", table)
	}
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	before, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("fix config.toml before changing it: %w", err)
	}
	lines := splitLines(string(old))
	start, end, ok := entrySpan(lines, table, name)
	if !ok {
		return fmt.Errorf("%s has no [[%s]] entry named %q", configPath, table, name)
	}
	lines = setKeys(lines, start, end, lists)

	return writeChecked(configPath, strings.Join(lines, ""), func(next config.Config) error {
		if !slices.Equal(entryNames(next), entryNames(before)) {
			return fmt.Errorf("the change would add or drop a server or agent; edit %s by hand", configPath)
		}
		got := entryLists(next, table, name)
		for key, want := range lists {
			// slices.Equal counts a nil list and an empty one as equal.
			if !slices.Equal(got[key], want) {
				return fmt.Errorf("the change left %s = %v in %q, want %v; edit %s by hand", key, got[key], name, want, configPath)
			}
		}
		return CheckServers(next.MCP.Servers)
	})
}

// SetTableLists sets list keys in the plain table table, such as
// {"folders": ...} in "index" or {"tools": ..., "confirm": ...} in
// "builtin", and adds the table at the end of the file when config has
// none. Setting two keys of one table takes one write, so a reader never
// sees one changed without the other. check runs on the loaded result
// before the file is replaced, so the caller can make sure the lists came
// out as it asked; it may be nil.
//
// Like SetEntryLists, it keeps every other line as it was and fails when
// config doesn't load or the result wouldn't.
func SetTableLists(configPath, table string, lists map[string][]string, check func(config.Config) error) error {
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	if _, err := config.Load(configPath); err != nil {
		return fmt.Errorf("fix config.toml before changing it: %w", err)
	}
	lines := splitLines(string(old))
	start, end, ok := tableSpan(lines, table)
	if !ok {
		// No such table yet: add its header at the end, after a blank
		// line, and let setKeys fill it in.
		if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
			lines[n-1] += "\n"
		}
		if len(lines) > 0 {
			lines = append(lines, "\n")
		}
		lines = append(lines, "["+table+"]\n")
		start, end = len(lines)-1, len(lines)
	}
	lines = setKeys(lines, start, end, lists)
	return writeChecked(configPath, strings.Join(lines, ""), func(next config.Config) error {
		if check == nil {
			return nil
		}
		return check(next)
	})
}

// splitLines splits text into lines that keep their "\n".
func splitLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // SplitAfter leaves "" after a final "\n"
	}
	return lines
}

// entrySpan finds the entry named name of the array table table: the index
// of its header line, and the index just past its last line, which is the
// next top-level header or the end of the file. Sub-tables such as
// [mcp.servers.env] stay part of it. ok is false when no entry has that
// name.
func entrySpan(lines []string, table, name string) (start, end int, ok bool) {
	start, end = -1, len(lines)
	for i, line := range lines {
		key, isHeader := header(line)
		if !isHeader || strings.HasPrefix(key, table+".") {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if key == table && entryNamed(lines[i:], table, name) {
			start = i
		}
	}
	return start, end, start >= 0
}

// tableSpan finds the plain table table: the index of its header line and
// the index just past its last line. ok is false when config has no such
// table.
func tableSpan(lines []string, table string) (start, end int, ok bool) {
	start, end = -1, len(lines)
	for i, line := range lines {
		key, isHeader := header(line)
		if !isHeader {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		// A header written [[x]] also yields key x, but an array table
		// isn't a plain one; only "[" + table + "]" counts.
		if key == table && !strings.HasPrefix(strings.TrimSpace(line), "[[") {
			start = i
		}
	}
	return start, end, start >= 0
}

// entryNamed reports whether lines, which start at a header of table, hold
// the entry called name. It parses the lines up to the next header with
// the TOML library, so any way of writing the name counts.
func entryNamed(lines []string, table, name string) bool {
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
		A2A config.A2A `toml:"a2a"`
	}
	if _, err := toml.Decode(b.String(), &parsed); err != nil {
		return false
	}
	switch table {
	case TableMCP:
		return len(parsed.MCP.Servers) == 1 && parsed.MCP.Servers[0].Name == name
	case TableA2A:
		return len(parsed.A2A.Agents) == 1 && parsed.A2A.Agents[0].Name == name
	}
	return false
}

// setKeys sets each key of lists in lines[start:end], a table whose header
// is lines[start], and returns the new lines. A key already there has its
// whole value replaced, however many lines it spans, and keeps whatever
// comes before the "=" and any comment after the value. A key not there
// goes in after the table's last key line, before any comments and blank
// lines that follow it and before any sub-table. Keys go in sorted order, so the result doesn't
// depend on map order.
func setKeys(lines []string, start, end int, lists map[string][]string) []string {
	keys := make([]string, 0, len(lists))
	for k := range lists {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value := list(lists[key])
		// The table's own keys stop at the first header after it, such as
		// [mcp.servers.env]: the keys below that belong to the sub-table.
		own := start + 1
		for own < end {
			if _, ok := header(lines[own]); ok {
				break
			}
			own++
		}
		from, to, ok := keySpan(lines, start+1, own, key)
		if ok {
			// Keep the key as written, "allow   = ", and the comment
			// after the value's last line.
			eq := strings.Index(lines[from], "=")
			comment := trailingComment(lines[to-1])
			line := lines[from][:eq+1] + " " + value + comment + "\n"
			lines = slices.Concat(lines[:from], []string{line}, lines[to:])
			end -= (to - from) - 1
			continue
		}
		at := own
		for at > start+1 && (isComment(lines[at-1]) || isBlank(lines[at-1])) {
			at--
		}
		if at > 0 && !strings.HasSuffix(lines[at-1], "\n") {
			lines[at-1] += "\n"
		}
		lines = slices.Insert(lines, at, key+" = "+value+"\n")
		end++
	}
	return lines
}

// keySpan finds key's assignment in lines[from:to]: the index of the line
// that starts it and the index just past the line that ends it. A value
// such as a list can run over several lines; the span ends at the line
// where its brackets close, counting only brackets outside strings and
// comments. ok is false when the key isn't there.
func keySpan(lines []string, from, to int, key string) (start, end int, ok bool) {
	for i := from; i < to; i++ {
		k, rest, found := strings.Cut(lines[i], "=")
		if !found || isComment(lines[i]) || strings.Trim(strings.TrimSpace(k), `"'`) != key {
			continue
		}
		depth := bracketDepth(rest)
		j := i + 1
		for depth > 0 && j < to {
			depth += bracketDepth(lines[j])
			j++
		}
		return i, j, true
	}
	return 0, 0, false
}

// bracketDepth returns how many more "[" than "]" line holds, outside
// quoted strings and before any comment.
func bracketDepth(line string) int {
	depth := 0
	var quote rune // the quote that opened the string we are in, or 0
	escaped := false
	for _, r := range line {
		switch {
		case quote != 0:
			if escaped {
				escaped = false
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return depth
		case r == '[':
			depth++
		case r == ']':
			depth--
		}
	}
	return depth
}

// trailingComment returns the comment at the end of line, with the spaces
// before it, such as "   # the server's tools", or "" when it has none. A
// "#" inside a quoted string doesn't count.
func trailingComment(line string) string {
	line = strings.TrimRight(line, "\r\n")
	var quote rune
	escaped := false
	for i, r := range line {
		switch {
		case quote != 0:
			if escaped {
				escaped = false
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			j := i
			for j > 0 && (line[j-1] == ' ' || line[j-1] == '\t') {
				j--
			}
			return line[j:]
		}
	}
	return ""
}

// entryNames lists the MCP servers, then the A2A agents, by name, so a
// check can tell that an edit added or dropped none.
func entryNames(cfg config.Config) []string {
	var names []string
	for _, s := range cfg.MCP.Servers {
		names = append(names, "mcp:"+s.Name)
	}
	for _, a := range cfg.A2A.Agents {
		names = append(names, "a2a:"+a.Name)
	}
	return names
}

// entryLists returns the allow, confirm and always_confirm lists of the
// entry named name in table.
func entryLists(cfg config.Config, table, name string) map[string][]string {
	switch table {
	case TableMCP:
		for _, s := range cfg.MCP.Servers {
			if s.Name == name {
				return map[string][]string{"allow": s.Allow, "confirm": s.Confirm, "always_confirm": s.AlwaysConfirm}
			}
		}
	case TableA2A:
		for _, a := range cfg.A2A.Agents {
			if a.Name == name {
				return map[string][]string{"allow": a.Allow, "confirm": a.Confirm}
			}
		}
	}
	return map[string][]string{}
}
