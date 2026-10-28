// This file holds the Apple Notes tools: search_notes, list_notes and
// read_note. Each reads the Notes app's database live, read-only, through
// internal/applenotes, so an answer reflects the notes as they are now.
// They only read, so they run without asking.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/applenotes"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/platform"
)

// The Apple Notes tools' names, as the model sees them.
const (
	SearchNotes = "search_notes"
	ListNotes   = "list_notes"
	ReadNote    = "read_note"
)

// Limits on the Apple Notes tools.
const (
	// defaultNotes is how many notes search_notes and list_notes return
	// when the model asks for no number; maxNotes is the most they return.
	defaultNotes = 10
	maxNotes     = 30
	// maxNoteChars is how much text read_note returns per call, as much
	// as read_file returns of a file.
	maxNoteChars = maxReadChars
)

// NotesNoAccess is what the Apple Notes tools return, and what the
// desktop app's Settings show, while macOS keeps merud out of the Notes
// database. A real turn showed why the model needs the words: told only
// "unable to open database file", it guessed that Notes was syncing.
const NotesNoAccess = "Meru can't read Apple Notes: macOS needs Full Disk Access for merud. " +
	"In System Settings, open Privacy & Security, Full Disk Access, and turn on merud (it can show as a.out). " +
	"After each Meru update, turn it off and on again."

// IsNotesTool reports whether name is one of the Apple Notes tools.
func IsNotesTool(name string) bool {
	return name == SearchNotes || name == ListNotes || name == ReadNote
}

// UseNotes lets the Apple Notes tools read the database at path. merud
// calls it once at startup on a Mac, with applenotes.DefaultPath; without
// it the tools stay off.
func (t *Tools) UseNotes(path string) {
	t.notesPath = path
}

// notesOff says why the Apple Notes tools can't run here, or "" when they
// can try. Access to the database is checked per call, not here: a user
// who grants Full Disk Access shouldn't have to restart merud for the tool
// list to change.
func (t *Tools) notesOff() string {
	if t.notesPath != "" {
		return ""
	}
	if !platform.Current().AppleNotes {
		return "Apple Notes is only on macOS"
	}
	return "merud found no Apple Notes database"
}

// NotesAccess opens and closes the Notes database once and returns nil
// when merud can read it, applenotes.ErrNoAccess when macOS refuses, or
// another error. merud logs it at startup, and the Settings screen shows
// NotesNoAccess while it fails with ErrNoAccess.
func (t *Tools) NotesAccess(ctx context.Context) error {
	if off := t.notesOff(); off != "" {
		return errors.New(off)
	}
	s, err := applenotes.Open(ctx, t.notesPath)
	if err != nil {
		return err
	}
	return s.Close()
}

// notesSpecs returns the three tools' schemas.
func notesSpecs() []engine.ToolSpec {
	return []engine.ToolSpec{
		{
			Name: SearchNotes,
			Description: "Searches the user's Apple Notes, the notes in the Mac's Notes app, for notes that hold every word of query, " +
				"in the title or the text, ignoring case. Notes whose title matches come first, newest first. " +
				"Each result gives the note's id, title, folder, modified date and a snippet; pass the id to read_note for the whole note.",
			Parameters: mustSchema(map[string]any{
				"query":  prop("string", "The words to find. Every word must appear in the note."),
				"folder": prop("string", "Search only this Notes folder. Leave it out to search every folder."),
				"limit":  prop("integer", fmt.Sprintf("How many notes to return, 1 to %d. Default %d.", maxNotes, defaultNotes)),
			}, "query"),
		},
		{
			Name: ListNotes,
			Description: "Lists the user's Apple Notes. With no folder it lists every Notes folder with its note count, then the newest notes; " +
				"with a folder it lists that folder's notes, newest first. Each note comes with its id, for read_note.",
			Parameters: mustSchema(map[string]any{
				"folder": prop("string", "The Notes folder to list. Leave it out for the folders and the newest notes."),
				"limit":  prop("integer", fmt.Sprintf("How many notes to list, 1 to %d. Default %d.", maxNotes, defaultNotes)),
			}),
		},
		{
			Name: ReadNote,
			Description: fmt.Sprintf("Reads one of the user's Apple Notes in full, by the id search_notes or list_notes gave. "+
				"It returns up to %d characters per call; when the note is longer, the result ends with the offset to pass next. "+
				"A locked note can't be read.", maxNoteChars),
			Parameters: mustSchema(map[string]any{
				"id":     prop("integer", "The note's id."),
				"offset": prop("integer", "Where to start, in characters from the start of the note. Default 0."),
			}, "id"),
		},
	}
}

// notesArgs is the JSON object the Apple Notes tools take; each uses the
// fields it needs.
type notesArgs struct {
	Query  string `json:"query"`
	Folder string `json:"folder"`
	Limit  int    `json:"limit"`
	ID     int64  `json:"id"`
	Offset int    `json:"offset"`
}

// callNotes runs the Apple Notes tool name with raw arguments. It opens
// the database, answers, and closes it. Without Full Disk Access it
// returns NotesNoAccess as an error, so the model reads what to tell the
// user.
func (t *Tools) callNotes(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var a notesArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("%s: bad arguments: %w", name, err)
	}
	limit := a.Limit
	if limit == 0 {
		limit = defaultNotes
	}
	if limit < 1 || limit > maxNotes {
		return "", fmt.Errorf("%s: limit must be 1 to %d", name, maxNotes)
	}
	s, err := applenotes.Open(ctx, t.notesPath)
	if err != nil {
		return "", notesError(name, err)
	}
	// defer closes the database when callNotes returns, on every path.
	defer s.Close()
	switch name {
	case SearchNotes:
		return t.searchNotes(ctx, s, a, limit)
	case ListNotes:
		return t.listNotes(ctx, s, a, limit)
	default:
		return t.readNote(ctx, s, a)
	}
}

