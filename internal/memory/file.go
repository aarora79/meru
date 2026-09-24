// This file holds the two text helpers behind a memory file: parse, which
// reads a file's frontmatter and fact, and slugify, which turns a fact into a
// file name.

package memory

import (
	"strings"
	"time"
)

// parse splits a memory file into its created date, its source and its text.
//
// Writing is strict (Add always writes the same shape), but reading is
// forgiving, because you may have edited the file by hand and a memory
// shouldn't vanish over a typo. A file with no frontmatter, or whose
// frontmatter never closes, is all text. Unknown keys are ignored, and a
// created date that doesn't parse leaves Created as the zero time.
func parse(data []byte) (created time.Time, source, text string) {
	s := strings.TrimPrefix(string(data), "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")

	// CutPrefix returns s without the prefix and whether it was there.
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return time.Time{}, "", strings.TrimSpace(s)
	}
	header, body, ok := cutClosingLine(rest)
	if !ok {
		return time.Time{}, "", strings.TrimSpace(s)
	}
	for line := range strings.Lines(header) {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "created":
			created = parseDate(value)
		case "source":
			source = value
		}
	}
	return created, source, strings.TrimSpace(body)
}

// cutClosingLine finds the first line of s that is exactly "---" and returns
// the text before it and the text after it. ok is false when there is none.
func cutClosingLine(s string) (before, after string, ok bool) {
	if rest, found := strings.CutPrefix(s, "---\n"); found {
		return "", rest, true
	}
	if s == "---" {
		return "", "", true
	}
	i := strings.Index(s, "\n---\n")
	if i >= 0 {
		return s[:i+1], s[i+len("\n---\n"):], true
	}
	if strings.HasSuffix(s, "\n---") {
		return s[:len(s)-len("---")], "", true
	}
	return "", "", false
}

// parseDate reads a created value written as 2026-09-23 or as a full
// timestamp such as 2026-09-23T10:15:02Z. It returns the zero time for
// anything else.
func parseDate(value string) time.Time {
	if t, err := time.Parse(dateLayout, value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

// slugify turns the start of text into a file name without the ".md":
// lowercase ASCII letters and digits, with every other run of characters
// turned into one "-", cut at a word boundary to at most maxSlugBytes.
// "Prefers index funds over individual stocks." becomes
// "prefers-index-funds-over-individual-stocks".
//
// The result can't hold a path separator, a dot or a space, so it is safe on
// every file system. Text with no ASCII letters or digits (a fact written in
// Hindi, say) becomes "memory", and a name Windows reserves, such as "con",
// gets a "memory-" prefix.
func slugify(text string) string {
	// FieldsFunc splits text wherever the function returns true, here at
	// every rune that isn't an ASCII letter or digit, and drops empty pieces.
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	slug := ""
	for _, w := range words {
		next := w
		if slug != "" {
			next = slug + "-" + w
		}
		if len(next) > maxSlugBytes {
			if slug == "" {
				// One very long first word: cut it. It holds only ASCII,
				// so cutting at a byte count can't split a character.
				slug = w[:maxSlugBytes]
			}
			break
		}
		slug = next
	}
	switch {
	case slug == "":
		return "memory"
	case isWindowsReserved(slug):
		return "memory-" + slug
	}
	return slug
}

// isWindowsReserved reports whether name is a device name Windows won't
// allow as a file name, even with an extension: con, prn, aux, nul, com0-9
// and lpt0-9. Meru checks on every OS so a memory folder copied to a Windows
// machine still works.
func isWindowsReserved(name string) bool {
	switch name {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(name) == 4 && (strings.HasPrefix(name, "com") || strings.HasPrefix(name, "lpt")) {
		return name[3] >= '0' && name[3] <= '9'
	}
	return false
}
