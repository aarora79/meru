// This file holds the code that talks to merud in the background: the command
// that runs one turn and feeds each reply event back into the Bubble Tea
// loop, and the commands that ask merud for its index status and its usage
// numbers for the header.

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

// How often the chat asks merud for its status: often while a scan runs
// (or before merud has answered at all), so the document count climbs as
// files land, and rarely otherwise, when only the watcher adds files. Each
// check is one count query on meru.db.
const (
	refreshScanning = 5 * time.Second
	refreshIdle     = 30 * time.Second
)

// nextRefresh returns how long to wait before the next status check, given
// what the last one said: refreshScanning while a scan runs or before any
// answer, refreshIdle otherwise.
func nextRefresh(ix *rpc.IndexStatus) time.Duration {
	if ix == nil || ix.Scanning {
		return refreshScanning
	}
	return refreshIdle
}

// refreshMsg tells the model it is time for the next status check.
type refreshMsg struct{}

// refreshAfter returns a command that sends a refreshMsg after d.
// tea.Tick runs the timer in Bubble Tea's own goroutine, so nothing here
// needs stopping when the chat quits.
func refreshAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return refreshMsg{} })
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

// usageMsg reports what merud said to OpUsage. answered is true when merud
// replied at all, even with an error event, and err says why no windows
// came. An older merud answers OpUsage with an "unknown op" error event,
// which gives answered true and an err. byModel is true for the reply to
// `/usage by model`, whose windows hold one model each.
type usageMsg struct {
	windows  []rpc.UsageWindow
	answered bool
	err      error
	byModel  bool
}

// usageCmd returns a command that asks merud once how much Meru has been
// used, in every window. The chat runs it next to pingCmd, on the same
// timer and after each answer, so the header's last-hour numbers stay
// fresh, and again when the user types /usage.
func usageCmd(ask askFunc) tea.Cmd {
	return askUsage(ask, false)
}

// askUsage returns a command that sends OpUsage once, asking for one
// window per answer model when byModel is true.
func askUsage(ask askFunc, byModel bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		defer cancel()
		req := rpc.Request{Op: rpc.OpUsage}
		if byModel {
			req.Kind = rpc.UsageByModel
		}
		var windows []rpc.UsageWindow
		for ev, err := range ask(ctx, req, nil) {
			if err != nil {
				return usageMsg{err: err, byModel: byModel}
			}
			switch ev.Type {
			case rpc.EventUsage:
				windows = ev.Usage
			case rpc.EventDone:
				return usageMsg{windows: windows, answered: true, byModel: byModel}
			case rpc.EventError:
				return usageMsg{answered: true, err: errors.New(ev.Error), byModel: byModel}
			}
		}
		return usageMsg{err: errors.New("merud sent no reply"), byModel: byModel}
	}
}
