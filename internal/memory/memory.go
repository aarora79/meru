// This file holds the Store: opening the memory directory, and adding,
// listing, reading and forgetting memory files.

package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits. One memory holds one fact, so maxTextBytes (4 KiB, a long
// paragraph) is generous; a longer text belongs in a note you index, not in
// memory. maxFileBytes is larger because you may edit a file by hand; List
// skips a file past it rather than read a runaway file into every prompt.
const (
	maxTextBytes   = 4 << 10  // 4 KiB
	maxFileBytes   = 64 << 10 // 64 KiB
	maxSourceBytes = 256
	maxSlugBytes   = 48
	// maxNameTries bounds the search for a free file name when many
	// memories start with the same words.
	maxNameTries = 1000
)

// dateLayout is how the created field is written: Go writes layouts as the
// reference time Mon Jan 2 15:04:05 2006, so "2006-01-02" means YYYY-MM-DD.
const dateLayout = "2006-01-02"

// kindPattern matches a kind, which is a folder name: letters, digits, "-"
// and "_", starting with a letter or digit, at most 64 bytes. It can't hold a
// path separator or "..", and it is a valid folder name on every platform.
var kindPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Errors callers can check for with errors.Is.
var (
	// ErrNotFound reports a memory ID with no file behind it.
	ErrNotFound = errors.New("memory not found")
	// ErrBadID reports an ID or path that doesn't name a memory file inside
	// the memory directory: "../x", an absolute path elsewhere, a symbolic
	// link, or anything but <kind>/<name>.md.
	ErrBadID = errors.New("not a memory file inside the memory directory")
)

// DefaultKinds returns the kinds ARCHITECTURE.md lists, in that order. Open
// creates a folder for each. It returns a new slice each call, so no caller
// can change the list for another.
func DefaultKinds() []string {
	return []string{"me", "preferences", "projects", "people", "reference", "other"}
}

// Memory is one memory file.
type Memory struct {
	// ID is the file's path relative to the memory directory, with "/" as
	// the separator on every OS, such as
	// "preferences/prefers-short-replies.md". It stays the same until the
	// file moves, so the indexer can key rows on it.
	ID string
	// Kind is the folder the file sits in.
	Kind string
	// Path is the file's absolute path.
	Path string
	// Text is the fact: the file's body with surrounding blank space trimmed.
	Text string
	// Created is the date in the frontmatter, or the zero time when the file
	// has none (a file you wrote by hand, say).
	Created time.Time
	// Source says where the memory came from, such as
	// "session 2026-09-23T101502-7f3a". Empty when unknown.
	Source string
	// Modified is the file's modification time, which changes when you edit
	// it by hand. The indexer compares it to spot edits.
	Modified time.Time
}

// Store is the memory directory. It holds only the directory's path and a
// clock, and every method works on the files directly, so one Store is safe
// to use from many goroutines at once.
type Store struct {
	dir string
	// now returns the current time. Open sets it to time.Now; tests in this
	// package swap in a fixed clock.
	now func() time.Time
}

// Open returns a Store for the memory directory dir (usually
// ~/.meru/memory). It creates dir and a folder for each default kind when
// they are missing, all with mode 0700 so only you can read them. It fails
// when a folder can't be created.
func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("memory directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}
	for _, kind := range DefaultKinds() {
		err := os.Mkdir(filepath.Join(abs, kind), 0o700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create memory folder %s: %w", kind, err)
		}
	}
	return &Store{dir: abs, now: time.Now}, nil
}

// Dir returns the memory directory's absolute path.
func (s *Store) Dir() string { return s.dir }

// openRoot opens an os.Root on the memory directory. An os.Root is a handle
// that refuses any path, including one reached through a symbolic link, that
// would leave the directory, so it backs up the checks in resolve. The caller
// must close it.
func (s *Store) openRoot() (*os.Root, error) {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, fmt.Errorf("open memory directory: %w", err)
	}
	return root, nil
}

