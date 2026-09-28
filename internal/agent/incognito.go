// This file holds the incognito chats merud keeps open: each one's history
// lives in memory, in a transcript.Session with no file, for as long as
// the chat stays in use. ARCHITECTURE.md, "Incognito chats", says what an
// incognito chat keeps and what it doesn't.

package agent

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/transcript"
)

// incognitoIdle is how long merud keeps an incognito chat after its last
// question. The client says when the user leaves the chat (see
// ForgetIncognito); this limit covers a client that quit without saying,
// so a chat's history doesn't sit in memory until merud stops.
const incognitoIdle = time.Hour

// maxIncognito caps the incognito chats merud keeps at once. A person has
// one or two open; past the cap the one quiet longest goes first.
const maxIncognito = 16

// errIncognitoGone is the error for a question in an incognito chat merud
// no longer holds.
var errIncognitoGone = errors.New("this incognito chat has ended, and Meru kept nothing from it; start a new one")

// incognitoChats holds the open incognito chats by session ID. Its zero
// value is ready to use. Turns in different chats run at once, so a mutex
// guards the map.
type incognitoChats struct {
	mu    sync.Mutex // guards chats
	chats map[string]*incognitoChat
}

// incognitoChat is one open incognito chat and when it was last used.
type incognitoChat struct {
	sess *transcript.Session
	used time.Time
}

// start opens a new incognito chat at now and returns its session.
func (c *incognitoChats) start(now time.Time) *transcript.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	if c.chats == nil {
		c.chats = map[string]*incognitoChat{}
	}
	if len(c.chats) >= maxIncognito {
		c.dropOldest()
	}
	s := transcript.NewIncognito()
	c.chats[s.ID()] = &incognitoChat{sess: s, used: now}
	return s
}

// get returns the incognito chat named id and marks it used at now. ok is
// false when merud doesn't hold it: it was never started, the client left
// it, or it went quiet for incognitoIdle.
func (c *incognitoChats) get(id string, now time.Time) (s *transcript.Session, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	ch, ok := c.chats[id]
	if !ok {
		return nil, false
	}
	ch.used = now
	return ch.sess, true
}

// forget drops the incognito chat named id and reports whether merud held
// it.
func (c *incognitoChats) forget(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.chats[id]
	delete(c.chats, id) // delete on a missing key does nothing
	return ok
}

// prune drops the chats quiet since before now minus incognitoIdle. The
// caller holds c.mu. It runs on every start and get, so no goroutine has
// to watch the clock.
func (c *incognitoChats) prune(now time.Time) {
	for id, ch := range c.chats {
		if now.Sub(ch.used) > incognitoIdle {
			delete(c.chats, id) // Go allows deleting from a map while ranging over it
		}
	}
}

// dropOldest drops the chat used longest ago. The caller holds c.mu.
func (c *incognitoChats) dropOldest() {
	oldest := ""
	for id, ch := range c.chats {
		if oldest == "" || ch.used.Before(c.chats[oldest].used) {
			oldest = id
		}
	}
	delete(c.chats, oldest)
}

// ForgetIncognito drops the incognito chat named id, with the history
// merud held for it, and reports whether there was one. merud calls it for
// the session_delete op, which a client sends when the user leaves an
// incognito chat.
func (a *Agent) ForgetIncognito(id string) bool {
	return a.incognito.forget(id)
}

// withoutRemember returns specs without the remember tool. An incognito
// chat keeps nothing, so the model isn't offered a way to save a memory
// from it; the tool refuses there as well, since a model can call a tool
// it wasn't offered.
func withoutRemember(specs []engine.ToolSpec) []engine.ToolSpec {
	// slices.DeleteFunc changes the slice it gets, so it works on a copy:
	// specs may be the tool list the dispatcher shares with other turns.
	return slices.DeleteFunc(slices.Clone(specs), func(s engine.ToolSpec) bool {
		return s.Name == builtin.Remember
	})
}
