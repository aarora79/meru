// This file is the client half of the protocol: the code meru uses to send a
// request to merud and read the reply.

package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
)

// maxLine is the longest single message we accept, 1 MiB. A token event is
// small; the limit only guards against a broken peer sending endless data.
const maxLine = 1 << 20

// Do sends req to the merud listening on socketPath and returns its reply as
// a sequence of events.
//
// The returned value is an iterator: range over it with
//
//	for ev, err := range rpc.Do(ctx, path, req, approve) { ... }
//
// The sequence ends after a "done" or "error" event, or with a non-nil err if
// the connection fails. Cancelling ctx closes the connection, which tells
// merud to stop the turn.
//
// An "approval" event doesn't reach the loop. Do calls approve with it and
// writes the answer back as a Reply; the turn waits meanwhile. A nil approve
// denies every call, which suits a client with no one to ask. If approve
// fails, Do ends the sequence with its error, which closes the connection
// and stops the turn.
func Do(ctx context.Context, socketPath string, req Request, approve ApproveFunc) iter.Seq2[Event, error] {
	// An iter.Seq2 is a function that calls yield once per item. The caller's
	// loop body runs inside yield; yield returns false when the caller breaks
	// out of the loop, and then we must stop.
	return func(yield func(Event, error) bool) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", socketPath)
		if err != nil {
			yield(Event{}, fmt.Errorf("connect to merud at %s: %w (is merud running?)", socketPath, err))
			return
		}
		// defer runs conn.Close() when this function returns, however it returns.
		defer conn.Close()

		// Close the connection as soon as ctx is cancelled, so a blocked read
		// returns at once. stop() undoes the hook when we finish normally.
		// The error from this Close is dropped on purpose: the connection is being
		// torn down because the caller gave up, and the read loop reports that.
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()

		enc := json.NewEncoder(conn)
		if err := enc.Encode(req); err != nil {
			yield(Event{}, fmt.Errorf("send request: %w", err))
			return
		}

		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
		for sc.Scan() {
			var ev Event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				yield(Event{}, fmt.Errorf("read reply: %w", err))
				return
			}
			if ev.Type == EventApproval && ev.Approval != nil {
				choice := ChoiceDeny
				if approve != nil {
					c, err := approve(ctx, *ev.Approval)
					if err != nil {
						yield(Event{}, fmt.Errorf("approval: %w", err))
						return
					}
					choice = c
				}
				if err := enc.Encode(Reply{ApprovalID: ev.Approval.ID, Choice: choice}); err != nil {
					yield(Event{}, fmt.Errorf("send approval: %w", err))
					return
				}
				continue
			}
			if !yield(ev, nil) {
				return // the caller stopped looping
			}
			if ev.Type == EventDone || ev.Type == EventError {
				return
			}
		}
		// The loop ended without done or error: the connection dropped.
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			yield(Event{}, fmt.Errorf("read reply: %w", err))
			return
		}
		if ctx.Err() != nil {
			yield(Event{}, ctx.Err())
			return
		}
		yield(Event{}, errors.New("merud closed the connection before finishing"))
	}
}