// Add saves text as a new memory of the given kind and returns it. source
// records where it came from and may be empty.
//
// The file name is a slug made from the first words of text, such as
// "prefers-short-replies-with-the-answer-in-the.md", with "-2", "-3" and so
// on added when that name is taken. Add creates the kind's folder when it
// is new.
//
// It fails when kind isn't a valid folder name, text is empty, too long or not
// UTF-8, source spans more than one line or is too long, or the file can't be
// written.
func (s *Store) Add(kind, text, source string) (Memory, error) {
	if !kindPattern.MatchString(kind) {
		return Memory{}, fmt.Errorf("kind %q: use letters, digits, - and _ only", kind)
	}
	text = strings.TrimSpace(text)
	source = strings.TrimSpace(source)
	switch {
	case text == "":
		return Memory{}, errors.New("memory text is empty")
	case len(text) > maxTextBytes:
		return Memory{}, fmt.Errorf("memory text is %d bytes, over the %d-byte limit", len(text), maxTextBytes)
	case !utf8.ValidString(text):
		return Memory{}, errors.New("memory text isn't valid UTF-8")
	case strings.ContainsAny(source, "\r\n"):
		return Memory{}, errors.New("memory source must be one line")
	case len(source) > maxSourceBytes:
		return Memory{}, fmt.Errorf("memory source is over the %d-byte limit", maxSourceBytes)
	}

	root, err := s.openRoot()
	if err != nil {
		return Memory{}, err
	}
	// defer runs root.Close() when Add returns, on every path.
	defer root.Close()

	if err := root.Mkdir(kind, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return Memory{}, fmt.Errorf("create memory folder %s: %w", kind, err)
	}
	if err := checkKindDir(root, kind); err != nil {
		return Memory{}, err
	}

	created := s.now()
	content := format(created, source, text)
	slug := slugify(text)
	for n := 1; n <= maxNameTries; n++ {
		name := slug + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.md", slug, n)
		}
		id := path.Join(kind, name)
		// O_EXCL makes the open fail when the file already exists, so two
		// Adds racing for one name can't both win it.
		f, err := root.OpenFile(filepath.FromSlash(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return Memory{}, fmt.Errorf("create memory %s: %w", id, err)
		}
		_, werr := f.WriteString(content)
		// Close can report a failed write, so check it too.
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = root.Remove(filepath.FromSlash(id))
			return Memory{}, fmt.Errorf("write memory %s: %w", id, werr)
		}
		return s.read(root, id)
	}
	return Memory{}, fmt.Errorf("no free file name for %q in %s after %d tries", slug, kind, maxNameTries)
}

// format builds a memory file: the frontmatter, then the text.
func format(created time.Time, source, text string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("created: " + created.Format(dateLayout) + "\n")
	if source != "" {
		b.WriteString("source: " + source + "\n")
	}
	b.WriteString("---\n")
	b.WriteString(text)
	b.WriteString("\n")
	return b.String()
}

// List returns every memory file under the memory directory, sorted by ID.
//
// A problem with one file or folder doesn't stop the listing: List skips it
// and reports it in the returned error, alongside the memories it could read.
// It skips symbolic links, files over the size limit, and folders whose names
// aren't valid kinds. Hidden entries (names starting with ".") and files not
// ending in ".md" are skipped silently. When the directory itself can't be
// read, List returns no memories and that error.
func (s *Store) List() ([]Memory, error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// root.FS() views the memory directory as an fs.FS, the standard
	// read-only file system interface, which fs.ReadDir can list.
	fsys := root.FS()
	kinds, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read memory directory: %w", err)
	}

	var memories []Memory
	var problems []error
	for _, k := range kinds {
		kind := k.Name()
		switch {
		case strings.HasPrefix(kind, "."):
			continue
		case k.Type()&fs.ModeSymlink != 0:
			problems = append(problems, fmt.Errorf("%s: skipped a symbolic link", kind))
			continue
		case !k.IsDir():
			continue
		case !kindPattern.MatchString(kind):
			problems = append(problems, fmt.Errorf("%s: skipped; folder names use letters, digits, - and _", kind))
			continue
		}
		found, errs := s.listKind(root, kind)
		memories = append(memories, found...)
		problems = append(problems, errs...)
	}
	// errors.Join returns nil when problems is empty, and otherwise one
	// error whose text lists them all.
	return memories, errors.Join(problems...)
}

// ListKind returns the memory files of one kind, sorted by ID, and reads no
// other folder. The agent reads the profile kinds on every turn, and
// reading only their folders keeps that quick however many memories the
// other kinds hold. A kind with no folder has no memories.
//
// Like List, it skips a file it can't read and reports it in the returned
// error alongside the memories it could read. It fails with ErrBadID when
// kind isn't a valid folder name or its folder is a symbolic link.
func (s *Store) ListKind(kind string) ([]Memory, error) {
	if !kindPattern.MatchString(kind) {
		return nil, fmt.Errorf("%w: kind %q", ErrBadID, kind)
	}
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkKindDir(root, kind); errors.Is(err, ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	memories, problems := s.listKind(root, kind)
	return memories, errors.Join(problems...)
}

// listKind reads every memory file in the kind folder, which the caller
// has checked, and returns them sorted by name, with one error per file or
// folder it couldn't read. It skips hidden files, files not ending in ".md"
// and folders inside the kind folder.
func (s *Store) listKind(root *os.Root, kind string) ([]Memory, []error) {
	// fs.ReadDir returns the entries sorted by name.
	files, err := fs.ReadDir(root.FS(), kind)
	if err != nil {
		return nil, []error{fmt.Errorf("%s: %w", kind, err)}
	}
	var memories []Memory
	var problems []error
	for _, f := range files {
		name := f.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") || f.IsDir() {
			continue
		}
		m, err := s.read(root, path.Join(kind, name))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		memories = append(memories, m)
	}
	return memories, problems
}

