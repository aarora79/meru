// This file holds `meru tools`: it asks merud which tools the model may use
// and prints them, grouped by source (an MCP server, an A2A agent, or
// merud's built-in tools). merud decides what is allowed; this side only
// formats the answer.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// noSources is what `meru tools` prints when merud has no tool source.
const noSources = `No tool sources yet. To add an MCP server, run meru mcp add <name>,
or add a [[mcp.servers]] entry to ~/.meru/config.toml and restart merud.`

// toolsCmd runs `meru tools` and `meru tools list`, which do the same thing.
// args are the words after "tools". It fails on any other word, when merud
// can't be reached, or when merud replies with an error.
func toolsCmd(ctx context.Context, socket string, args []string, stdout io.Writer) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "list") {
		return errors.New("usage: meru tools [list]")
	}
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpTools}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventTools:
			fmt.Fprint(stdout, toolsText(ev.Servers, newLook(stdout)))
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	return nil
}

// toolsText writes the sources as blocks of text, one per source, with a
// blank line between them:
//
//	notes  mcp · stdio · connected
//	  notes.search  asks first
//	  notes.read
//	  2 of 5 tools allowed
//
// Tool names line up in one column across all blocks, so the marks after
// them do too. lk colours the status and the marks when the terminal
// allows.
func toolsText(servers []rpc.ServerInfo, lk look) string {
	if len(servers) == 0 {
		return noSources + "\n"
	}
	nameWidth := 0
	for _, s := range servers {
		for _, t := range s.Tools {
			nameWidth = max(nameWidth, len([]rune(t.Name)))
		}
	}

	var b strings.Builder
	for i, s := range servers {
		if i > 0 {
			b.WriteString("\n")
		}
		// The header: name, then kind, transport and status, dot-separated.
		parts := []string{s.Kind}
		if s.Transport != "" {
			parts = append(parts, s.Transport)
		}
		status := lk.good.Render("connected")
		if !s.Connected {
			text := "not connected"
			if s.LastError != "" {
				text += ": " + s.LastError
			}
			status = lk.bad.Render(text)
		}
		fmt.Fprintf(&b, "%s  %s · %s\n", lk.bold.Render(s.Name), lk.dim.Render(strings.Join(parts, " · ")), status)

		for _, t := range s.Tools {
			mark := ""
			switch {
			case t.AlwaysAsks:
				mark = "always asks"
			case t.Confirm:
				mark = "asks first"
			}
			if mark == "" {
				fmt.Fprintf(&b, "  %s\n", t.Name)
				continue
			}
			// %-*s pads the name with spaces to nameWidth columns; the *
			// takes the width from the argument before the name.
			fmt.Fprintf(&b, "  %-*s  %s\n", nameWidth, t.Name, lk.amber.Render(mark))
		}
		fmt.Fprintf(&b, "  %s\n", lk.dim.Render(fmt.Sprintf("%d of %d tools allowed", len(s.Tools), s.Offered)))
		for _, u := range s.Unknown {
			fmt.Fprintf(&b, "  %s\n", lk.amber.Render(fmt.Sprintf("warning: allow lists %q, but %s offers no such tool", u, s.Name)))
		}
	}
	return b.String()
}
