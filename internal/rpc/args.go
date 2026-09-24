// This file holds the helpers both clients use to show a tool call's
// arguments: ArgsLines for an approval prompt, which has room for several
// lines, ArgsLine for a log or status line, which has room for one, and
// ArgvLine for a local command's program and arguments.

package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ArgsLines returns args as indented JSON, one string per line, at most
// maxLines of them. When args run longer, the last line says how many lines
// it left out, such as "… 12 more lines". Arguments that aren't valid JSON
// come back as they are, cut the same way. Empty args give nil.
func ArgsLines(args json.RawMessage, maxLines int) []string {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil
	}
	var pretty bytes.Buffer
	// json.Indent rewrites valid JSON with one field per line. On invalid
	// JSON it fails, and we show the raw bytes instead.
	text := string(args)
	if err := json.Indent(&pretty, args, "", "  "); err == nil {
		text = pretty.String()
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if maxLines < 2 || len(lines) <= maxLines {
		return lines
	}
	// Keep maxLines-1 lines and use the last one to say what was cut.
	cut := len(lines) - (maxLines - 1)
	return append(lines[:maxLines-1], fmt.Sprintf("… %d more lines", cut))
}

// ArgsLine returns args as compact JSON on one line, cut to at most width
// characters with "…" at the end. Empty args give "".
func ArgsLine(args json.RawMessage, width int) string {
	if len(bytes.TrimSpace(args)) == 0 {
		return ""
	}
	var compact bytes.Buffer
	text := string(args)
	if err := json.Compact(&compact, args); err == nil {
		text = compact.String()
	}
	// Raw text that isn't JSON may hold new lines; a log line can't.
	text = strings.Join(strings.Fields(text), " ")
	return Cut(text, width)
}

// Cut shortens s to at most width characters, ending it with "…" when it
// had to cut. It counts characters (runes), not bytes, so it never splits a
// character that takes more than one byte, such as "é".
func Cut(s string, width int) string {
	if width < 1 || utf8.RuneCountInString(s) <= width {
		return s
	}
	runes := []rune(s)
	return string(runes[:width-1]) + "…"
}

// ArgvLine writes a command's program and arguments on one line, separated
// by spaces, as a reader would type them: an element that is empty or holds
// a space, a quote, a backslash or a character that doesn't print gets Go's
// double quotes, so each element stays readable as one. The line is for
// people only; merud never runs it, and runs no shell.
func ArgvLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		plain := a != ""
		for _, r := range a {
			if r <= ' ' || r == '"' || r == '\\' || r == '\'' || !unicode.IsPrint(r) {
				plain = false
				break
			}
		}
		if plain {
			parts[i] = a
		} else {
			// strconv.Quote wraps the string in double quotes and escapes
			// quotes, backslashes and characters that don't print.
			parts[i] = strconv.Quote(a)
		}
	}
	return strings.Join(parts, " ")
}
