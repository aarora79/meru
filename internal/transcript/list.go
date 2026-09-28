// This file lists the sessions under the sessions directory, for the
// desktop app's list of past conversations, and reads one session's lines
// back. Both read the files alone: the transcripts are the source of truth,
// so the list stays right after meru.db is deleted.

package transcript

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// titleRunes caps a session's title, in characters. A list of past
// conversations has room for about one line.
const titleRunes = 80

// Info describes one session file, as List returns it.
type Info struct {
	ID string
	// Started is when the session began, read from its ID; Updated is when
	// its file last changed.
	Started time.Time
	Updated time.Time
	// Title is the session's first question on one line, cut to
	// titleRunes characters with "…" at the end.
	Title string
	// Turns counts the questions in the session.
	Turns int
	// Folder and Tags are the chat's folder and tags, from its newest meta
	// line; "" and nil when it has none.
	Folder string
	Tags   []string
}

// List returns the sessions under dir that hold at least one question,
// the most recently changed first, at most limit of them; a limit of zero
// or less means all. A missing dir gives none and no error.
//
// It finds the files by name, so it only reads the ones it returns, plus
// any empty ones it has to skip. It fails when dir can't be walked or a
// file it needs can't be read.
func List(dir string, limit int) ([]Info, error) {
	type found struct {
		id, path string
		mod      time.Time
	}
	var files []found
	// WalkDir visits every file and folder under dir. It never follows a
	// symbolic link, so a link can't point List outside dir.
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		id, ok := strings.CutSuffix(d.Name(), ".jsonl")
		if !ok || !idPattern.MatchString(id) {
			return nil // not a session file
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, found{id: id, path: path, mod: info.ModTime()})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no session yet
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions in %s: %w", dir, err)
	}

	// Newest change first; the ID breaks a tie, so the order is stable.
	slices.SortFunc(files, func(a, b found) int {
		if c := b.mod.Compare(a.mod); c != 0 {
			return c
		}
		return strings.Compare(b.id, a.id)
	})

	var out []Info
	for _, f := range files {
		if limit > 0 && len(out) == limit {
			break
		}
		lines, err := readFile(f.path, f.id)
		if err != nil {
			return nil, err
		}
		if info := infoOf(f.id, f.mod, lines); info.Turns > 0 {
			out = append(out, info)
		}
	}
	return out, nil
}

// Info describes the session as List would: its title, question count,
// times, folder and tags. It fails for an incognito session, which List
// never shows, and when the file can't be read.
func (s *Session) Info() (Info, error) {
	if s.mem != nil {
		return Info{}, errors.New("an incognito chat isn't in the chat list")
	}
	st, err := os.Stat(s.path)
	if err != nil {
		return Info{}, fmt.Errorf("read session %s: %w", s.id, err)
	}
	lines, err := s.read()
	if err != nil {
		return Info{}, err
	}
	return infoOf(s.id, st.ModTime(), lines), nil
}

// infoOf builds the Info of session id from its lines and its file's
// modification time.
func infoOf(id string, mod time.Time, lines []Line) Info {
	meta := MetaOf(lines)
	info := Info{ID: id, Updated: mod, Started: startOf(id), Folder: meta.Folder, Tags: meta.Tags}
	for _, l := range lines {
		if l.Type != TypeUser {
			continue
		}
		if info.Turns == 0 {
			info.Title = title(l.Text)
		}
		info.Turns++
	}
	return info
}

// Lines returns every line of the session, oldest first, skipping lines
// that aren't valid JSON, as History does. It fails when the file can't be
// read.
func (s *Session) Lines() ([]Line, error) {
	return s.read()
}

// startOf reads the start time out of a session ID such as
// 2026-09-23T101502-7f3a. The ID holds UTC. It returns the zero time for an
// ID that doesn't parse, which List's pattern check rules out.
func startOf(id string) time.Time {
	t, err := time.Parse("2006-01-02T150405", id[:len("2006-01-02T150405")])
	if err != nil {
		return time.Time{}
	}
	return t
}

// title turns a question into a one-line title: runs of spaces and new
// lines become one space, and a long question is cut to titleRunes
// characters, ending in "…".
func title(q string) string {
	t := strings.Join(strings.Fields(q), " ")
	if utf8.RuneCountInString(t) <= titleRunes {
		return t
	}
	// A []rune holds one entry per character, so cutting it never splits
	// a character that takes more than one byte, such as "é".
	return strings.TrimSpace(string([]rune(t)[:titleRunes-1])) + "…"
}
