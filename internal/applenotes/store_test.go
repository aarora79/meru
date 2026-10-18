// This file tests Open and the queries against a small database the test
// builds itself in the shape of NoteStore.sqlite, in both the current
// column layout and an older one. No real note is in the repo.

package applenotes

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"
	"google.golang.org/protobuf/encoding/protowire"
)

// body builds a note's ZDATA the way the Notes app stores it: the text in
// Note field 2, inside Document field 3, inside NoteStoreProto field 2,
// gzipped. A Document version field (2, a number) comes first, as in real
// data, so the walk has to skip a field that isn't the one it wants.
func body(t *testing.T, text string) []byte {
	t.Helper()
	var note []byte
	note = protowire.AppendTag(note, fieldText, protowire.BytesType)
	note = protowire.AppendString(note, text)
	var doc []byte
	doc = protowire.AppendTag(doc, 2, protowire.VarintType)
	doc = protowire.AppendVarint(doc, 1)
	doc = protowire.AppendTag(doc, fieldNote, protowire.BytesType)
	doc = protowire.AppendBytes(doc, note)
	var store []byte
	store = protowire.AppendTag(store, fieldDocument, protowire.BytesType)
	store = protowire.AppendBytes(store, doc)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(store); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fixture is one note the test database holds.
type fixture struct {
	id      int64
	title   string
	folder  int64 // Z_PK of its folder
	days    int   // modified this many days after the Apple epoch
	text    string
	deleted bool
	locked  bool
}

// layoutKind picks the column names the test database uses.
type layoutKind int

const (
	modern layoutKind = iota // ZTITLE1, ZTITLE2, ZMODIFICATIONDATE1, ZFOLDERTYPE
	older                    // ZTITLE for both, ZMODIFICATIONDATE, no ZFOLDERTYPE
)

// makeDB writes a NoteStore-shaped database into a temporary folder and
// returns its path. Folders: 1 "Notes", 2 "Work", 3 "Recently Deleted"
// (the trash, in the modern layout).
func makeDB(t *testing.T, kind layoutKind, notes []fixture) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	db, err := driver.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	noteTitle, folderTitle, date, extra := "ZTITLE1", "ZTITLE2", "ZMODIFICATIONDATE1", ", ZFOLDERTYPE INTEGER"
	if kind == older {
		noteTitle, folderTitle, date, extra = "ZTITLE", "ZTITLE", "ZMODIFICATIONDATE", ""
	}
	titles := noteTitle + " TEXT"
	if folderTitle != noteTitle {
		titles += ", " + folderTitle + " TEXT"
	}
	exec(t, db, `CREATE TABLE Z_PRIMARYKEY (Z_ENT INTEGER, Z_NAME TEXT)`)
	exec(t, db, `INSERT INTO Z_PRIMARYKEY VALUES (5, 'ICAttachment'), (12, 'ICNote'), (15, 'ICFolder')`)
	exec(t, db, `CREATE TABLE ZICCLOUDSYNCINGOBJECT (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, `+titles+
		`, ZFOLDER INTEGER, `+date+` REAL, ZMARKEDFORDELETION INTEGER, ZISPASSWORDPROTECTED INTEGER`+extra+`)`)
	exec(t, db, `CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`)
	folder := func(pk int64, name string, typ int) {
		if kind == older {
			exec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, Z_ENT, ZTITLE, ZMARKEDFORDELETION) VALUES (?, 15, ?, 0)`, pk, name)
			return
		}
		exec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, Z_ENT, ZTITLE2, ZMARKEDFORDELETION, ZFOLDERTYPE) VALUES (?, 15, ?, 0, ?)`, pk, name, typ)
	}
	folder(1, "Notes", 0)
	folder(2, "Work", 0)
	folder(3, "Recently Deleted", trashFolderType)
	for _, n := range notes {
		exec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, Z_ENT, `+noteTitle+`, ZFOLDER, `+date+`, ZMARKEDFORDELETION, ZISPASSWORDPROTECTED) VALUES (?, 12, ?, ?, ?, ?, ?)`,
			n.id, n.title, n.folder, float64(n.days*86400), n.deleted, n.locked)
		exec(t, db, `INSERT INTO ZICNOTEDATA (ZNOTE, ZDATA) VALUES (?, ?)`, n.id, body(t, n.text))
	}
	return path
}

// exec runs one statement and fails the test on an error.
func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// sample is the notes most tests use.
var sample = []fixture{
	{id: 101, title: "Garden plan", folder: 1, days: 100, text: "Garden plan\nSow tomatoes in April. Water the basil daily."},
	{id: 102, title: "Trip to Lisbon", folder: 2, days: 300, text: "Trip to Lisbon\nFlights booked. Visit the garden at Estrela."},
	{id: 103, title: "Old garden note", folder: 3, days: 50, text: "This note sits in Recently Deleted."},
	{id: 104, title: "Gone", folder: 1, days: 400, text: "Marked for deletion.", deleted: true},
	{id: 105, title: "Bank codes", folder: 2, days: 200, text: "secret garden codes", locked: true},
}

// open opens path and closes the store when the test ends.
func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLayouts(t *testing.T) {
	for _, tt := range []struct {
		name string
		kind layoutKind
	}{{"modern", modern}, {"older", older}} {
		t.Run(tt.name, func(t *testing.T) {
			s := open(t, makeDB(t, tt.kind, sample))
			notes, err := s.Notes(context.Background(), "", 10)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, n := range notes {
				ids = append(ids, n.ID)
			}
			// Newest first. 104 is deleted; 103 is in the trash, which
			// only the modern layout marks.
			want := []int64{102, 105, 101}
			if tt.kind == older {
				want = []int64{102, 105, 101, 103}
			}
			if !slicesEqual(ids, want) {
				t.Errorf("notes = %v, want %v", ids, want)
			}
			if notes[0].Folder != "Work" || notes[0].Title != "Trip to Lisbon" || notes[0].Text != "" {
				t.Errorf("first note = %+v", notes[0])
			}
			if got := notes[0].Modified; !got.Equal(appleEpoch.Add(300 * 24 * time.Hour)) {
				t.Errorf("modified = %v", got)
			}
		})
	}
}

func TestFolders(t *testing.T) {
	s := open(t, makeDB(t, modern, sample))
	folders, err := s.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Recently Deleted stays out; Notes counts 101 only (104 is deleted).
	want := []Folder{{"Notes", 1}, {"Work", 2}}
	if len(folders) != len(want) || folders[0] != want[0] || folders[1] != want[1] {
		t.Errorf("folders = %+v, want %+v", folders, want)
	}
}

func TestNotesInFolder(t *testing.T) {
	s := open(t, makeDB(t, modern, sample))
	notes, err := s.Notes(context.Background(), "work", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].ID != 102 {
		t.Errorf("notes = %+v, want 102 alone (folder matched ignoring case, limit 1)", notes)
	}
}

func TestNote(t *testing.T) {
	s := open(t, makeDB(t, modern, sample))
	ctx := context.Background()
	n, err := s.Note(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.Text, "Sow tomatoes") {
		t.Errorf("text = %q", n.Text)
	}
	locked, err := s.Note(ctx, 105)
	if err != nil {
		t.Fatal(err)
	}
	if !locked.Locked || locked.Text != "" {
		t.Errorf("locked note = %+v, want Locked and no text", locked)
	}
	for _, id := range []int64{104, 999} {
		if _, err := s.Note(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Note(%d) err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestSearch(t *testing.T) {
	s := open(t, makeDB(t, modern, sample))
	ctx := context.Background()
	tests := []struct {
		name, query, folder string
		want                []int64
	}{
		// 101 has "garden" in its title, so it ranks above 102, which is
		// newer but has it only in the text. 103 (trash), 104 (deleted)
		// and 105 (locked, title doesn't match) stay out.
		{"title first", "Garden", "", []int64{101, 102}},
		{"every word must match", "garden lisbon", "", []int64{102}},
		{"folder", "garden", "Notes", []int64{101}},
		{"a locked note matches on its title", "bank", "", []int64{105}},
		{"no match", "zebra", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := s.Search(ctx, tt.query, tt.folder, 10)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, h := range hits {
				ids = append(ids, h.Note.ID)
			}
			if !slicesEqual(ids, tt.want) {
				t.Errorf("hits = %v, want %v", ids, tt.want)
			}
		})
	}
	if _, err := s.Search(ctx, "   ", "", 10); err == nil {
		t.Error("an empty query should fail")
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("filler ", 100) + "the Estrela garden is green " + strings.Repeat("more ", 100)
	got := snippet(long, "estrela")
	if !strings.Contains(got, "Estrela garden") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("snippet = %q", got)
	}
	if got := snippet("short text", "absent"); got != "short text" {
		t.Errorf("snippet without a match = %q", got)
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "missing.sqlite")); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("missing file: err = %v, want ErrNoDatabase", err)
	}
	// A file merud may not read stands in for a missing Full Disk Access
	// grant. root reads anything, so the case means nothing there.
	if os.Geteuid() != 0 {
		path := makeDB(t, modern, sample)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, path); !errors.Is(err, ErrNoAccess) {
			t.Errorf("unreadable file: err = %v, want ErrNoAccess", err)
		}
	}
	// A SQLite file that isn't a Notes database fails with a layout error.
	other := filepath.Join(t.TempDir(), "other.sqlite")
	db, err := driver.Open("file:" + other)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db, `CREATE TABLE x (y)`)
	db.Close()
	if _, err := Open(ctx, other); err == nil || errors.Is(err, ErrNoAccess) {
		t.Errorf("not a Notes database: err = %v, want a layout error", err)
	}
}

func TestNoteText(t *testing.T) {
	if got, err := noteText(body(t, "héllo wörld")); err != nil || got != "héllo wörld" {
		t.Errorf("noteText = %q, %v", got, err)
	}
	if _, err := noteText([]byte("not gzip")); err == nil {
		t.Error("plain bytes should fail")
	}
	// Gzip around a message with no text field.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 7))
	zw.Close()
	if _, err := noteText(buf.Bytes()); !errors.Is(err, errNoText) {
		t.Errorf("no text field: err = %v, want errNoText", err)
	}
}

// slicesEqual reports whether a and b hold the same IDs in the same order;
// a nil and an empty slice count as equal.
func slicesEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
