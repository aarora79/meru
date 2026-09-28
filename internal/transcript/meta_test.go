// This file tests what the user sets on a chat: meta lines, the folder
// and tag rules, the folder list, Delete, and incognito sessions.

package transcript

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// newAsked returns a new session under a temporary folder that holds one
// question, so List shows it, and the folder.
func newAsked(t *testing.T) (*Session, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Line{Type: TypeUser, Text: "plan the garden beds"}); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestMetaOf(t *testing.T) {
	tests := []struct {
		name  string
		lines []Line
		want  Meta
	}{
		{"no meta line", []Line{{Type: TypeUser, Text: "hi"}}, Meta{}},
		{"one", []Line{{Type: TypeMeta, Folder: "Garden", Tags: []string{"beds"}}}, Meta{Folder: "Garden", Tags: []string{"beds"}}},
		{
			"the newest wins, and an empty one clears",
			[]Line{
				{Type: TypeMeta, Folder: "Garden", Tags: []string{"beds"}},
				{Type: TypeUser, Text: "more"},
				{Type: TypeMeta, Tags: []string{"soil"}},
			},
			Meta{Tags: []string{"soil"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetaOf(tt.lines); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MetaOf = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSetMeta checks that the meta line round-trips through the file and
// List, and that the file keeps its modification time.
func TestSetMeta(t *testing.T) {
	s, dir := newAsked(t)
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(s.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(Meta{Folder: "Garden", Tags: []string{"beds", "soil"}}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := s.SetMeta(Meta{Folder: "Yard", Tags: []string{"beds"}}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("mtime = %v, want %v kept", info.ModTime(), old)
	}
	got, err := s.Meta()
	if err != nil || got.Folder != "Yard" || !reflect.DeepEqual(got.Tags, []string{"beds"}) {
		t.Errorf("Meta = %+v, %v", got, err)
	}
	// The line is plain JSON, so grep finds a tag in the file.
	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type":"meta","folder":"Yard","tags":["beds"]`) {
		t.Errorf("file holds %s", b)
	}
	list, err := List(dir, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list[0].Folder != "Yard" || !reflect.DeepEqual(list[0].Tags, []string{"beds"}) || list[0].Turns != 1 {
		t.Errorf("List row = %+v", list[0])
	}
}

func TestCleanFolder(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"  Garden   plans ", "Garden plans", true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("a", 61), "", false},
		{"bad\x07bell", "", false},
	}
	for _, tt := range tests {
		got, err := CleanFolder(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("CleanFolder(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestWithTags(t *testing.T) {
	tests := []struct {
		name              string
		tags, add, remove []string
		want              []string
		ok                bool
	}{
		{"add, cleaned and once", nil, []string{"#Beds", "beds", "soil_test"}, nil, []string{"beds", "soil_test"}, true},
		{"remove", []string{"beds", "soil"}, nil, []string{"BEDS"}, []string{"soil"}, true},
		{"two words", nil, []string{"two words"}, nil, nil, false},
		{"empty", nil, []string{" "}, nil, nil, false},
		{"too long", nil, []string{strings.Repeat("x", 33)}, nil, nil, false},
		{"too many", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}, []string{"m"}, nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WithTags(tt.tags, tt.add, tt.remove)
			if (err == nil) != tt.ok || (tt.ok && !reflect.DeepEqual(got, tt.want)) {
				t.Errorf("WithTags = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestFoldersFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	got, err := LoadFolders(dir)
	if err != nil || got != nil {
		t.Fatalf("LoadFolders of a missing file = %v, %v", got, err)
	}
	want := []string{"Garden", "Trips"}
	if err := SaveFolders(dir, want); err != nil {
		t.Fatalf("SaveFolders: %v", err)
	}
	if got, err = LoadFolders(dir); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("LoadFolders = %v, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, FoldersFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("folders.json mode = %v, %v", info.Mode(), err)
	}
	if err := os.WriteFile(filepath.Join(dir, FoldersFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFolders(dir); err == nil {
		t.Error("LoadFolders read a broken file without an error")
	}
	// The folder list never shows up as a session.
	if list, err := List(dir, 0); err != nil || len(list) != 0 {
		t.Errorf("List = %v, %v", list, err)
	}
}

func TestDelete(t *testing.T) {
	s, dir := newAsked(t)
	outside := filepath.Join(t.TempDir(), "keep.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := "2026-01-02T030405-abcd"
	if err := os.MkdirAll(filepath.Dir(sessionPath(dir, link)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sessionPath(dir, link)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		id   string
		ok   bool
	}{
		{"a path", "../../keep", false},
		{"not an ID", "garden", false},
		{"an incognito ID", "incognito-0123abcd", false},
		{"no such session", "2026-01-01T000000-0000", false},
		{"a symbolic link", link, false},
		{"the session", s.ID(), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Delete(dir, tt.id); (err == nil) != tt.ok {
				t.Errorf("Delete(%q) = %v", tt.id, err)
			}
		})
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the session file is still there: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the file the link points at is gone: %v", err)
	}
}

func TestIncognito(t *testing.T) {
	dir := t.TempDir()
	s := NewIncognito()
	if !s.Incognito() || !IsIncognito(s.ID()) || s.Path() != "" {
		t.Fatalf("NewIncognito = %q, path %q", s.ID(), s.Path())
	}
	for _, l := range []Line{
		{Type: TypeUser, Text: "what is a raised bed?"},
		{Type: TypeAssistant, Text: "A bed of soil above the ground."},
	} {
		if err := s.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.History(5)
	if err != nil || len(h) != 2 || h[1].Content != "A bed of soil above the ground." {
		t.Errorf("History = %+v, %v", h, err)
	}
	if err := s.SetMeta(Meta{Folder: "Garden"}); err == nil {
		t.Error("SetMeta worked on an incognito session")
	}
	if _, err := Open(dir, s.ID()); err == nil {
		t.Error("Open took an incognito ID")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Errorf("the folder holds %v, %v", entries, err)
	}
	if IsIncognito("2026-09-23T101502-7f3a") || IsIncognito("incognito-../x") {
		t.Error("IsIncognito took a bad ID")
	}
}
