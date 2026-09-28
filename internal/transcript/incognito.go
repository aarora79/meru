// This file holds incognito sessions: a Session whose lines live in memory
// and never reach a file. merud keeps one for as long as the chat stays
// open; when it lets go of it, nothing about the chat is left on disk. See
// ARCHITECTURE.md, "Incognito chats".

package transcript

import (
	"regexp"
	"slices"
	"sync"
)

// IncognitoPrefix starts every incognito session's ID, as in
// incognito-7f3a09bc. The ID fails idPattern, so Open, Delete and every
// replay refuse it: no code that works on files can take one.
const IncognitoPrefix = "incognito-"

// incognitoPattern matches an incognito session's ID: the prefix and eight
// random hex digits.
var incognitoPattern = regexp.MustCompile(`^incognito-[0-9a-f]{8}$`)

// memLines holds an incognito session's lines. Several goroutines of one
// turn append at once (the tool calls of a round run side by side), so a
// mutex guards the slice.
type memLines struct {
	mu    sync.Mutex // guards lines
	lines []Line
}

// add appends l.
func (m *memLines) add(l Line) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines = append(m.lines, l)
}

// all returns a copy of the lines, so the caller can read them while
// another goroutine appends.
func (m *memLines) all() []Line {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.lines)
}

// NewIncognito returns a new incognito session: one with an ID of its own
// and no file. Append keeps each line in memory, and History, Model and
// Lines read them back, so a turn works in it as in any session. Nothing
// reaches the disk, and the lines go when the last pointer to the session
// does.
func NewIncognito() *Session {
	return &Session{id: IncognitoPrefix + randomHex(4), mem: &memLines{}}
}

// IsIncognito reports whether id names an incognito session.
func IsIncognito(id string) bool {
	return incognitoPattern.MatchString(id)
}

// Incognito reports whether s is an incognito session, one with no file.
func (s *Session) Incognito() bool {
	return s.mem != nil
}
