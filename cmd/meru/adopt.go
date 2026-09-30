// This file holds `meru mcp adopt` and `meru mcp unadopt`: moving the
// obsidian or google server you set up by hand over to its connector, and
// back. merud does the work (the connector_adopt and connector_unadopt
// ops); this command shows what merud will change, asks, and then tells
// merud to go ahead. See ARCHITECTURE.md, "Moving to connectors".

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// adoptUsage is the shape of the two commands.
const adoptUsage = `usage:
  meru mcp adopt [--yes] [--email <address> --client-id <id>] <obsidian|google>
  meru mcp unadopt [--yes] <obsidian|google>`

// adopt runs `meru mcp adopt ...`, or `meru mcp unadopt ...` when undo is
// true. It asks merud what it would change and prints that; after a yes,
// or with --yes, it asks merud to make the changes, then prints the
// connector's state. --email and --client-id give Google's values when
// there is no start script to read them from, and then it asks for the
// client secret without showing it. It fails on words it doesn't know,
// and with merud's reason when merud refuses.
func (c *console) adopt(ctx context.Context, socket string, words []string, undo bool) error {
	sure := false
	values := map[string]string{}
	id := ""
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--yes" || w == "-yes" || w == "-y":
			sure = true
		case !undo && (w == "--email" || w == "--client-id") && i+1 < len(words):
			values[strings.ReplaceAll(strings.TrimPrefix(w, "--"), "-", "_")] = words[i+1]
			i++
		case strings.HasPrefix(w, "-") || id != "":
			return errors.New(adoptUsage)
		default:
			id = w
		}
	}
	if id == "" {
		return errors.New(adoptUsage)
	}
	if len(values) > 0 {
		fmt.Fprint(c.out, "The OAuth client's secret (it doesn't show as you type): ")
		secret, err := c.readSecret()
		if err != nil {
			return err
		}
		values["client_secret"] = secret
	}

	op := rpc.OpConnectorAdopt
	if undo {
		op = rpc.OpConnectorUnadopt
	}
	plan, err := adoptOp(ctx, socket, rpc.Request{Op: op, ID: id, Adopt: &rpc.AdoptRequest{Values: values}})
	if err != nil {
		return err
	}
	if plan.Nothing {
		fmt.Fprintln(c.out, strings.Join(plan.Changes, "\n"))
		return nil
	}
	verb := "adopt " + id
	if undo {
		verb = "restore the " + id + " entry"
	}
	fmt.Fprintf(c.out, "To %s, merud will:\n\n", verb)
	for i, line := range plan.Changes {
		fmt.Fprintf(c.out, "  %d. %s\n", i+1, strings.ReplaceAll(line, "\n", "\n     "))
	}
	fmt.Fprintln(c.out)
	if !sure {
		ok, err := c.yes("Go ahead?", false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(c.out, "Nothing was changed.")
			return nil
		}
	}
	if _, err := adoptOp(ctx, socket, rpc.Request{Op: op, ID: id, Adopt: &rpc.AdoptRequest{Apply: true, Values: values}}); err != nil {
		return err
	}
	if undo {
		fmt.Fprintf(c.out, "Done: the %s entry is back in [[mcp.servers]]. meru mcp adopt %s moves it over again.\n", id, id)
	} else {
		fmt.Fprintf(c.out, "Done. meru mcp unadopt %s puts the old entry back.\n", id)
	}
	// Show where the connector stands now; it may still be installing.
	for _, row := range connectorRows(ctx, socket) {
		if row.ID == id {
			fmt.Fprintf(c.out, "%s: %s %s\n", row.Name, rpc.ConnectorWords(row.State), row.Sentence)
			if row.Link != "" {
				fmt.Fprintf(c.out, "Sign in here: %s\n", row.Link)
			}
		}
	}
	if !undo {
		fmt.Fprintln(c.out, "meru mcp status shows how it goes.")
	}
	return nil
}

// adoptOp sends one adopt or unadopt request and returns merud's answer.
// It fails when merud can't be reached or refuses, with merud's reason.
func adoptOp(ctx context.Context, socket string, req rpc.Request) (rpc.AdoptResult, error) {
	var out rpc.AdoptResult
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return out, err
		}
		switch ev.Type {
		case rpc.EventAdopt:
			if ev.Adopted != nil {
				out = *ev.Adopted
			}
		case rpc.EventError:
			return out, errors.New(ev.Error)
		}
	}
	return out, nil
}
