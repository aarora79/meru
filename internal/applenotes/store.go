// This file holds Open and the queries over NoteStore.sqlite: the folders,
// the notes in one folder or all of them, a word search, and one note's
// full text.

package applenotes

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// ErrNoAccess means macOS keeps merud out of the Notes database: it needs
// Full Disk Access. macOS shows no prompt for that; it refuses the open.
var ErrNoAccess = errors.New("macOS denies access to the Notes database; merud needs Full Disk Access")

// ErrNoDatabase means there is no Notes database at the path, as on a Mac
// where Notes has never run, or on another system.
var ErrNoDatabase = errors.New("no Apple Notes database found")

// ErrNotFound means no note has the ID asked for.
var ErrNotFound = errors.New("no note with that ID")

// appleEpoch is when Apple's Core Data dates start: they count seconds
// from 1 January 2001, UTC.
var appleEpoch = time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)

// trashFolderType is ZFOLDERTYPE's value for the Recently Deleted folder.
// Notes in it are deleted as far as the user is concerned, so they stay out.
const trashFolderType = 1

// DefaultPath returns where the Notes app keeps its database for the
// current user, or "" when the home folder is unknown.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Group Containers", "group.com.apple.notes", "NoteStore.sqlite")
}

// Folder is one Notes folder and how many notes it holds.
type Folder struct {
	Name  string
	Notes int
}

// Note is one note. Text is filled only by Store.Note and Store.Search;
// the lists leave it empty. A locked note never has Text.
type Note struct {
	ID       int64
	Title    string
	Folder   string
	Modified time.Time
	Locked   bool
	Text     string
}

// Hit is one search result: the note and a short piece of its text around
// the first match.
type Hit struct {
	Note    Note
	Snippet string
}

// Store is an open, read-only Notes database. Make one with Open and close
// it when done; it holds one SQLite connection.
type Store struct {
	db *sql.DB
	// The schema facts Open found; see layout.
	layout layout
}

// layout holds what Open learned about this database's schema: the entity
// numbers Core Data gave notes and folders, and the names of the columns
// that differ between macOS releases. An optional column is "" when the
// database lacks it.
type layout struct {
	noteEnt, folderEnt int64
	noteTitle          string // ZTITLE1 on recent releases
	folderTitle        string // ZTITLE2 on recent releases
	modified           string // the date column, or a COALESCE over the ones that exist
	deleted            string // ZMARKEDFORDELETION, optional
	locked             string // ZISPASSWORDPROTECTED, optional
	folderType         string // ZFOLDERTYPE, optional
}

