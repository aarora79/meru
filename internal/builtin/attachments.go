// This file holds AttachmentText, which reads a mail attachment that an
// MCP or A2A call just saved and hands its text to dispatch, which adds it
// to the call's result. See ARCHITECTURE.md, "Adding an MCP server".

package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/dispatch"
)

// Limits on the attachments one result gets.
const (
	// maxAttached is how many attachments one result gets. A mail with
	// many attachments is rare, and two pages of text already fill most of
	// what dispatch lets the model read.
	maxAttached = 2
	// attachSlack is how long before the call began a file's modified time
	// may be and still count as saved by the call. Some file systems keep
	// modified times in whole seconds, or two-second steps, so a file saved
	// in the call's first moment can look older than the call.
	attachSlack = 2 * time.Second
	// attachLines is room kept, in characters, for the lines each
	// attachment adds around its text.
	attachLines = 600
)

// AttachmentText returns the text to add to a tool result that names a
// file the call saved in the attachments folder, or "" when there is none.
// text is the result; since is when the call began. dispatch calls it,
// through Options.Attachments, for each MCP or A2A call that succeeds.
//
// The google server saves an attachment and reports only its file name.
// In testing the model then failed to open it: it downloaded the file four
// times, guessed the wrong folder and ran out of rounds. Reading the file
// here gives the model its text in the same result.
//
// A file counts when its name appears in text word for word, and it was
// modified no earlier than since, less attachSlack. The first rule needs
// no knowledge of any server's wording. The second keeps an old attachment
// out when some other result happens to name it: only a file the call
// itself wrote qualifies, and no time window needs tuning. At most
// maxAttached files count, in the order text names them.
//
// Each file gets a line with its path, in the "~" form read_file takes,
// then as much of its text as read_file returns in one call, less what the
// result already uses of dispatch.MaxModelResult. Check and ReadText apply
// read_file's rules, so a symlink, a secret or a file too large gets the
// path line and the reason, and no text. With no output folder, or no
// [index] folders (the file tools are off then), it returns "".
func (t *Tools) AttachmentText(text string, since time.Time) string {
	if t.outputDir == "" || !t.hasFiles() {
		return ""
	}
	dir := filepath.Join(t.outputDir, attachmentsFolder)
	// ReadDir lists the folder in one call. It stays small: the google
	// server deletes each file an hour after it saves it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type found struct {
		name string
		at   int // where text first names it
	}
	var names []found
	for _, e := range entries {
		at := strings.Index(text, e.Name())
		if at < 0 || e.IsDir() {
			continue
		}
		// For a symlink, Info describes the link itself, not its target.
		info, err := e.Info()
		if err != nil || info.ModTime().Before(since.Add(-attachSlack)) {
			continue
		}
		names = append(names, found{e.Name(), at})
	}
	if len(names) == 0 {
		return ""
	}
	// SortFunc orders by the function's result: negative puts a first.
	slices.SortFunc(names, func(a, b found) int { return a.at - b.at })
	names = names[:min(len(names), maxAttached)]

	// The attachments share what room the result leaves, so dispatch's
	// cut never drops the line that says how to read on.
	room := dispatch.MaxModelResult - utf8.RuneCountInString(text) - len(names)*attachLines
	each := min(maxReadChars, max(room, 0)/len(names))
	var b strings.Builder
	for _, f := range names {
		b.WriteString(t.attachment(filepath.Join(dir, f.name), each))
	}
	return b.String()
}

// attachment returns the block for the file at p: a line with its path,
// then up to limit characters of its text, headed by a separator line and
// followed, when the text goes on, by the offset to pass read_file. When
// Meru can't read the file, or limit is 0, the block is the path line
// alone, saying why.
func (t *Tools) attachment(p string, limit int) string {
	shown := t.show(p)
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nSaved at %s.", shown)

	c, err := t.files.Check(p)
	if err == nil && c.Reason != "" {
		fmt.Fprintf(&b, " Meru can't read it: %s.\n", why(c.Reason))
		return b.String()
	}
	if err != nil {
		fmt.Fprintf(&b, " Meru couldn't read it: %v.\n", err)
		return b.String()
	}
	pages, reason, err := t.files.ReadText(c.Path)
	switch {
	case err != nil:
		fmt.Fprintf(&b, " Meru couldn't read it: %v.\n", err)
		return b.String()
	case reason != "":
		fmt.Fprintf(&b, " Meru can't read it: %s.\n", why(reason))
		return b.String()
	case limit <= 0:
		b.WriteString(" The result has no room for its text; read it with read_file.\n")
		return b.String()
	}

	// Count characters, as read_file does, so the offset below is the one
	// read_file takes.
	runes := []rune(joinPages(pages))
	end := min(limit, len(runes))
	if end < len(runes) {
		b.WriteString(" Read it with read_file; the text below is its first part.\n")
	} else {
		b.WriteString(" Meru read all of it; the text is below.\n")
	}
	fmt.Fprintf(&b, "--- Meru read %s (%d of %d characters) ---\n", filepath.Base(p), end, len(runes))
	b.WriteString(string(runes[:end]))
	if end < len(runes) {
		fmt.Fprintf(&b, "\n[%d more characters. To read on, call read_file with path %q and offset %d.]\n",
			len(runes)-end, shown, end)
	}
	return b.String()
}