// Get returns the memory with the given ID ("preferences/prefers-short-replies.md") or
// absolute path. It fails with ErrBadID when ref doesn't name a memory file
// inside the directory, and with ErrNotFound when the file doesn't exist.
func (s *Store) Get(ref string) (Memory, error) {
	id, err := s.resolve(ref)
	if err != nil {
		return Memory{}, err
	}
	root, err := s.openRoot()
	if err != nil {
		return Memory{}, err
	}
	defer root.Close()
	if err := checkKindDir(root, path.Dir(id)); err != nil {
		return Memory{}, err
	}
	return s.read(root, id)
}

// Forget deletes the memory with the given ID or absolute path. It deletes
// only a plain file inside the memory directory: it fails with ErrBadID for
// anything else, including a symbolic link, and with ErrNotFound when the
// file doesn't exist.
func (s *Store) Forget(ref string) error {
	id, err := s.resolve(ref)
	if err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkKindDir(root, path.Dir(id)); err != nil {
		return err
	}
	if _, err := statFile(root, id); err != nil {
		return err
	}
	if err := root.Remove(filepath.FromSlash(id)); err != nil {
		return fmt.Errorf("forget %s: %w", id, err)
	}
	return nil
}

// Kinds returns the default kinds plus every other valid folder in the
// memory directory, sorted, with no repeats. It fails when the directory
// can't be read.
func (s *Store) Kinds() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read memory directory: %w", err)
	}
	kinds := DefaultKinds()
	for _, e := range entries {
		// e.Type() describes the entry itself, so a symbolic link to a
		// folder doesn't count as a folder here.
		if e.Type().IsDir() && kindPattern.MatchString(e.Name()) {
			kinds = append(kinds, e.Name())
		}
	}
	slices.Sort(kinds)
	return slices.Compact(kinds), nil
}

// resolve turns a memory ID or an absolute path into a clean ID of the form
// "<kind>/<name>.md". It fails with ErrBadID for anything else: an empty
// string, "..", an absolute path outside the directory, more or fewer than
// two parts, a bad kind, or a name that is hidden or not ".md".
func (s *Store) resolve(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("%w: empty ID", ErrBadID)
	}
	if filepath.IsAbs(ref) {
		rel, err := filepath.Rel(s.dir, filepath.Clean(ref))
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrBadID, ref)
		}
		ref = filepath.ToSlash(rel)
	}
	// filepath.IsLocal rejects "..", absolute paths, empty paths and, on
	// Windows, reserved names such as NUL. path.Clean then removes "./"
	// and doubled slashes.
	if !filepath.IsLocal(filepath.FromSlash(ref)) || strings.Contains(ref, `\`) {
		return "", fmt.Errorf("%w: %s", ErrBadID, ref)
	}
	id := path.Clean(ref)
	kind, name, found := strings.Cut(id, "/")
	if !found || !kindPattern.MatchString(kind) || strings.Contains(name, "/") ||
		strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
		return "", fmt.Errorf("%w: %s", ErrBadID, ref)
	}
	return id, nil
}

// checkKindDir fails with ErrBadID when the kind folder is a symbolic link or
// not a folder, and with ErrNotFound when it doesn't exist.
func checkKindDir(root *os.Root, kind string) error {
	// Lstat reports on the entry itself and doesn't follow a link.
	info, err := root.Lstat(kind)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, kind)
	}
	if err != nil {
		return fmt.Errorf("memory folder %s: %w", kind, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a plain folder", ErrBadID, kind)
	}
	return nil
}

// statFile checks that id names a plain file (not a link, folder or device)
// no larger than maxFileBytes, and returns its details.
func statFile(root *os.Root, id string) (fs.FileInfo, error) {
	info, err := root.Lstat(filepath.FromSlash(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("memory %s: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a plain file", ErrBadID, id)
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s: skipped; over the %d KiB limit", id, maxFileBytes>>10)
	}
	return info, nil
}

// read loads the memory file id through root and parses it.
func (s *Store) read(root *os.Root, id string) (Memory, error) {
	info, err := statFile(root, id)
	if err != nil {
		return Memory{}, err
	}
	data, err := root.ReadFile(filepath.FromSlash(id))
	if err != nil {
		return Memory{}, fmt.Errorf("read memory %s: %w", id, err)
	}
	created, source, text := parse(data)
	return Memory{
		ID:       id,
		Kind:     path.Dir(id),
		Path:     filepath.Join(s.dir, filepath.FromSlash(id)),
		Text:     text,
		Created:  created,
		Source:   source,
		Modified: info.ModTime(),
	}, nil
}
