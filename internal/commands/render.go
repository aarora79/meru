// This file turns the model's arguments into a concrete argv: ArgsFromJSON
// reads the JSON the model sent, and Render checks each value against its
// parameter's type and puts it into the declared argv.

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ArgsFromJSON reads the model's arguments, a JSON object, into one string
// per parameter. A number keeps the text the model wrote, so Render's int
// check sees "20" and refuses "20.5". Empty arguments give an empty map. It
// fails on text that isn't a JSON object and on a value that is neither a
// string nor a number.
func ArgsFromJSON(raw json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	// UseNumber keeps a number as its text (json.Number) instead of a
	// float64, which would turn a large int into 1.2e+19.
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the arguments aren't a JSON object: %v", err)
	}
	for k, v := range m {
		// A type switch runs the case that matches v's dynamic type.
		switch v := v.(type) {
		case string:
			out[k] = v
		case json.Number:
			out[k] = v.String()
		default:
			return nil, fmt.Errorf("parameter %q must be a string or a number", k)
		}
	}
	return out, nil
}

// Render turns the model's arguments into a concrete argv. It returns an
// error when an argument is missing, fails its type's validation, or names
// a parameter the command does not declare. Render never returns a shell
// string; each placeholder becomes part of exactly one argv element, so a
// value with spaces, quotes or a semicolon stays one element.
//
// The checks by type:
//
//   - string: not empty, no null byte, at most MaxLen bytes, the whole
//     value a match for Pattern when one is set, and not starting with
//     "-" when the placeholder is the whole element, since the program
//     would read such a value as a flag. Inside a longer element, such as
//     "--grep={pattern}", a leading "-" is safe.
//   - int: a whole number within Min and Max.
//   - enum: one of Values.
//   - path: "~" expands to the home directory, and a relative path starts
//     at Under. The path must exist, and once every symbolic link in it is
//     resolved it must lie inside Under. The argv gets the resolved path,
//     so a link changed after the check can't redirect the program.
func (c Command) Render(args map[string]string) ([]string, error) {
	var unknown []string
	for name := range args {
		if _, ok := c.Params[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, fmt.Errorf("%s takes no parameter %s; it takes %s",
			c.ToolName(), quoteAll(unknown), c.paramList())
	}

	values := map[string]string{}
	// Walk the names in order, so the first error is always the same one.
	names := make([]string, 0, len(c.Params))
	for name := range c.Params {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		v, ok := args[name]
		if !ok {
			return nil, fmt.Errorf("%s needs the parameter %q", c.ToolName(), name)
		}
		checked, err := c.Params[name].check(v)
		if err != nil {
			return nil, fmt.Errorf("%s: parameter %q: %w", c.ToolName(), name, err)
		}
		values[name] = checked
	}

	argv := make([]string, 0, len(c.Argv))
	for _, elem := range c.Argv {
		// New parsed every element already, so this can't fail.
		segs, err := parse(elem)
		if err != nil {
			return nil, err
		}
		if len(segs) == 1 && segs[0].param != "" {
			name := segs[0].param
			if c.Params[name].Type == TypeString && strings.HasPrefix(values[name], "-") {
				return nil, fmt.Errorf("%s: parameter %q can't start with \"-\", because the program would read it as a flag",
					c.ToolName(), name)
			}
		}
		var b strings.Builder
		for _, s := range segs {
			if s.param != "" {
				b.WriteString(values[s.param])
			} else {
				b.WriteString(s.text)
			}
		}
		argv = append(argv, b.String())
	}
	return argv, nil
}

// check validates one value against p and returns the value the argv gets:
// the value itself, an int in its plain form, or a path resolved.
func (p Param) check(v string) (string, error) {
	if strings.ContainsRune(v, 0) {
		return "", errors.New("the value holds a null byte")
	}
	switch p.Type {
	case TypeString:
		if v == "" {
			return "", errors.New("the value is empty")
		}
		if len(v) > p.MaxLen {
			return "", fmt.Errorf("the value is %d bytes, over the %d-byte limit", len(v), p.MaxLen)
		}
		if p.Pattern != nil && !p.Pattern.MatchString(v) {
			return "", fmt.Errorf("%q doesn't match the pattern %s", v, p.Pattern)
		}
		return v, nil
	case TypeInt:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return "", fmt.Errorf("%q is not a whole number", v)
		}
		if p.Min != nil && n < *p.Min {
			return "", fmt.Errorf("%d is below the minimum, %d", n, *p.Min)
		}
		if p.Max != nil && n > *p.Max {
			return "", fmt.Errorf("%d is above the maximum, %d", n, *p.Max)
		}
		return strconv.FormatInt(n, 10), nil
	case TypeEnum:
		if !slices.Contains(p.Values, v) {
			return "", fmt.Errorf("%q is not one of %s", v, quoteAll(p.Values))
		}
		return v, nil
	case TypePath:
		return p.checkPath(v)
	}
	return "", fmt.Errorf("unknown type %q", p.Type)
}

// checkPath resolves a path value and checks it lies inside p.Under. It
// resolves before it checks: a check on the path as written would let
// "~/repos/link", a link to /etc, through.
func (p Param) checkPath(v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "", errors.New("the path is empty")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	path := expandHome(v, home)
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.Under, path)
	}
	// EvalSymlinks follows every link in the path and returns the real
	// one. It fails when the path doesn't exist.
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%s: no such file or folder under %s", v, p.Under)
	}
	if !inside(p.Under, real) {
		return "", fmt.Errorf("%s is outside %s, the only folder this parameter may name", v, p.Under)
	}
	return real, nil
}

// inside reports whether path is dir or lies below it. Both must be clean
// absolute paths. filepath.Rel gives a path that starts with ".." when path
// is elsewhere.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// paramList names the command's parameters for an error message, such as
// "\"repo\" and \"since\"", or "no parameters".
func (c Command) paramList() string {
	if len(c.Params) == 0 {
		return "no parameters"
	}
	names := make([]string, 0, len(c.Params))
	for name := range c.Params {
		names = append(names, name)
	}
	slices.Sort(names)
	return quoteAll(names)
}

// quoteAll quotes each string and joins them: "a", "b" and "c".
func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}
