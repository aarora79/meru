// This file tests search_notes, list_notes and read_note against a small
// database in the shape of the Notes app's NoteStore.sqlite, which the
// test builds itself. internal/applenotes tests the reader in depth; these
// tests check what the model reads.

package builtin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3/driver"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/aarora79/meru/internal/config"
)

// notesBody builds a note's stored data: the text in the protobuf path
// the Notes app uses, gzipped.
func notesBody(t *testing.T, text string) []byte {
	t.Helper()
	note := protowire.AppendString(protowire.AppendTag(nil, 2, protowire.BytesType), text)
	doc := protowire.AppendBytes(protowire.AppendTag(nil, 3, protowire.BytesType), note)
	store := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), doc)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(store)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// notesDB writes a NoteStore-shaped database with two folders and three
// notes, one of them long and one locked, and returns its path.
func notesDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	db, err := driver.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	long := "Reading list\n" + strings.Repeat("a line about books. ", 1000)
	for _, q := range []string{
		`CREATE TABLE Z_PRIMARYKEY (Z_ENT INTEGER, Z_NAME TEXT)`,
		`INSERT INTO Z_PRIMARYKEY VALUES (12, 'ICNote'), (15, 'ICFolder')`,
		`CREATE TABLE ZICCLOUDSYNCINGOBJECT (Z_PK INTEGER PRIMARY KEY, Z_ENT INTEGER, ZTITLE1 TEXT, ZTITLE2 TEXT,
			ZFOLDER INTEGER, ZMODIFICATIONDATE1 REAL, ZMARKEDFORDELETION INTEGER, ZISPASSWORDPROTECTED INTEGER, ZFOLDERTYPE INTEGER)`,
		`CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, Z_ENT, ZTITLE2) VALUES (1, 15, 'Notes'), (2, 15, 'Work')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []struct {
		id           int64
		title        string
		folder, days int
		locked       int
		text         string
	}{
		{7, "Garden plan", 1, 100, 0, "Garden plan\nSow tomatoes in April."},
		{8, "Reading list", 2, 200, 0, long},
		{9, "Bank codes", 2, 50, 1, "never shown"},
	} {
		if _, err := db.Exec(`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, Z_ENT, ZTITLE1, ZFOLDER, ZMODIFICATIONDATE1, ZMARKEDFORDELETION, ZISPASSWORDPROTECTED) VALUES (?, 12, ?, ?, ?, 0, ?)`,
			n.id, n.title, n.folder, float64(n.days*86400), n.locked); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO ZICNOTEDATA (ZNOTE, ZDATA) VALUES (?, ?)`, n.id, notesBody(t, n.text)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// notesTools returns built-in tools with the notes tools reading path.
func notesTools(t *testing.T, path string) *Tools {
	t.Helper()
	tools := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{MaxResults: 8}, nil, "", nil, nil, nil)
	tools.UseNotes(path)
	return tools
}

func TestNotesTools(t *testing.T) {
	tools := notesTools(t, notesDB(t))
	ctx := context.Background()
	tests := []struct {
		name, tool, args string
		want             []string // parts the result must hold
		wantErr          bool
	}{
		{"search", SearchNotes, `{"query":"tomatoes"}`, []string{"1 note holds", "id 7", "Garden plan", "in Notes", "Sow tomatoes"}, false},
		{"search with no hit", SearchNotes, `{"query":"zebra"}`, []string{"No note in the user's Apple Notes"}, false},
		{"search in a folder", SearchNotes, `{"query":"garden","folder":"Work"}`, []string{"No note in the Notes folder “Work”"}, false},
		{"empty query", SearchNotes, `{"query":"  "}`, nil, true},
		{"limit too big", SearchNotes, `{"query":"garden","limit":99}`, nil, true},
		{"list folders and newest", ListNotes, `{}`, []string{"2 Notes folders", "- Notes (1 note)", "- Work (2 notes)", "The newest notes", "id 8"}, false},
		{"list one folder", ListNotes, `{"folder":"work"}`, []string{"The newest notes in “work”", "id 8", "id 9", "(locked)"}, false},
		{"read", ReadNote, `{"id":7}`, []string{"Garden plan", "Sow tomatoes in April."}, false},
		{"read a long note", ReadNote, `{"id":8}`, []string{"more characters. To read on, call read_note with id 8 and offset 12000"}, false},
		{"read on", ReadNote, `{"id":8,"offset":12000}`, []string{"a line about books"}, false},
		{"read a locked note", ReadNote, `{"id":9}`, []string{"locked in the Notes app"}, false},
		{"read a missing note", ReadNote, `{"id":99}`, nil, true},
		{"read with no id", ReadNote, `{}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tools.Call(ctx, tt.tool, json.RawMessage(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, want %v: %s", res.IsError, tt.wantErr, res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result lacks %q:\n%s", w, res.Text)
				}
			}
			if strings.Contains(res.Text, "never shown") {
				t.Error("a locked note's text reached the result")
			}
		})
	}
}

func TestNotesNoAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	path := notesDB(t)
	// A file merud may not read stands in for a missing Full Disk Access
	// grant.
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	tools := notesTools(t, path)
	res, err := tools.Call(context.Background(), ListNotes, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Text != NotesNoAccess {
		t.Errorf("result = %+v, want the Full Disk Access sentence", res)
	}
	if err := tools.NotesAccess(context.Background()); err == nil {
		t.Error("NotesAccess = nil, want an error")
	}
}

func TestNotesOff(t *testing.T) {
	tools := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{MaxResults: 8}, nil, "", nil, nil, nil)
	for _, s := range tools.Tools() {
		if IsNotesTool(s.Name) {
			t.Errorf("%s offered with no Notes database", s.Name)
		}
	}
	if res, err := tools.Call(context.Background(), SearchNotes, json.RawMessage(`{"query":"x"}`)); err == nil {
		t.Errorf("search_notes ran with no Notes database: %+v", res)
	}
	if tools.NotesAccess(context.Background()) == nil {
		t.Error("NotesAccess = nil with no Notes database")
	}
}