// sentence is an error whose text is a whole sentence for the user, with
// a capital and a full stop, which the model reads and passes on. Go's
// usual error text is a lower-case fragment meant to be wrapped; these are
// never wrapped.
type sentence string

// Error returns the sentence. Having this method makes sentence an error.
func (s sentence) Error() string { return string(s) }

// notesError turns a reader error into the words the model reads.
func notesError(name string, err error) error {
	switch {
	case errors.Is(err, applenotes.ErrNoAccess):
		return sentence(NotesNoAccess)
	case errors.Is(err, applenotes.ErrNoDatabase):
		return sentence("Apple Notes has no notes on this Mac: the Notes app has never stored any here.")
	}
	return fmt.Errorf("%s: %w", name, err)
}

// searchNotes runs search_notes.
func (t *Tools) searchNotes(ctx context.Context, s *applenotes.Store, a notesArgs, limit int) (string, error) {
	if strings.TrimSpace(a.Query) == "" {
		return "", errors.New("search_notes: query is empty")
	}
	hits, err := s.Search(ctx, a.Query, a.Folder, limit)
	if err != nil {
		return "", notesError(SearchNotes, err)
	}
	if len(hits) == 0 {
		where := "the user's Apple Notes"
		if a.Folder != "" {
			where = "the Notes folder " + quote(a.Folder)
		}
		return fmt.Sprintf("No note in %s holds every word of %s.", where, quote(a.Query)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s:\n", notesCount(len(hits), "holds", "hold"), quote(a.Query))
	for _, h := range hits {
		b.WriteString("\n" + noteLine(h.Note))
		if h.Snippet != "" {
			b.WriteString("\n  " + h.Snippet)
		}
	}
	return b.String(), nil
}

// listNotes runs list_notes.
func (t *Tools) listNotes(ctx context.Context, s *applenotes.Store, a notesArgs, limit int) (string, error) {
	var b strings.Builder
	if a.Folder == "" {
		folders, err := s.Folders(ctx)
		if err != nil {
			return "", notesError(ListNotes, err)
		}
		fmt.Fprintf(&b, "%d Notes folders:\n", len(folders))
		for _, f := range folders {
			fmt.Fprintf(&b, "- %s (%s)\n", f.Name, notesCount(f.Notes, "", ""))
		}
		b.WriteString("\nThe newest notes:\n")
	}
	notes, err := s.Notes(ctx, a.Folder, limit)
	if err != nil {
		return "", notesError(ListNotes, err)
	}
	if len(notes) == 0 {
		if a.Folder != "" {
			return fmt.Sprintf("The Notes folder %s holds no notes, or there is no folder by that name.", quote(a.Folder)), nil
		}
		b.WriteString("(none)")
		return b.String(), nil
	}
	if a.Folder != "" {
		fmt.Fprintf(&b, "The newest notes in %s:\n", quote(a.Folder))
	}
	for _, n := range notes {
		b.WriteString("\n" + noteLine(n))
	}
	return b.String(), nil
}

// readNote runs read_note: one page of a note's text, as read_file pages
// a file.
func (t *Tools) readNote(ctx context.Context, s *applenotes.Store, a notesArgs) (string, error) {
	if a.ID <= 0 {
		return "", errors.New("read_note: id is missing; take it from search_notes or list_notes")
	}
	if a.Offset < 0 {
		return "", fmt.Errorf("read_note: offset %d is negative", a.Offset)
	}
	n, err := s.Note(ctx, a.ID)
	if errors.Is(err, applenotes.ErrNotFound) {
		return "", fmt.Errorf("read_note: no note has id %d; search or list the notes again for its id", a.ID)
	}
	if err != nil {
		return "", notesError(ReadNote, err)
	}
	if n.Locked {
		return fmt.Sprintf("%s\nThis note is locked in the Notes app, so Meru can't read it.", noteLine(n)), nil
	}
	runes := []rune(n.Text)
	if a.Offset > 0 && a.Offset >= len(runes) {
		return "", fmt.Errorf("read_note: offset %d is past the end; the note has %d characters", a.Offset, len(runes))
	}
	end := min(a.Offset+maxNoteChars, len(runes))
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d characters)\n\n%s", noteLine(n), len(runes), string(runes[a.Offset:end]))
	if end < len(runes) {
		fmt.Fprintf(&b, "\n\n[%d more characters. To read on, call read_note with id %d and offset %d.]", len(runes)-end, n.ID, end)
	}
	return b.String(), nil
}

// noteLine describes one note on one line: its id, title, folder and the
// day it last changed.
func noteLine(n applenotes.Note) string {
	title := n.Title
	if title == "" {
		title = "(untitled)"
	}
	line := fmt.Sprintf("- id %d: %s, in %s", n.ID, quote(title), n.Folder)
	if !n.Modified.IsZero() {
		line += ", modified " + n.Modified.Local().Format(time.DateOnly)
	}
	if n.Locked {
		line += " (locked)"
	}
	return line
}

// notesCount writes n with "note" or "notes", followed by one or many,
// the verb for one note or for several: notesCount(1, "holds", "hold") is
// "1 note holds". Empty verbs leave the verb out.
func notesCount(n int, one, many string) string {
	s := fmt.Sprintf("%d notes", n)
	verb := many
	if n == 1 {
		s, verb = "1 note", one
	}
	if verb != "" {
		s += " " + verb
	}
	return s
}

// quote puts s in curly quotes, so a title with commas reads as one thing.
func quote(s string) string { return "“" + s + "”" }
