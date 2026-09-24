// This file starts the chat program: it wires the model to the real socket
// and to the running Bubble Tea program, then hands the terminal to Bubble Tea
// until the user quits.

package tui

import (
	"context"
	"fmt"
	"iter"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// Run opens the chat screen and talks to the merud listening on socket. It
// returns when the user quits (nil) or when ctx is cancelled or the terminal
// fails (an error).
func Run(ctx context.Context, socket string) error {
	ask := func(ctx context.Context, req rpc.Request) iter.Seq2[rpc.Event, error] {
		return rpc.Do(ctx, socket, req)
	}

	// The model needs a way to Send into the program, but the program is
	// built from the model. relay breaks the loop: the model gets relay now,
	// and relay gets the program one line later.
	relay := &programRelay{}
	p := tea.NewProgram(newModel(ask, relay), tea.WithAltScreen(), tea.WithContext(ctx))
	relay.p = p

	final, err := p.Run()
	// A turn may still be streaming if the program stopped for some other
	// reason than a quit key. Cancel it so its goroutine closes the socket.
	// final.(Model) is a type assertion: it checks that the interface value
	// holds a Model, and ok reports whether it did.
	if m, ok := final.(Model); ok {
		m.stopTurn()
	}
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	return nil
}

// programRelay forwards Send calls to a *tea.Program set after the model is
// built.
type programRelay struct {
	p *tea.Program
}

// Send passes msg to the program. Once the program has ended, Send drops the
// message instead of blocking.
func (r *programRelay) Send(msg tea.Msg) {
	r.p.Send(msg)
}