// Open opens the database at path read-only and learns its layout. It
// fails with ErrNoDatabase when there is no file, with ErrNoAccess when
// macOS refuses to let merud read it, and with another error when the
// layout isn't one it knows.
func Open(ctx context.Context, path string) (*Store, error) {
	// Stat first: without Full Disk Access macOS refuses even this, which
	// tells a missing grant from a missing file.
	if _, err := os.Stat(path); err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, ErrNoDatabase
		case errors.Is(err, fs.ErrPermission):
			return nil, ErrNoAccess
		}
		return nil, fmt.Errorf("open notes: %w", err)
	}
	db, err := driver.Open(dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open notes: %w", err)
	}
	// One connection: each call opens a Store, makes a few queries and
	// closes it.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if s.layout, err = readLayout(ctx, db); err != nil {
		// errors.Join keeps both errors if Close fails too.
		return nil, errors.Join(noAccess(err), db.Close())
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// dataSourceName builds the SQLite URI for a read-only open of path:
// mode=ro opens without write access, query_only refuses any statement
// that would write, and busy_timeout waits up to 5 seconds while the Notes
// app holds a lock. The path is escaped the way internal/store escapes
// meru.db's, since this one holds a space ("Group Containers").
func dataSourceName(path string) string {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u := url.URL{Path: filepath.ToSlash(path)}
	return "file:" + u.EscapedPath() + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

// noAccess turns SQLite's refusal to open the file into ErrNoAccess, and
// returns any other error as it is. Without Full Disk Access, SQLite
// reports "unable to open database file" (CANTOPEN) on the first query.
func noAccess(err error) error {
	// errors.As finds a *sqlite3.Error anywhere in err's chain.
	var serr *sqlite3.Error
	if errors.As(err, &serr) {
		switch serr.Code() {
		case sqlite3.CANTOPEN, sqlite3.PERM, sqlite3.AUTH:
			return ErrNoAccess
		}
	}
	return err
}

// readLayout finds the entity numbers and the column names this
// database uses. It fails when a table or column it needs is missing.
func readLayout(ctx context.Context, db *sql.DB) (layout, error) {
	var l layout
	rows, err := db.QueryContext(ctx, `SELECT Z_ENT, Z_NAME FROM Z_PRIMARYKEY WHERE Z_NAME IN ('ICNote', 'ICFolder')`)
	if err != nil {
		return l, fmt.Errorf("read notes layout: %w", err)
	}
	for rows.Next() {
		var ent int64
		var name string
		if err := rows.Scan(&ent, &name); err != nil {
			_ = rows.Close() // the scan error is the one worth reporting
			return l, fmt.Errorf("read notes layout: %w", err)
		}
		if name == "ICNote" {
			l.noteEnt = ent
		} else {
			l.folderEnt = ent
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return l, fmt.Errorf("read notes layout: %w", err)
	}
	if l.noteEnt == 0 || l.folderEnt == 0 {
		return l, errors.New("read notes layout: no ICNote or ICFolder entity in Z_PRIMARYKEY")
	}

	cols, err := columns(ctx, db, "ZICCLOUDSYNCINGOBJECT")
	if err != nil {
		return l, err
	}
	// first returns the first name in names that the table has, or "".
	first := func(names ...string) string {
		for _, n := range names {
			if cols[n] {
				return n
			}
		}
		return ""
	}
	l.noteTitle = first("ZTITLE1", "ZTITLE")
	l.folderTitle = first("ZTITLE2", "ZTITLE")
	var dates []string
	for _, n := range []string{"ZMODIFICATIONDATE1", "ZMODIFICATIONDATE", "ZMODIFIEDDATE"} {
		if cols[n] {
			dates = append(dates, "n."+n)
		}
	}
	// SQLite's COALESCE needs two arguments or more, so one date column
	// stands alone.
	switch len(dates) {
	case 0:
	case 1:
		l.modified = dates[0]
	default:
		l.modified = "COALESCE(" + strings.Join(dates, ", ") + ")"
	}
	l.deleted = first("ZMARKEDFORDELETION")
	l.locked = first("ZISPASSWORDPROTECTED")
	l.folderType = first("ZFOLDERTYPE")
	if l.noteTitle == "" || l.folderTitle == "" || !cols["ZFOLDER"] || l.modified == "" {
		return l, errors.New("read notes layout: ZICCLOUDSYNCINGOBJECT lacks a title, folder or date column Meru knows")
	}
	data, err := columns(ctx, db, "ZICNOTEDATA")
	if err != nil {
		return l, err
	}
	if !data["ZNOTE"] || !data["ZDATA"] {
		return l, errors.New("read notes layout: ZICNOTEDATA lacks ZNOTE or ZDATA")
	}
	return l, nil
}

// columns returns the set of column names table has. The table name is
// one of the two this file names, never input, so it is safe to put in
// the PRAGMA, which takes no parameters.
func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	// #nosec G202 -- table is one of two names this file passes, never input.
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info('"+table+"')")
	if err != nil {
		return nil, fmt.Errorf("read notes layout: %w", err)
	}
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close() // the scan error is the one worth reporting
			return nil, fmt.Errorf("read notes layout: %w", err)
		}
		cols[name] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read notes layout: %w", err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("read notes layout: no table %s", table)
	}
	return cols, nil
}

// noteFilter returns the SQL conditions that keep a row of n (notes) with
// folder f out of the results: deleted notes, and notes in Recently
// Deleted.
func (l layout) noteFilter() string {
	cond := "n.Z_ENT = ?"
	if l.deleted != "" {
		cond += " AND COALESCE(n." + l.deleted + ", 0) = 0"
	}
	if l.folderType != "" {
		cond += " AND COALESCE(f." + l.folderType + ", 0) != " + fmt.Sprint(trashFolderType)
	}
	return cond
}

// noteQuery is the SELECT every note query shares: the ID, title, folder,
// date, the locked flag and, when withData is true, the note's data.
func (l layout) noteQuery(withData bool) string {
	locked := "0"
	if l.locked != "" {
		locked = "COALESCE(n." + l.locked + ", 0)"
	}
	data := "NULL"
	join := ""
	if withData {
		data = "d.ZDATA"
		join = " LEFT JOIN ZICNOTEDATA d ON d.ZNOTE = n.Z_PK"
	}
	return "SELECT n.Z_PK, COALESCE(n." + l.noteTitle + ", ''), COALESCE(f." + l.folderTitle + ", ''), " +
		"COALESCE(" + l.modified + ", 0), " + locked + ", " + data +
		" FROM ZICCLOUDSYNCINGOBJECT n LEFT JOIN ZICCLOUDSYNCINGOBJECT f ON f.Z_PK = n.ZFOLDER" + join +
		" WHERE " + l.noteFilter()
}

