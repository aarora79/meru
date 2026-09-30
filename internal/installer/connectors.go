// This file holds the hand-off of the connectors to merud. The Web
// search, Obsidian and Google steps only gather what each connector
// needs; the Start Meru step, once merud answers, sends each one to merud
// over the socket: connector_set to turn it on with its values, or
// connector_adopt to move a server the user set up by hand over to it.
// merud installs, starts and checks the connector, and the step shows
// each line merud reports. The installer itself runs no docker, uv or
// launchctl for a connector. See ARCHITECTURE.md, "Installer" and
// "Setting up a connector".

package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// handOffWait bounds each connector's hand-off: merud follows a connector
// for up to five minutes while it installs, after a reload before that.
const handOffWait = 6 * time.Minute

// HandOff is what one step asks the Start Meru step to send to merud for
// one connector: a change for connector_set, or, with Adopt, a
// connector_adopt that moves the hand-added entry over, with Values for
// what the entry doesn't hold. Change's secrets stay in the installer's
// memory until merud has them, and never go to the page or a log.
type HandOff struct {
	ID   string
	Name string
	// Change is the connector_set change; nil with Adopt.
	Change *rpc.ConnectorChange
	Adopt  bool
	Values map[string]string
}

// HandResult is where one connector stood after its hand-off: its line
// for the step's summary, and the sign-in link while it waits for one,
// which the page opens through the Bridge and never sees.
type HandResult struct {
	Line string
	Link string
}

// HandConnectors sends each hand-off to the merud on socket, in order,
// and passes each line merud reports to say. A connector merud refuses,
// or one that fails, doesn't stop the others: its line says why, and the
// user can fix it later in Meru.app's Settings. It returns one result
// per hand-off.
func HandConnectors(ctx context.Context, socket string, hands []HandOff, say func(string)) []HandResult {
	out := make([]HandResult, 0, len(hands))
	for _, h := range hands {
		res, err := handOne(ctx, socket, h, say)
		if err != nil {
			res = HandResult{Line: h.Name + ": merud couldn't set it up: " + strings.TrimSuffix(err.Error(), ".") + ". Fix it later in Meru.app's Settings, Connections."}
		}
		say(res.Line)
		out = append(out, res)
	}
	return out
}

// handOne sends one hand-off. A change goes to merud as connector_set,
// and merud follows the connector until it settles. An adopt asks merud
// to make the changes at once, since the user chose Adopt on the step's
// screen, which said what it does; merud then installs the connector on
// its own, and the step watches it through the connectors op.
func handOne(ctx context.Context, socket string, h HandOff, say func(string)) (HandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, handOffWait)
	defer cancel()
	var last *rpc.ConnectorStatus
	// show passes a sentence to say when it differs from the last one.
	show := func(st rpc.ConnectorStatus) {
		if last == nil || last.Sentence != st.Sentence {
			say("  " + st.Sentence)
		}
		last = &st
	}
	if h.Adopt {
		say("Moving your own " + h.Name + " server over to Meru")
		if err := adopt(ctx, socket, h); err != nil {
			return HandResult{}, err
		}
		if err := watchConnector(ctx, socket, h.ID, show); err != nil {
			return HandResult{}, err
		}
	} else {
		say("Asking merud to set up " + h.Name)
		req := rpc.Request{Op: rpc.OpConnectorSet, ID: h.ID, Connector: h.Change}
		for ev, err := range rpc.Do(ctx, socket, req, nil) {
			if err != nil {
				return HandResult{}, err
			}
			switch ev.Type {
			case rpc.EventConnector:
				if ev.Connector != nil {
					show(*ev.Connector)
				}
			case rpc.EventError:
				return HandResult{}, errors.New(ev.Error)
			}
		}
	}
	if last == nil {
		return HandResult{}, errors.New("merud sent no connector status")
	}
	line := fmt.Sprintf("%s: %s. %s", h.Name, rpc.ConnectorWords(last.State), last.Sentence)
	switch {
	case len(last.Fix) > 0:
		line += " Fix it in Meru.app's Settings, Connections."
	case last.Link != "":
		line += " Sign in with the button on the last screen, or on Google's card in Meru.app's Settings."
	}
	return HandResult{Line: line, Link: last.Link}, nil
}

// watchEvery is how often watchConnector asks merud about a connector.
const watchEvery = time.Second

// watchConnector asks merud for connector id's status every watchEvery
// and hands each to show, until the connector is no longer starting or
// ctx ends. It fails when merud can't say, or has no such connector.
func watchConnector(ctx context.Context, socket, id string, show func(rpc.ConnectorStatus)) error {
	for {
		st, err := connectorStatus(ctx, socket, id)
		if err != nil {
			return err
		}
		show(st)
		if st.State != rpc.ConnectorStarting {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil // the step reports where the connector stood last
		case <-time.After(watchEvery):
		}
	}
}

// connectorStatus asks merud for its connectors and returns the one
// called id.
func connectorStatus(ctx context.Context, socket, id string) (rpc.ConnectorStatus, error) {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpConnectors}, nil) {
		if err != nil {
			return rpc.ConnectorStatus{}, err
		}
		if ev.Type == rpc.EventError {
			return rpc.ConnectorStatus{}, errors.New(ev.Error)
		}
		for _, c := range ev.Connectors {
			if c.ID == id {
				return c, nil
			}
		}
	}
	return rpc.ConnectorStatus{}, fmt.Errorf("merud has no connector called %q", id)
}

// adopt asks merud to move the hand-added entry h.ID over to its
// connector, and fails with merud's reason when it refuses.
func adopt(ctx context.Context, socket string, h HandOff) error {
	req := rpc.Request{Op: rpc.OpConnectorAdopt, ID: h.ID, Adopt: &rpc.AdoptRequest{Apply: true, Values: h.Values}}
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return err
		}
		if ev.Type == rpc.EventError {
			return errors.New(ev.Error)
		}
	}
	return nil
}
