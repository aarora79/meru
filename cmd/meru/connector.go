// This file holds `meru mcp set` and `meru mcp fix`: setting a
// connector's fields and turning it on or off, and the Fix button's
// terminal form. merud does the work (the connector_set and connector_fix
// ops): it checks each value against the connector's manifest, writes
// config.toml and secrets.toml, and follows the connector as it installs,
// starts and checks. This command reads the words, asks for a secret
// without showing it, and prints what merud reports. See ARCHITECTURE.md,
// "Setting up a connector".

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorUsage is the shape of the two commands. A secret field named
// alone, such as client_secret, is asked for without showing it: a secret
// never goes on the command line, where the shell's history would keep it.
const connectorUsage = `usage:
  meru mcp set <connector> [enabled=true|false] [field=value...] [secret-field...]
  meru mcp fix <connector>`

// setConnector runs `meru mcp set <id> …`. Each word is enabled=true or
// enabled=false, field=value for a field the connector shows, or the
// name of a secret field alone, which it asks for without echo. It sends
// one connector_set and prints each sentence merud reports until the
// connector settles. It fails on a word it can't read, on a secret given
// as field=value, and with merud's reason when merud refuses.
func (c *console) setConnector(ctx context.Context, socket string, words []string) error {
	if len(words) < 2 {
		return errors.New(connectorUsage)
	}
	id := words[0]
	row, err := connectorByID(ctx, socket, id)
	if err != nil {
		return err
	}
	types := map[string]rpc.ConnectorField{}
	for _, f := range row.Fields {
		types[f.ID] = f
	}
	change := rpc.ConnectorChange{Values: map[string]string{}, Secrets: map[string]string{}}
	for _, w := range words[1:] {
		// strings.Cut splits at the first "=": key, value, and whether it
		// found one.
		k, v, hasValue := strings.Cut(w, "=")
		f, known := types[k]
		switch {
		case k == "enabled" && hasValue:
			on, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("enabled takes true or false, not %q", v)
			}
			change.Enabled = &on
		case known && f.Type == "secret" && hasValue:
			return fmt.Errorf("%s is a secret: write %s alone and meru asks for it, so it stays out of your shell history", k, k)
		case known && f.Type == "secret":
			fmt.Fprintf(c.out, "%s (it doesn't show as you type): ", f.Label)
			secret, err := c.readSecret()
			if err != nil {
				return err
			}
			change.Secrets[k] = secret
		case hasValue:
			// merud checks the field, and says so when the connector
			// has none of that name.
			change.Values[k] = v
		default:
			return errors.New(connectorUsage)
		}
	}
	return c.sendChange(ctx, socket, row, change)
}

// fixConnector runs `meru mcp fix <id>`, the Fix button in a terminal. It
// asks merud to fix the connector. When merud answers with fields to ask
// (rpc.AskFields), it asks each one, then sends them with connector_set;
// a connector that is off is asked about first and turned on with them.
// Otherwise merud has run the check again, and it prints how that went.
func (c *console) fixConnector(ctx context.Context, socket string, words []string) error {
	if len(words) != 1 {
		return errors.New(connectorUsage)
	}
	row, err := c.connectorOp(ctx, socket, rpc.Request{Op: rpc.OpConnectorFix, ID: words[0]})
	if err != nil {
		return err
	}
	ask := rpc.AskFields(row)
	switch {
	case row.State == rpc.ConnectorOff:
		on, err := c.yes("Turn "+row.Name+" on?", true)
		if err != nil {
			return err
		}
		if !on {
			fmt.Fprintln(c.out, "Nothing was changed.")
			return nil
		}
	case len(ask) == 0:
		c.printConnector(row)
		return nil
	}
	change, err := c.askFields(ask)
	if err != nil {
		return err
	}
	if row.State == rpc.ConnectorOff {
		on := true
		change.Enabled = &on
	}
	return c.sendChange(ctx, socket, row, change)
}

