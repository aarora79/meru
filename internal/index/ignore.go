// This file reads .gitignore-style patterns and matches paths against them.
// The same code serves .gitignore files, .meruignore files and the config's
// [index] ignore list.

package index

import (
	"fmt"
	"regexp"
	"strings"
)

// pattern is one line of a .gitignore-style file, compiled to a regular
// expression over a slash-separated path relative to the file's folder.
type pattern struct {
	re      *regexp.Regexp
	negate  bool // the line started with "!": a match un-ignores the path
	dirOnly bool // the line ended with "/": it matches folders only
}

// parsePatterns compiles each line of a .gitignore-style file. Blank lines
// and comments ("# ...") produce nothing. It returns the patterns that
// compiled and, separately, one error per line that didn't, so a caller can
// decide whether a bad line is fatal (the config) or only worth a log line
// (someone's .gitignore).
func parsePatterns(lines []string) ([]pattern, []error) {
	var out []pattern
	var errs []error
	for n, line := range lines {
		// A three-value result: the pattern, whether the line held one, and
		// an error.
		p, ok, err := compilePattern(line)
		if err != nil {
			errs = append(errs, fmt.Errorf("line %d %q: %w", n+1, line, err))
			continue
		}
		if ok {
			out = append(out, p)
		}
	}
	return out, errs
}

// compilePattern turns one line into a pattern, following the rules in git's
// gitignore documentation that matter for skipping files:
//
//   - a line starting with "#" is a comment; "\#" is a literal "#"
//   - "!" at the start negates the pattern; "\!" is a literal "!"
//   - a trailing "/" matches only folders
//   - a "/" at the start or in the middle anchors the pattern to the folder
//     holding the ignore file; without one, the pattern matches a name at
//     any depth
//   - "*" matches anything but "/", "?" one character but "/", and "[a-z]"
//     a character class
//   - "**/" at the start or between slashes matches zero or more folders,
//     and "/**" at the end matches everything inside
//
// ok is false for blank lines and comments. It fails when the line holds a
// broken character class that the regular expression compiler rejects.
func compilePattern(line string) (p pattern, ok bool, err error) {
	line = strings.TrimSuffix(line, "\r")
	line = trimTrailingSpaces(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return pattern{}, false, nil
	}
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return pattern{}, false, nil
	}
	// A slash anywhere but the end (already removed) anchors the pattern.
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	body := globToRegexp(line)
	expr := "^" + body + "$"
	if !anchored {
		// An unanchored pattern matches its name in any folder, so allow any
		// run of folders in front of it.
		expr = "^(?:.*/)?" + body + "$"
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return pattern{}, false, err
	}
	p.re = re
	return p, true, nil
}

// trimTrailingSpaces removes spaces at the end of a line unless a backslash
// escapes them, as git does.
func trimTrailingSpaces(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, `\ `) {
		s = s[:len(s)-1]
	}
	return s
}

// globToRegexp translates a gitignore glob into the body of a regular
// expression. Everything that isn't a glob character is quoted, so a "." in
// a file name matches only a dot.
func globToRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		// atSegmentStart is true when c begins a path segment, which is the
		// only place "**" has its special meaning.
		atSegmentStart := i == 0 || glob[i-1] == '/'
		switch {
		case c == '*' && strings.HasPrefix(glob[i:], "**/") && atSegmentStart:
			b.WriteString("(?:.*/)?") // zero or more folders
			i += 2
		case c == '*' && glob[i:] == "**" && atSegmentStart:
			b.WriteString(".*") // everything, at any depth
			i++
		case c == '*':
			// Any other run of stars acts as one "*".
			for i+1 < len(glob) && glob[i+1] == '*' {
				i++
			}
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		case c == '[':
			class, n := charClass(glob[i:])
			if n == 0 {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(class)
			i += n - 1
		default:
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		}
	}
	return b.String()
}

// charClass translates a "[...]" class at the start of s into regular
// expression syntax and returns it with the number of bytes it used. It
// returns n == 0 when s has no closing "]", so the caller treats "[" as a
// plain character.
func charClass(s string) (class string, n int) {
	i := 1
	var b strings.Builder
	b.WriteByte('[')
	if i < len(s) && (s[i] == '!' || s[i] == '^') {
		b.WriteByte('^')
		i++
	}
	// A "]" right after the opening bracket belongs to the class.
	if i < len(s) && s[i] == ']' {
		b.WriteString(`\]`)
		i++
	}
	for ; i < len(s); i++ {
		switch s[i] {
		case ']':
			b.WriteByte(']')
			return b.String(), i + 1
		case '\\', '[':
			b.WriteByte('\\')
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return "", 0
}

// matchPatterns checks rel, a slash-separated path relative to the folder
// the patterns came from, against ps. The last pattern that matches decides,
// as in git. matched is false when no pattern matched; ignored is the
// verdict when one did.
func matchPatterns(ps []pattern, rel string, isDir bool) (matched, ignored bool) {
	for _, p := range ps {
		if p.dirOnly && !isDir {
			continue
		}
		if p.re.MatchString(rel) {
			matched, ignored = true, !p.negate
		}
	}
	return matched, ignored
}
