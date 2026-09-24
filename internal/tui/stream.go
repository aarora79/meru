// This file holds the code that talks to merud in the background: the command
// that runs one turn and feeds each reply event back into the Bubble Tea
// loop, and the command that pings merud when the chat opens.

package tui

import (
	"context"
	"errors"
	"iter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// askFunc sends one request to merud and returns the reply events. approve
// answers merud's approval questions; nil denies them all. In the real
// program askFunc wraps rpc.Do with the socket path; tests pass a fake that
// returns scripted events, so they never open a socket.
//
// iter.Seq2[rpc.Event, error] is Go's type for "something you can range over
// that yields two values per step", here an event and an error.
type askFunc func(ctx context.Context, req rpc.Request, approve rpc.ApproveFunc) iter.Seq2[rpc.Event, error]

// sender delivers a message into a running Bubble Tea program. *tea.Program
// has a Send method with this shape, so it satisfies the interface without
// saying so; Go matches interfaces by method set, not by declaration. Tests
// pass a fake that records the messages instead.
type sender interface {
	Send(msg tea.Msg)
}

// eventMsg carries one event from merud into Update. turn names the question
// it belongs to, so Update can drop late events from a turn the user
// cancelled.
type eventMsg struct {
	turn int
	ev   rpc.Event
}

// turnDoneMsg tells Update that a turn's stream has ended. err is nil when
// merud finished with "done" or "error"; otherwise it says why the connection
// failed, and is context.Canceled when the user pressed Ctrl-C.
type turnDoneMsg struct {
	turn int
	err  error
}

// streamCmd returns a Bubble Tea command that runs one turn.
//
// A tea.Cmd is a function Bubble Tea calls in its own goroutine (a function
// running at the same time as the rest of the program), then feeds the
// function's return value to Update as a message. We use that goroutine to
// read the whole stream: each event goes in through send.Send as it arrives,
// and the final turnDoneMsg goes in as the command's return value. Both paths
// end up on the same queue, in order, so turnDoneMsg always comes last.
func streamCmd(ctx context.Context, ask askFunc, send sender, turn int, req rpc.Request) tea.Cmd {
	return func() tea.Msg {
		// range over an iterator runs the loop body once per yielded pair.
		for ev, err := range ask(ctx, req, approveVia(send, turn)) {
			if err != nil {
				return turnDoneMsg{turn: turn, err: err}
			}
			send.Send(eventMsg{turn: turn, ev: ev})
		}
		return turnDoneMsg{turn: turn}
	}
}

// approvalRequestMsg asks Update to show an approval box for one tool call.
// reply takes the user's choice back to the goroutine that waits for it.
// turn names the question it belongs to, like eventMsg's.
type approvalRequestMsg struct {
	turn     int
	approval rpc.Approval
	// reply has room for one choice, so Update can send the answer without
	// waiting, even when the goroutine has already given up.
	reply chan rpc.Choice
}

// approveVia returns the ApproveFunc for one turn. rpc.Do calls it from the
// stream goroutine each time merud asks about a tool call.
//
// The goroutine can't draw anything or read keys; only Update can. So the
// function hands the question to Update as an approvalRequestMsg, through
// send like any event, and then blocks until one of two things happens:
// Update puts the user's choice on the reply channel, or ctx ends because
// the user pressed Ctrl-C or quit. select waits for whichever comes first.
// While it blocks, the turn waits too: merud holds the tool call until the
// Reply arrives.
func approveVia(send sender, turn int) rpc.ApproveFunc {
	return func(ctx context.Context, a rpc.Approval) (rpc.Choice, error) {
		reply := make(chan rpc.Choice, 1)
		send.Send(approvalRequestMsg{turn: turn, approval: a, reply: reply})
		select {
		case c := <-reply:
			return c, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// pingTimeout bounds how long a status check waits for merud.
const pingTimeout = 2 * time.Second

// pingMsg reports a status check's result: err is nil when merud answered,
// and index holds what the search index held then. index is nil when merud
// answered without it.
type pingMsg struct {
	err   error
	index *rpc.IndexStatus
}

// pingCmd returns a command that asks merud once what its index holds. The
// answer says two things for the header: that merud is up, and how many
// documents it can search. The chat runs it at start and after each
// answer, because the watcher indexes new files while merud runs.
func pingCmd(ask askFunc) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		// defer runs cancel when this function returns, freeing the timer.
		defer cancel()
		var index *rpc.IndexStatus
		for ev, err := range ask(ctx, rpc.Request{Op: rpc.OpIndexStatus}, nil) {
			if err != nil {
				return pingMsg{err: err}
			}
			switch ev.Type {
			case rpc.EventStatus:
				index = ev.Status
			case rpc.EventDone:
				return pingMsg{index: index}
			case rpc.EventError:
				return pingMsg{err: errors.New(ev.Error)}
			}
		}
		return pingMsg{err: errors.New("merud sent no reply")}
	}
}
