// This file starts the chat program: it wires the model to the real socket
// and to the running Bubble Tea program, then hands the terminal to Bubble Tea
// until the user quits.

package tui

import (
	"context"
	"fmt"
	"iter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/aarora79/meru/internal/rpc"
)

// Run opens the chat screen and talks to the merud listening on socket. info
// fills the header and turns on mouse copying. It returns when the user quits (nil) or when ctx is
// cancelled or the terminal fails (an error).
func Run(ctx context.Context, socket string, info Info) error {
	ask := func(ctx context.Context, req rpc.Request, approve rpc.ApproveFunc) iter.Seq2[rpc.Event, error] {
		return rpc.Do(ctx, socket, req, approve)
	}

	// The model needs a way to Send into the program, but the program is
	// built from the model. relay breaks the loop: the model gets relay now,
	// and relay gets the program one line later.
	relay := &programRelay{}
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	// With [chat] mouse_copy on, Bubble Tea asks the terminal for mouse
	// clicks, so a click on a code block's label can copy it. The terminal
	// then leaves plain click-and-drag to the program, which is why the
	// setting is off by default.
	if info.MouseCopy {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(newModel(ask, relay, info, terminalLook()), opts...)
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

// terminalLook picks how to draw on this terminal. Lip Gloss's default
// renderer checks stdout: how many colours it supports, whether NO_COLOR is
// set, and whether the background is dark. Asking now, before Bubble Tea
// takes over the terminal, keeps the background query's reply from landing
// in the input box.
//
// Glamour's "dark" and "light" styles colour the answer for each kind of
// background. With colour off, "notty" draws the same structure with no
// escape codes at all; the other two would still send bold codes.
func terminalLook() look {
	r := lipgloss.DefaultRenderer()
	style := "light"
	switch {
	case r.ColorProfile() == termenv.Ascii:
		style = "notty"
	case r.HasDarkBackground():
		style = "dark"
	}
	return look{renderer: r, markdownStyle: style, links: style != "notty"}
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
