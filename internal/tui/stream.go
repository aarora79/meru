// This file holds the code that runs one turn in the background: it sends the
// question to merud and feeds each reply event back into the Bubble Tea loop.

package tui

import (
	"context"
	"iter"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// askFunc sends one request to merud and returns the reply events. In the real
// program it wraps rpc.Do with the socket path; tests pass a fake that returns
// scripted events, so they never open a socket.
//
// iter.Seq2[rpc.Event, error] is Go's type for "something you can range over
// that yields two values per step", here an event and an error.
type askFunc func(ctx context.Context, req rpc.Request) iter.Seq2[rpc.Event, error]

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
		for ev, err := range ask(ctx, req) {
			if err != nil {
				return turnDoneMsg{turn: turn, err: err}
			}
			send.Send(eventMsg{turn: turn, ev: ev})
		}
		return turnDoneMsg{turn: turn}
	}
}