// askFields asks for each field in fields, one line at a time: its label,
// its help, its choices, and the value it holds now, which Enter keeps. A
// secret field is read without echo, and Enter keeps the saved one. It
// returns the values that changed, the secrets apart.
func (c *console) askFields(fields []rpc.ConnectorField) (rpc.ConnectorChange, error) {
	change := rpc.ConnectorChange{Values: map[string]string{}, Secrets: map[string]string{}}
	for _, f := range fields {
		fmt.Fprintln(c.out)
		fmt.Fprintln(c.out, fieldTitle(f))
		if f.Help != "" {
			fmt.Fprintln(c.out, "  "+f.Help)
		}
		if f.Type == "secret" {
			prompt := "  (it doesn't show as you type): "
			if f.Saved {
				prompt = "  (saved already; Enter keeps it, and it doesn't show as you type): "
			}
			fmt.Fprint(c.out, prompt)
			v, err := c.readSecret()
			if err != nil {
				return change, err
			}
			if v != "" {
				change.Secrets[f.ID] = v
			}
			continue
		}
		now := f.Value
		if now == "" {
			now = f.Default
		}
		// The prompt reads "  value (a, b) [now]:", with each part only
		// when there is one.
		prompt := "  value"
		if len(f.Choices) > 0 {
			prompt += " (" + strings.Join(f.Choices, ", ") + ")"
		}
		if now != "" {
			prompt += " [" + now + "]"
		}
		v, err := c.ask(prompt + ":")
		if err != nil {
			return change, err
		}
		if v != "" && v != f.Value {
			change.Values[f.ID] = v
		}
	}
	return change, nil
}

// fieldTitle names a field for a prompt: its label, and "(optional)"
// when it may stay empty.
func fieldTitle(f rpc.ConnectorField) string {
	if f.Required {
		return f.Label
	}
	return f.Label + " (optional)"
}

// sendChange sends change to connector row with connector_set, prints
// each sentence as merud follows the connector, and then where it
// settled. It fails with merud's reason when merud refuses.
func (c *console) sendChange(ctx context.Context, socket string, row rpc.ConnectorStatus, change rpc.ConnectorChange) error {
	fmt.Fprintln(c.out)
	final, err := c.connectorOp(ctx, socket, rpc.Request{Op: rpc.OpConnectorSet, ID: row.ID, Connector: &change})
	if err != nil {
		return err
	}
	c.printConnector(final)
	return nil
}

// connectorOp sends req, a connector_set or connector_fix, prints the
// sentence of each "connector" event but the last as it arrives, and
// returns the last, which says where the connector stands. It fails when
// merud can't be reached, refuses, or sends no status.
func (c *console) connectorOp(ctx context.Context, socket string, req rpc.Request) (rpc.ConnectorStatus, error) {
	var last *rpc.ConnectorStatus
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return rpc.ConnectorStatus{}, err
		}
		switch ev.Type {
		case rpc.EventConnector:
			if ev.Connector == nil {
				continue
			}
			// Each sentence shows once the next event proves it was only
			// a step on the way.
			if last != nil {
				fmt.Fprintln(c.out, "  "+last.Sentence)
			}
			last = ev.Connector
		case rpc.EventError:
			return rpc.ConnectorStatus{}, errors.New(ev.Error)
		}
	}
	if last == nil {
		return rpc.ConnectorStatus{}, errors.New("merud sent no connector status")
	}
	return *last, nil
}

// printConnector prints where a connector stands: its name, state and
// sentence, the sign-in link while it waits for one, and the fix command
// when it needs fields.
func (c *console) printConnector(row rpc.ConnectorStatus) {
	fmt.Fprintf(c.out, "%s: %s. %s\n", row.Name, rpc.ConnectorWords(row.State), row.Sentence)
	if row.Link != "" {
		fmt.Fprintf(c.out, "Sign in here: %s\n", row.Link)
	}
	if hint := rpc.FixHint(row.ID, row.Fix); row.State == rpc.ConnectorNeedsConfig && hint != "" {
		fmt.Fprintln(c.out, hint)
	}
}

// connectorByID asks merud for its connectors and returns the one called
// id. It fails when merud can't say, or has none of that name.
func connectorByID(ctx context.Context, socket, id string) (rpc.ConnectorStatus, error) {
	var names []string
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpConnectors}, nil) {
		if err != nil {
			return rpc.ConnectorStatus{}, err
		}
		if ev.Type == rpc.EventError {
			return rpc.ConnectorStatus{}, errors.New(ev.Error)
		}
		for _, row := range ev.Connectors {
			if row.ID == id {
				return row, nil
			}
			names = append(names, row.ID)
		}
	}
	return rpc.ConnectorStatus{}, fmt.Errorf("merud has no connector called %q; it has %s", id, strings.Join(names, ", "))
}
