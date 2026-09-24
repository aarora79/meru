// This file tests the memory Store: adding, listing, reading and forgetting
// memory files, the file names Add picks, and the paths it must refuse.

package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// openTest opens a Store in a fresh temporary directory with a fixed clock,
// so created dates are predictable.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 9, 23, 10, 15, 2, 0, time.UTC) }
	return s
}

// TestOpenCreatesKinds checks that Open makes the default folders, private
// to the user, and that Kinds lists them plus a folder you add.
func TestOpenCreatesKinds(t *testing.T) {
	s := openTest(t)
	for _, kind := range DefaultKinds() {
		checkMode(t, filepath.Join(s.Dir(), kind), 0o700)
	}
	checkMode(t, s.Dir(), 0o700)

	if err := os.Mkdir(filepath.Join(s.Dir(), "health"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Dir(), "bad name"), 0o700); err != nil {
		t.Fatal(err)
	}
	kinds, err := s.Kinds()
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	want := []string{"health", "me", "other", "people", "preferences", "projects", "reference"}
	if !slices.Equal(kinds, want) {
		t.Errorf("Kinds = %v, want %v", kinds, want)
	}
}

// TestAddGetForget walks one memory through its life: add, read back with the
// frontmatter intact, find in List, forget.
func TestAddGetForget(t *testing.T) {
	s := openTest(t)
	m, err := s.Add("preferences", "  Prefers index funds over individual stocks for retirement accounts.\n", "session 2026-09-23T101502-7f3a")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if m.ID != "preferences/prefers-index-funds-over-individual-stocks-for.md" {
		t.Errorf("ID = %q", m.ID)
	}
	if m.Kind != "preferences" || m.Path != filepath.Join(s.Dir(), "preferences", "prefers-index-funds-over-individual-stocks-for.md") {
		t.Errorf("Kind, Path = %q, %q", m.Kind, m.Path)
	}
	checkMode(t, m.Path, 0o600)

	// The file on disk has exactly the shape ARCHITECTURE.md shows.
	data, err := os.ReadFile(m.Path) // #nosec G304 -- a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "---\ncreated: 2026-09-23\nsource: session 2026-09-23T101502-7f3a\n---\nPrefers index funds over individual stocks for retirement accounts.\n"
	if string(data) != wantFile {
		t.Errorf("file =\n%s\nwant\n%s", data, wantFile)
	}

	// Get works with the ID and with the absolute path, and round-trips
	// the frontmatter.
	for _, ref := range []string{m.ID, m.Path} {
		got, err := s.Get(ref)
		if err != nil {
			t.Fatalf("Get(%q): %v", ref, err)
		}
		if got.Text != "Prefers index funds over individual stocks for retirement accounts." ||
			got.Source != "session 2026-09-23T101502-7f3a" ||
			!got.Created.Equal(time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)) ||
			got.Modified.IsZero() {
			t.Errorf("Get(%q) = %+v", ref, got)
		}
	}

	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}

	if err := s.Forget(m.ID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Forget = %v, want ErrNotFound", err)
	}
	if err := s.Forget(m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Forget = %v, want ErrNotFound", err)
	}
}

// TestAddRejects checks the inputs Add must turn down.
func TestAddRejects(t *testing.T) {
	s := openTest(t)
	tests := []struct {
		name, kind, text, source string
	}{
		{"empty text", "me", "   \n", ""},
		{"too long", "me", strings.Repeat("a", maxTextBytes+1), ""},
		{"bad UTF-8", "me", "caf\xe9", ""},
		{"source on two lines", "me", "x", "a\nsource: forged"},
		{"long source", "me", "x", strings.Repeat("s", maxSourceBytes+1)},
		{"kind with dots", "..", "x", ""},
		{"kind with slash", "me/../../x", "x", ""},
		{"empty kind", "", "x", ""},
		{"hidden kind", ".secret", "x", ""},
		{"kind with space", "my notes", "x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if m, err := s.Add(tt.kind, tt.text, tt.source); err == nil {
				t.Errorf("Add accepted it and wrote %s", m.Path)
			}
		})
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("rejected Adds left %d files", len(list))
	}
}

// TestSlugify checks the file names Add derives from text.
func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Prefers index funds.", "prefers-index-funds"},
		{"  Lives in Seattle, WA (since 2019)!  ", "lives-in-seattle-wa-since-2019"},
		{"../../etc/passwd", "etc-passwd"},
		{`C:\Windows\system32`, "c-windows-system32"},
		{"नमस्ते दुनिया", "memory"},
		{"Café crème", "caf-cr-me"},
		{"con", "memory-con"},
		{"LPT1", "memory-lpt1"},
		{"one two three four five six seven eight nine ten eleven", "one-two-three-four-five-six-seven-eight-nine-ten"},
		{strings.Repeat("x", 100), strings.Repeat("x", maxSlugBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := slugify(tt.in)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len(got) > maxSlugBytes+len("memory-") {
				t.Errorf("slug %q is too long", got)
			}
		})
	}
}

