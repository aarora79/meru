// This file holds the frontmatter parser: it splits a SKILL.md file into its
// header fields and its body. It reads the small part of YAML that skill
// files use, and rejects the rest with an error that names the line.

package skills

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// errNoFrontmatter reports a file that doesn't open with a "---" line.
var errNoFrontmatter = errors.New(`no frontmatter: the file must start with a "---" line`)

// keyPattern matches a frontmatter key such as "name", "allowed-tools" or
// "user_invocable". YAML allows far more; skill files never need it.
var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// parseFrontmatter splits data into its frontmatter fields and its body.
//
// The file must open with a line holding only "---", and a second such line
// ends the frontmatter. Everything after it is the body, with blank lines
// trimmed from both ends. Windows line endings (\r\n) and a leading
// byte-order mark are accepted.
//
// It returns the fields as a map from key to text. A nested block such as
//
//	metadata:
//	  author: aarora79
//
// comes back flattened, as "metadata.author" = "aarora79". It fails on a
// missing or unclosed frontmatter, a malformed key, a duplicate key, a tab
// used for indentation, or a quoted value it can't read.
func parseFrontmatter(data []byte) (fields map[string]string, body string, err error) {
	text := strings.TrimPrefix(string(data), "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// strings.Split returns every line; the last one is "" when the file
	// ends in a newline, which the loop below treats as a blank line.
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " ") != "---" {
		return nil, "", errNoFrontmatter
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " ") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", errors.New(`frontmatter never closes: add a "---" line after the last field`)
	}

	// Line numbers in errors count from 1 at the top of the file, and the
	// fields start on line 2.
	fields, err = parseFields(lines[1:end], 2)
	if err != nil {
		return nil, "", err
	}
	body = strings.Trim(strings.Join(lines[end+1:], "\n"), "\n")
	return fields, body, nil
}

// parseFields reads "key: value" lines at the left margin, each followed by
// any lines indented under it. first is the file line number of lines[0], for
// error messages. It returns the fields, or an error naming the first line it
// can't read.
func parseFields(lines []string, first int) (map[string]string, error) {
	fields := make(map[string]string)
	// seen tracks the keys at this level, including ones whose value was a
	// nested map and so never landed in fields under their own name.
	seen := make(map[string]bool)
	i := 0
	for i < len(lines) {
		line := lines[i]
		lineNo := first + i
		if isBlank(line) || strings.HasPrefix(line, "#") {
			i++
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			return nil, fmt.Errorf("line %d: indented line with no key above it", lineNo)
		}
		// strings.Cut splits at the first ":" and reports whether it found one.
		key, value, found := strings.Cut(line, ":")
		if !found || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf(`line %d: want "key: value", got %q`, lineNo, line)
		}
		if value != "" && value[0] != ' ' {
			return nil, fmt.Errorf(`line %d: put a space after the ":" in %q`, lineNo, line)
		}
		value = strings.TrimSpace(value)
		if seen[key] {
			return nil, fmt.Errorf("line %d: key %q appears twice", lineNo, key)
		}
		seen[key] = true

		// Collect the indented lines under this key. Blank lines inside the
		// block belong to it; blank lines at its end don't.
		j := i + 1
		for j < len(lines) && (isBlank(lines[j]) || lines[j][0] == ' ' || lines[j][0] == '\t') {
			j++
		}
		block := lines[i+1 : j]
		for len(block) > 0 && isBlank(block[len(block)-1]) {
			block = block[:len(block)-1]
		}
		for k, b := range block {
			if !isBlank(b) && strings.HasPrefix(strings.TrimLeft(b, " "), "\t") {
				return nil, fmt.Errorf("line %d: indent with spaces, not tabs", lineNo+1+k)
			}
		}

		switch {
		case isBlockIndicator(value):
			fields[key] = blockScalar(block, value)
		case value == "" && len(block) > 0 && looksLikeMap(block):
			// A nested map: parse it the same way and prefix each key.
			nested, err := parseFields(dedent(block), lineNo+1)
			if err != nil {
				return nil, err
			}
			for k, v := range nested {
				fields[key+"."+k] = v
			}
		case value == "":
			// An empty value, or something this parser doesn't model, such as
			// a list. Keep the lines as they are so nothing is lost.
			fields[key] = strings.Join(dedent(block), "\n")
		case value[0] == '"' || value[0] == '\'':
			if len(block) > 0 {
				return nil, fmt.Errorf("line %d: a quoted value must fit on one line", lineNo)
			}
			unquoted, err := unquote(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			fields[key] = unquoted
		default:
			// A plain value may carry on over indented lines, as in the
			// description example in ARCHITECTURE.md. YAML joins them with
			// spaces.
			fields[key] = foldLines(append([]string{value}, block...))
		}
		i = j
	}
	return fields, nil
}

// isBlank reports whether line holds only spaces and tabs.
func isBlank(line string) bool {
	return strings.TrimSpace(line) == ""
}

// isBlockIndicator reports whether value starts a YAML block scalar: ">"
// folds the lines below into one paragraph, "|" keeps their line breaks, and
// a trailing "-" drops the final newline.
func isBlockIndicator(value string) bool {
	switch value {
	case ">", ">-", ">+", "|", "|-", "|+":
		return true
	}
	return false
}

// blockScalar turns the lines under a ">" or "|" key into the value.
func blockScalar(block []string, indicator string) string {
	lines := dedent(block)
	var v string
	if indicator[0] == '|' {
		v = strings.Join(lines, "\n")
	} else {
		v = foldLines(lines)
	}
	if strings.HasSuffix(indicator, "-") || v == "" {
		return v
	}
	return v + "\n"
}

// foldLines joins lines the way YAML folds text: neighbouring lines join with
// one space, and a blank line becomes a line break.
func foldLines(lines []string) string {
	var b strings.Builder
	pendingBreak := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			b.WriteString("\n")
			pendingBreak = true
			continue
		}
		if b.Len() > 0 && !pendingBreak {
			b.WriteString(" ")
		}
		b.WriteString(line)
		pendingBreak = false
	}
	return strings.TrimSpace(b.String())
}

// looksLikeMap reports whether the first non-blank line of an indented block
// reads as "key: value" or "key:", which makes the block a nested map.
func looksLikeMap(block []string) bool {
	for _, line := range block {
		if isBlank(line) {
			continue
		}
		key, rest, found := strings.Cut(strings.TrimSpace(line), ":")
		return found && keyPattern.MatchString(key) && (rest == "" || rest[0] == ' ')
	}
	return false
}

// dedent removes the indent the non-blank lines of block share, so a nested
// block can be parsed as if it started at the left margin. Blank lines come
// back empty.
func dedent(block []string) []string {
	indent := -1
	for _, line := range block {
		if isBlank(line) {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	out := make([]string, len(block))
	for i, line := range block {
		if isBlank(line) {
			out[i] = ""
			continue
		}
		out[i] = line[indent:]
	}
	return out
}

// unquote reads a one-line quoted YAML value. Double quotes take the usual
// backslash escapes (\n, \", \\ and so on). Single quotes take none; inside
// them, two single quotes in a row stand for one.
func unquote(value string) (string, error) {
	q := value[0]
	if len(value) < 2 || value[len(value)-1] != q {
		return "", fmt.Errorf("quoted value %s has no closing quote", value)
	}
	if q == '\'' {
		inner := value[1 : len(value)-1]
		return strings.ReplaceAll(inner, "''", "'"), nil
	}
	// strconv.Unquote reads a Go string literal. Go and YAML share the common
	// escapes, which is all a skill header needs.
	s, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("can't read quoted value %s: %w", value, err)
	}
	return s, nil
}