// scanNotes reads the rows a noteQuery returns. With decode true it fills
// each unlocked note's Text from its data; a note whose data won't decode
// keeps an empty Text, as a note with no text would.
func scanNotes(rows *sql.Rows, decode bool) ([]Note, error) {
	var notes []Note
	for rows.Next() {
		var n Note
		var secs float64
		var locked int64
		var data []byte
		if err := rows.Scan(&n.ID, &n.Title, &n.Folder, &secs, &locked, &data); err != nil {
			return nil, err
		}
		n.Locked = locked != 0
		if secs > 0 {
			n.Modified = appleEpoch.Add(time.Duration(secs * float64(time.Second)))
		}
		if decode && !n.Locked && len(data) > 0 {
			// A decode error leaves Text empty: one damaged note
			// mustn't hide the rest.
			n.Text, _ = noteText(data)
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// all returns every note, filtered by folder when folder isn't "", with
// its text when decode is true, newest first.
func (s *Store) all(ctx context.Context, folder string, decode bool) ([]Note, error) {
	q := s.layout.noteQuery(decode)
	args := []any{s.layout.noteEnt}
	if folder != "" {
		// Folder names match ignoring case, as a person types them.
		q += " AND f." + s.layout.folderTitle + " = ? COLLATE NOCASE"
		args = append(args, folder)
	}
	q += " ORDER BY 4 DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("read notes: %w", noAccess(err))
	}
	// defer closes the rows when all returns, on every path.
	defer rows.Close()
	notes, err := scanNotes(rows, decode)
	if err != nil {
		return nil, fmt.Errorf("read notes: %w", noAccess(err))
	}
	return notes, nil
}

// Folders returns every folder but Recently Deleted, with how many notes
// each holds, by name.
func (s *Store) Folders(ctx context.Context) ([]Folder, error) {
	notes, err := s.all(ctx, "", false)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, n := range notes {
		counts[n.Folder]++
	}
	// A folder with no notes left still shows, with 0.
	// #nosec G202 -- the column names come from readLayout's fixed lists; values go in as ? parameters.
	q := "SELECT COALESCE(f." + s.layout.folderTitle + ", '') FROM ZICCLOUDSYNCINGOBJECT f WHERE f.Z_ENT = ?"
	if s.layout.deleted != "" {
		q += " AND COALESCE(f." + s.layout.deleted + ", 0) = 0"
	}
	if s.layout.folderType != "" {
		q += " AND COALESCE(f." + s.layout.folderType + ", 0) != " + fmt.Sprint(trashFolderType)
	}
	rows, err := s.db.QueryContext(ctx, q, s.layout.folderEnt)
	if err != nil {
		return nil, fmt.Errorf("read folders: %w", noAccess(err))
	}
	defer rows.Close()
	seen := map[string]bool{}
	var folders []Folder
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read folders: %w", err)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		folders = append(folders, Folder{Name: name, Notes: counts[name]})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read folders: %w", err)
	}
	slices.SortFunc(folders, func(a, b Folder) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return folders, nil
}

// Notes returns up to limit notes, newest first, from folder, or from
// every folder when folder is "". The notes carry no Text.
func (s *Store) Notes(ctx context.Context, folder string, limit int) ([]Note, error) {
	notes, err := s.all(ctx, folder, false)
	if err != nil {
		return nil, err
	}
	return notes[:min(limit, len(notes))], nil
}

// Note returns the note with id, with its text. It fails with ErrNotFound
// when no note that isn't deleted has that ID.
func (s *Store) Note(ctx context.Context, id int64) (Note, error) {
	// #nosec G202 -- noteQuery holds only readLayout's column names; the ID is a ? parameter.
	q := s.layout.noteQuery(true) + " AND n.Z_PK = ?"
	rows, err := s.db.QueryContext(ctx, q, s.layout.noteEnt, id)
	if err != nil {
		return Note{}, fmt.Errorf("read note: %w", noAccess(err))
	}
	defer rows.Close()
	notes, err := scanNotes(rows, true)
	if err != nil {
		return Note{}, fmt.Errorf("read note: %w", noAccess(err))
	}
	if len(notes) == 0 {
		return Note{}, ErrNotFound
	}
	return notes[0], nil
}

// Search returns up to limit notes that hold every word of query, in
// their title or text, ignoring case: notes whose title holds every word
// first, then the rest, newest first within each. folder, when not "",
// limits the search to that folder. Each hit carries a snippet of the text
// around the first word's first match. A locked note matches on its title
// alone.
func (s *Store) Search(ctx context.Context, query, folder string, limit int) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, errors.New("search notes: the query has no words")
	}
	notes, err := s.all(ctx, folder, true)
	if err != nil {
		return nil, err
	}
	var inTitle, inText []Hit
	for _, n := range notes {
		title, text := strings.ToLower(n.Title), strings.ToLower(n.Text)
		if !holdsAll(title+"\n"+text, words) {
			continue
		}
		h := Hit{Note: n, Snippet: snippet(n.Text, words[0])}
		if holdsAll(title, words) {
			inTitle = append(inTitle, h)
		} else {
			inText = append(inText, h)
		}
	}
	// all returned the notes newest first, and each list kept that order.
	hits := append(inTitle, inText...)
	return hits[:min(limit, len(hits))], nil
}

// holdsAll reports whether s holds every one of words.
func holdsAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// snippetChars is about how long a search snippet is, in characters.
const snippetChars = 240

// snippet returns about snippetChars characters of text around the first
// place word appears, ignoring case, on one line, with "…" where it cut.
// It returns the start of text when word isn't in it, as when the match
// was in the title.
func snippet(text, word string) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	// Find the match in runes, not bytes, so a cut never splits a
	// character. ToLower can change a string's length in bytes but keeps
	// one rune per rune for the scripts notes hold.
	start := 0
	if i := strings.Index(strings.ToLower(flat), word); i >= 0 {
		start = max(utf8.RuneCountInString(flat[:i])-snippetChars/3, 0)
	}
	end := min(start+snippetChars, len(runes))
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