// TestSlugUnique checks that memories with the same opening words get
// distinct files, including when many Adds run at once.
func TestSlugUnique(t *testing.T) {
	s := openTest(t)
	first, err := s.Add("people", "Sam is my sister.", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Add("people", "Sam is my sister!", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "people/sam-is-my-sister.md" || second.ID != "people/sam-is-my-sister-2.md" {
		t.Errorf("IDs = %q, %q", first.ID, second.ID)
	}

	// Twenty goroutines add the same text at once. O_EXCL must give each a
	// file of its own. sync.WaitGroup waits for all of them to finish.
	const n = 20
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			m, err := s.Add("other", "Same words every time.", fmt.Sprintf("test %d", i))
			ids[i], errs[i] = m.ID, err
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	if len(slices.Compact(ids)) != n {
		t.Errorf("concurrent Adds shared a file: %v", ids)
	}
}

// TestCustomKind checks that a folder you make by hand is a kind like any
// other, and that Add creates a new kind's folder.
func TestCustomKind(t *testing.T) {
	s := openTest(t)
	if err := os.Mkdir(filepath.Join(s.Dir(), "health"), 0o700); err != nil {
		t.Fatal(err)
	}
	hand := filepath.Join(s.Dir(), "health", "Allergy Notes.md")
	if err := os.WriteFile(hand, []byte("Allergic to penicillin.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("travel", "Prefers aisle seats.", ""); err != nil {
		t.Fatalf("Add to a new kind: %v", err)
	}
	checkMode(t, filepath.Join(s.Dir(), "travel"), 0o700)

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	want := []string{"health/Allergy Notes.md", "travel/prefers-aisle-seats.md"}
	if !slices.Equal(ids, want) {
		t.Errorf("IDs = %v, want %v", ids, want)
	}
	// The hand-written file has no frontmatter: all of it is text.
	if list[0].Text != "Allergic to penicillin." || !list[0].Created.IsZero() || list[0].Kind != "health" {
		t.Errorf("hand-written memory = %+v", list[0])
	}
	if err := s.Forget("health/Allergy Notes.md"); err != nil {
		t.Errorf("Forget a hand-written file: %v", err)
	}
}

// TestParse checks how parse reads hand-edited files.
func TestParse(t *testing.T) {
	day := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		in          string
		wantCreated time.Time
		wantSource  string
		wantText    string
	}{
		{"written by Add", "---\ncreated: 2026-09-23\nsource: session x\n---\nFact.\n", day, "session x", "Fact."},
		{"no frontmatter", "Just a fact.\n", time.Time{}, "", "Just a fact."},
		{"never closes", "---\ncreated: 2026-09-23\nFact.", time.Time{}, "", "---\ncreated: 2026-09-23\nFact."},
		{"empty frontmatter", "---\n---\nFact.", time.Time{}, "", "Fact."},
		{"bad date and extra key", "---\ncreated: yesterday\nmood: good\n---\nFact.", time.Time{}, "", "Fact."},
		{"timestamp date", "---\ncreated: 2026-09-23T00:00:00Z\n---\nFact.", day, "", "Fact."},
		{"windows endings", "---\r\ncreated: 2026-09-23\r\n---\r\nFact.\r\n", day, "", "Fact."},
		{"dashes in the text", "---\ncreated: 2026-09-23\n---\nA\n---\nB\n", day, "", "A\n---\nB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, source, text := parse([]byte(tt.in))
			if !created.Equal(tt.wantCreated) || source != tt.wantSource || text != tt.wantText {
				t.Errorf("parse = %v, %q, %q; want %v, %q, %q", created, source, text, tt.wantCreated, tt.wantSource, tt.wantText)
			}
		})
	}
}

// TestRefusesTraversal checks that Get and Forget refuse every reference that
// isn't a memory file inside the directory, and that nothing outside it is
// touched.
func TestRefusesTraversal(t *testing.T) {
	s := openTest(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outside, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file next to the memory directory, reachable with "../".
	sibling := filepath.Join(filepath.Dir(s.Dir()), "sibling.md")
	if err := os.WriteFile(sibling, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("me", "Real memory.", ""); err != nil {
		t.Fatal(err)
	}

	refs := []string{
		"",
		"../sibling.md",
		"me/../../sibling.md",
		"me/../../../" + filepath.ToSlash(outside),
		outside,
		filepath.Join(s.Dir(), "..", "sibling.md"),
		"me",
		"me/",
		"real-memory.md",
		"me/sub/real-memory.md",
		"me/.hidden.md",
		"me/real-memory.txt",
		`me\real-memory.md`,
		"/etc/passwd",
	}
	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			if _, err := s.Get(ref); !errors.Is(err, ErrBadID) {
				t.Errorf("Get(%q) = %v, want ErrBadID", ref, err)
			}
			if err := s.Forget(ref); !errors.Is(err, ErrBadID) {
				t.Errorf("Forget(%q) = %v, want ErrBadID", ref, err)
			}
		})
	}
	for _, p := range []string{outside, sibling} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}
}

