// This file holds stripBase64, which takes long runs of base64 out of an
// MCP or A2A result before the model reads it. See ARCHITECTURE.md,
// "Agent loop" step 4.

package dispatch

import (
	"fmt"
	"strings"
)

// minBase64 is the shortest run of base64 characters that stripBase64
// removes. A model can't read base64, so a file sent that way is only
// noise: in a real turn, the google server's get_gmail_attachment_content
// returned a PDF as 109,068 characters of base64, far past the 16,000
// characters the model reads.
//
// Shorter base64-looking strings must survive, because the model passes
// them back to a tool. A Gmail attachment ID runs to about 400 characters
// of base64url, message IDs are 16, and a web token rarely passes 1,000
// and holds dots, which end a run. 2,000 characters leaves those five
// times the room, and a real file is larger: 2,000 characters of base64
// decode to 1,500 bytes.
const minBase64 = 2000

// Base64 tools wrap long output at a fixed width, most often 64 or 76
// characters a line. stripBase64 counts a line whose width falls in this
// range as possible wrapped base64, and joins it to the lines that follow
// it when they have the same width (the last may be shorter).
const (
	minWrap = 60
	maxWrap = 100
)

// base64Note replaces a run stripBase64 removes. It gives the length, so
// the model knows how much data the tool sent, and points it at the text
// that dispatch adds when the call saved a file Meru can read (see
// Options.Attachments).
const base64Note = "[base64 data, %d characters, removed by Meru; the file's text is below when Meru could read it]"

// stripBase64 returns text with each long run of base64 replaced by
// base64Note. A run counts when it holds at least minBase64 characters of
// the base64 alphabet, either on one line or wrapped over lines of one
// width (see wrapped). Everything else, short IDs and the words around a
// run, stays as it was.
//
// It works line by line, so it never needs to hold more than the result
// and its copy, and it runs in time proportional to the text's length.
func stripBase64(text string) string {
	// Most results are short, and a short result can't hold a long run.
	if len(text) < minBase64 {
		return text
	}
	// SplitAfter keeps each "\n" on the end of its line, so joining the
	// pieces gives back the text.
	lines := strings.SplitAfter(text, "\n")
	var b strings.Builder
	for i := 0; i < len(lines); {
		if n, chars := wrapped(lines[i:]); n > 0 {
			fmt.Fprintf(&b, base64Note, chars)
			// Keep the line break after the block, so the text after it
			// still starts on a line of its own.
			last := lines[i+n-1]
			b.WriteString(last[len(body(last)):])
			i += n
			continue
		}
		b.WriteString(stripRuns(lines[i]))
		i++
	}
	return b.String()
}

// wrapped looks for a block of wrapped base64 at the start of lines. The
// first line must be base64 alone, between minWrap and maxWrap characters
// long. Each line after it with the same width, all base64, joins the
// block; a shorter one joins and ends it. It returns how many lines the
// block holds and how many base64 characters, or 0, 0 when the block holds
// fewer than minBase64 characters.
//
// The same-width rule keeps a list of IDs, one to a line, out of it: IDs
// of one width that fits the range are rare, and a list long enough to
// pass minBase64 rarer still.
func wrapped(lines []string) (n, chars int) {
	width := len(body(lines[0]))
	if width < minWrap || width > maxWrap || !allBase64(body(lines[0])) {
		return 0, 0
	}
	chars, n = width, 1
	for n < len(lines) {
		l := body(lines[n])
		if l == "" || len(l) > width || !allBase64(l) {
			break
		}
		chars += len(l)
		n++
		if len(l) < width {
			break
		}
	}
	if chars < minBase64 {
		return 0, 0
	}
	return n, chars
}

// stripRuns returns line with each run of at least minBase64 base64
// characters replaced by base64Note. It walks the bytes: every base64
// character is plain ASCII, and no byte of a multi-byte UTF-8 character
// looks like one, so it never cuts a character in two.
func stripRuns(line string) string {
	if len(line) < minBase64 {
		return line
	}
	var b strings.Builder
	start := 0 // where the current run began
	for i := 0; i <= len(line); i++ {
		if i < len(line) && isBase64(line[i]) {
			continue
		}
		// i ends the run line[start:i], which may be empty.
		if i-start >= minBase64 {
			fmt.Fprintf(&b, base64Note, i-start)
		} else {
			b.WriteString(line[start:i])
		}
		if i < len(line) {
			b.WriteByte(line[i])
		}
		start = i + 1
	}
	return b.String()
}

// body returns line without its line break, "\n" or "\r\n".
func body(line string) string {
	return strings.TrimRight(line, "\r\n")
}

// allBase64 says whether every byte of s is in the base64 alphabet.
func allBase64(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isBase64(s[i]) {
			return false
		}
	}
	return true
}

// isBase64 says whether c belongs to base64 or base64url: letters, digits,
// "+", "/", "-", "_", and "=" for padding.
func isBase64(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		return true
	}
	return c == '+' || c == '/' || c == '-' || c == '_' || c == '='
}
