// This file is the DevTools Protocol client: how merud sends Chrome a
// command and gets its reply. It has two layers, in this order:
//
//  1. Framing. Chrome, started with --remote-debugging-pipe, reads JSON
//     messages from its file descriptor 3 and writes JSON messages to its
//     file descriptor 4, each ending in a NUL byte (0x00).
//  2. Replies. Each command carries a number, its id; Chrome's reply
//     carries the same id. A map of pending commands hands each reply to
//     the call waiting for it. Messages with no id are events, such as
//     "the page loaded"; the renderer polls the page instead of waiting for
//     events, so the client drops them.

package render

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxMessage caps one message from Chrome. The largest is the page's HTML,
// which the renderer refuses past 5 MiB anyway; JSON escaping can double
// it, so 32 MiB leaves room and still stops a runaway page.
const maxMessage = 32 << 20

// errPipeClosed means Chrome's end of the pipes closed: it exited or
// crashed. Every call waiting at that moment fails with it.
var errPipeClosed = errors.New("the browser closed its DevTools pipe")

// conn is one DevTools connection over a pair of pipes.
type conn struct {
	// w is Chrome's fd 3. wmu makes each message go out whole, since
	// several goroutines could call at once.
	w   io.Writer
	wmu sync.Mutex

	// mu guards the fields below it.
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan reply
	// readErr is why the reader stopped; done closes when it has.
	readErr error
	done    chan struct{}
}

// reply is Chrome's answer to one command: its result, or the error
// Chrome sent instead.
type reply struct {
	result json.RawMessage
	err    error
}

// outgoing is one command. The `json:"..."` struct tags name the JSON
// keys; omitempty leaves out an empty sessionId, which a command to the
// browser itself has none of.
type outgoing struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// incoming is one message from Chrome: a reply (ID set) or an event.
type incoming struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// newConn returns a connection that writes commands to w and reads
// Chrome's messages from r. It starts one goroutine that reads r until r
// closes; done closes when it has stopped, so the owner can wait for it.
func newConn(w io.Writer, r io.Reader) *conn {
	c := &conn{w: w, pending: map[int64]chan reply{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

// read is the reader goroutine. It splits r at NUL bytes, hands each
// reply to the call waiting for its id, and drops events. When r ends, it
// fails every waiting call and closes done.
func (c *conn) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var err error
	for {
		var msg []byte
		msg, err = readMessage(br)
		if err != nil {
			break
		}
		var in incoming
		if json.Unmarshal(msg, &in) != nil || in.ID == 0 {
			continue // an event, or a message we can't read: nothing waits for it
		}
		rep := reply{result: in.Result}
		if in.Error != nil {
			rep.err = fmt.Errorf("the browser said %s (%d)", in.Error.Message, in.Error.Code)
		}
		c.mu.Lock()
		ch, ok := c.pending[in.ID]
		delete(c.pending, in.ID)
		c.mu.Unlock()
		if ok {
			ch <- rep // buffered, so this never blocks
		}
	}
	if errors.Is(err, io.EOF) {
		err = errPipeClosed
	}
	c.mu.Lock()
	c.readErr = err
	for id, ch := range c.pending {
		ch <- reply{err: err}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// readMessage reads one NUL-terminated message, without the NUL. It
// fails at the end of the stream and on a message over maxMessage.
func readMessage(br *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for {
		// ReadSlice returns up to and including the NUL, or a full buffer
		// (bufio.ErrBufferFull) when the message is longer than it.
		part, err := br.ReadSlice(0)
		buf.Write(part)
		if buf.Len() > maxMessage {
			return nil, fmt.Errorf("a DevTools message is larger than %d MiB", maxMessage>>20)
		}
		switch {
		case err == nil:
			return buf.Bytes()[:buf.Len()-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// call sends method with params to the browser (session "") or to the
// page attached as session, and waits for the reply, which it decodes
// into result when result isn't nil. It fails when Chrome answers with an
// error, the pipe closes, or ctx ends.
func (c *conn) call(ctx context.Context, session, method string, params, result any) error {
	// A buffer of one lets the reader hand over the reply even when this
	// call has given up and gone.
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	msg, err := json.Marshal(outgoing{ID: id, Method: method, Params: params, SessionID: session})
	if err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}
	c.wmu.Lock()
	_, err = c.w.Write(append(msg, 0))
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}

	// select waits for whichever comes first: the reply, or the end of
	// ctx.
	select {
	case rep := <-ch:
		if rep.err != nil {
			return fmt.Errorf("%s: %w", method, rep.err)
		}
		if result != nil {
			if err := json.Unmarshal(rep.result, result); err != nil {
				return fmt.Errorf("%s: read the reply: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// forget drops a pending call, so the map doesn't keep a call that gave
// up.
func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
