// This file holds what the user sets on a chat, apart from the chat itself:
// its folder and tags, kept in "meta" lines, and deleting the chat's file.
// ARCHITECTURE.md, "Organizing past chats", says how the clients use them.

package transcript

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits on what the user can set. A folder name or a tag shows on one
// line of the chat list, and a chat with more tags than fit there is hard
// to read.
const (
	maxFolderRunes = 60
	maxTagRunes    = 32
	// MaxTags caps the tags on one chat.
	MaxTags = 12
)

// Meta is a chat's folder and tags, as its newest meta line holds them.
// The zero Meta means no folder and no tags.
type Meta struct {
	Folder string
	Tags   []string
}

// MetaOf returns the Meta of the newest meta line in lines, or the zero
// Meta when there is none. Each meta line holds the whole state, so the
// older ones don't matter.
func MetaOf(lines []Line) Meta {
	var m Meta
	for _, l := range lines {
		if l.Type == TypeMeta {
			m = Meta{Folder: l.Folder, Tags: l.Tags}
		}
	}
	return m
}

// Meta returns the session's folder and tags. It fails only when the file
// can't be read.
func (s *Session) Meta() (Meta, error) {
	lines, err := s.read()
	if err != nil {
		return Meta{}, err
	}
	return MetaOf(lines), nil
}

// SetMeta appends a meta line that holds m, the chat's whole new state.
//
// It then puts the file's modification time back as it was. The chat list
// orders chats by that time (see List), and moving or tagging a chat isn't
// talking in it: without this, a chat moved into a folder would jump to
// Today. The line keeps its own time, so the transcript still says when
// the move happened.
//
// It fails for an incognito session, which keeps no folder or tags, and
// when the file can't be read or written.
func (s *Session) SetMeta(m Meta) error {
	if s.mem != nil {
		return errors.New("an incognito chat has no folder or tags")
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("set folder and tags of %s: %w", s.id, err)
	}
	if err := s.Append(Line{Type: TypeMeta, Folder: m.Folder, Tags: m.Tags}); err != nil {
		return err
	}
	// A zero time.Time leaves the access time as it is.
	if err := os.Chtimes(s.path, time.Time{}, info.ModTime()); err != nil {
		return fmt.Errorf("keep the time of %s: %w", s.id, err)
	}
	return nil
}

// CleanFolder returns a folder name ready to store: runs of spaces become
// one space, and the ends lose theirs. It fails for a name that is empty,
// longer than 60 characters, or holds a control character.
func CleanFolder(name string) (string, error) {
	// strings.Fields splits on any run of spaces, tabs and new lines, so
	// the join leaves one space between words and none at the ends.
	n := strings.Join(strings.Fields(name), " ")
	switch {
	case n == "":
		return "", errors.New("a folder needs a name")
	case utf8.RuneCountInString(n) > maxFolderRunes:
		return "", fmt.Errorf("a folder name has at most %d characters", maxFolderRunes)
	case strings.IndexFunc(n, unicode.IsControl) >= 0:
		return "", errors.New("a folder name can't hold control characters")
	}
	return n, nil
}

// CleanTag returns a tag ready to store: lower case, without a leading
// "#" or spaces at the ends. A tag is one word of letters, digits, "-"
// and "_", so a search for it matches it and nothing else. CleanTag fails
// for a tag that is empty, longer than 32 characters, or holds anything
// else.
func CleanTag(tag string) (string, error) {
	t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
	switch {
	case t == "":
		return "", errors.New("a tag can't be empty")
	case utf8.RuneCountInString(t) > maxTagRunes:
		return "", fmt.Errorf("a tag has at most %d characters", maxTagRunes)
	}
	for _, r := range t {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return "", fmt.Errorf("a tag is one word of letters, digits, - and _; %q isn't", tag)
		}
	}
	return t, nil
}

// WithTags returns tags with add appended and remove taken out, each one
// cleaned by CleanTag, in the order they came, with no tag twice. It fails
// when a tag doesn't pass CleanTag or the result holds more than MaxTags.
func WithTags(tags, add, remove []string) ([]string, error) {
	out := slices.Clone(tags)
	for _, a := range add {
		t, err := CleanTag(a)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	for _, r := range remove {
		t, err := CleanTag(r)
		if err != nil {
			return nil, err
		}
		// slices.DeleteFunc drops every element the function returns true for.
		out = slices.DeleteFunc(out, func(x string) bool { return x == t })
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("a chat has at most %d tags", MaxTags)
	}
	return out, nil
}

// Delete removes the file of the session id names under dir, for good. It
// takes an ID, never a path: the ID must match the pattern of a session
// ID, so it can't name a file outside dir, and the file must be a regular
// file, never a symbolic link. It fails when the ID isn't valid, the file
// doesn't exist or isn't a regular file, or the OS refuses.
func Delete(dir, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("session ID %q isn't valid", id)
	}
	path := sessionPath(dir, id)
	// Lstat, unlike Stat, looks at a symbolic link itself, not at the file
	// it points to.
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("delete session %s: not a regular file", id)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}
