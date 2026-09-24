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

// pingTimeout bounds how long the opening ping waits for merud.
const pingTimeout = 2 * time.Second

// pingMsg reports the opening ping's result: err is nil when merud answered.
type pingMsg struct {
	err error
}

// pingCmd returns a command that pings merud once, so the header can say
// whether it is up before the user asks anything.
func pingCmd(ask askFunc) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		// defer runs cancel when this function returns, freeing the timer.
		defer cancel()
		for ev, err := range ask(ctx, rpc.Request{Op: rpc.OpPing}) {
			if err != nil {
				return pingMsg{err: err}
			}
			if ev.Type == rpc.EventDone {
				return pingMsg{}
			}
		}
		return pingMsg{err: errors.New("merud sent no reply to ping")}
	}
}