// TestRefusesSymlinks checks that a symbolic link, whether it stands in for a
// kind folder or a memory file, is never read, written through or deleted.
func TestRefusesSymlinks(t *testing.T) {
	s := openTest(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A kind folder that is a link to a folder outside.
	if err := os.Symlink(outsideDir, filepath.Join(s.Dir(), "linked")); err != nil {
		t.Skipf("can't make symbolic links here: %v", err)
	}
	// A memory file that is a link to a file outside.
	if err := os.Symlink(outside, filepath.Join(s.Dir(), "me", "link.md")); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"linked/secret.md", "me/link.md"} {
		if m, err := s.Get(ref); !errors.Is(err, ErrBadID) {
			t.Errorf("Get(%q) = %q, %v; want ErrBadID", ref, m.Text, err)
		}
		if err := s.Forget(ref); !errors.Is(err, ErrBadID) {
			t.Errorf("Forget(%q) = %v, want ErrBadID", ref, err)
		}
	}
	if _, err := s.Add("linked", "Should not land outside.", ""); err == nil {
		t.Error("Add wrote through a linked kind folder")
	}

	list, err := s.List()
	if len(list) != 0 {
		t.Errorf("List returned linked files: %+v", list)
	}
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("List error = %v, want it to name the skipped links", err)
	}
	kinds, _ := s.Kinds()
	if slices.Contains(kinds, "linked") {
		t.Error("Kinds counted a linked folder")
	}

	entries, _ := os.ReadDir(outsideDir)
	if len(entries) != 1 {
		t.Errorf("the outside folder now holds %d files, want 1", len(entries))
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file is gone: %v", err)
	}
}

// TestListSkipsBadFiles checks that one oversized file is reported without
// hiding the rest, and that non-Markdown and hidden files are ignored.
func TestListSkipsBadFiles(t *testing.T) {
	s := openTest(t)
	if _, err := s.Add("me", "Good one.", ""); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(s.Dir(), "other", "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", maxFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", ".DS_Store", ".draft.md"} {
		if err := os.WriteFile(filepath.Join(s.Dir(), "other", name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if len(list) != 1 || list[0].ID != "me/good-one.md" {
		t.Errorf("List = %+v", list)
	}
	if err == nil || !strings.Contains(err.Error(), "big.md") {
		t.Errorf("List error = %v, want it to name big.md", err)
	}
}

// TestListKind checks that ListKind reads one folder only, reports a bad
// file there without hiding the rest, and refuses a kind that isn't a
// plain folder name.
func TestListKind(t *testing.T) {
	s := openTest(t)
	for _, m := range []struct{ kind, text string }{
		{"me", "Name is Amit."}, {"me", "Lives in Washington."}, {"projects", "Builds Meru."},
	} {
		if _, err := s.Add(m.kind, m.text, ""); err != nil {
			t.Fatal(err)
		}
	}
	big := filepath.Join(s.Dir(), "preferences", "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", maxFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		kind    string
		wantIDs []string
		wantErr string // "" for none
	}{
		{"me", []string{"me/lives-in-washington.md", "me/name-is-amit.md"}, ""},
		{"projects", []string{"projects/builds-meru.md"}, ""},
		{"preferences", nil, "big.md"},
		{"travel", nil, ""}, // no folder, no memories
		{"../me", nil, ErrBadID.Error()},
		{"", nil, ErrBadID.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			list, err := s.ListKind(tt.kind)
			var ids []string
			for _, m := range list {
				ids = append(ids, m.ID)
			}
			if !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("IDs = %v, want %v", ids, tt.wantIDs)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// checkMode fails the test when path's permission bits aren't want. Windows
// has no Unix permission bits, so the check is skipped there.
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}
