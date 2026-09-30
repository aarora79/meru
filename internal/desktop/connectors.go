// This file holds the Bridge methods behind the connector cards in
// Settings, Connections: the list, Save (connector_set), Fix
// (connector_fix) and Adopt (connector_adopt). A save or a fix can take
// minutes while merud installs a connector, so each passes merud's
// progress to the page as it comes, as a KindConnector Update, and
// returns where the connector settled. merud checks and writes
// everything; a secret goes to merud and never comes back. See
// ARCHITECTURE.md, "Setting up a connector".

package desktop

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorTimeout bounds a save or a fix: merud follows a connector for
// up to five minutes while it installs, after the reload before that.
const connectorTimeout = 6 * time.Minute

// ConnectorDot is one connector as the rail shows it: a dot coloured by
// State, and a link to its card in Settings.
type ConnectorDot struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Words says the state as the page shows it, such as "needs config".
	Words string `json:"words"`
}

// Connectors asks merud for every connector, with its fields, for the
// cards in Settings. Every list comes back as [] rather than null, which
// the page's code can't loop over.
func (b *Bridge) Connectors(ctx context.Context) ([]rpc.ConnectorStatus, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpConnectors}, rpc.EventConnectors)
	if err != nil {
		return nil, err
	}
	out := make([]rpc.ConnectorStatus, 0, len(ev.Connectors))
	for _, c := range ev.Connectors {
		out = append(out, pageConnector(c))
	}
	return out, nil
}

// SetConnector saves change to connector id: on or off, field values and
// secrets, as the card's Save button and its switch send them. merud
// checks the change, writes it and follows the connector; each step
// reaches the page as a KindConnector Update, and the return value is
// where it settled. It fails with merud's reason when merud refuses,
// such as "Obsidian can't find the vault folder ~/Nowhere."
func (b *Bridge) SetConnector(ctx context.Context, id string, change rpc.ConnectorChange) (rpc.ConnectorStatus, error) {
	if strings.TrimSpace(id) == "" {
		return rpc.ConnectorStatus{}, errors.New("name a connector first")
	}
	return b.follow(ctx, rpc.Request{Op: rpc.OpConnectorSet, ID: id, Connector: &change})
}

// FixConnector is the card's Fix button. merud either names the fields to
// ask again, in the status's Fix, which the page then marks on the card,
// or runs the connector's check again and follows it, as SetConnector
// does.
func (b *Bridge) FixConnector(ctx context.Context, id string) (rpc.ConnectorStatus, error) {
	if strings.TrimSpace(id) == "" {
		return rpc.ConnectorStatus{}, errors.New("name a connector first")
	}
	return b.follow(ctx, rpc.Request{Op: rpc.OpConnectorFix, ID: id})
}

// AdoptConnector is the Adopt button on the card of a connector set up
// by hand. Without apply it asks merud for the plan, which the page shows
// in its own dialog; with apply merud makes the changes. It fails with
// merud's reason, such as a value the old entry doesn't hold.
func (b *Bridge) AdoptConnector(ctx context.Context, id string, apply bool) (rpc.AdoptResult, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpConnectorAdopt, ID: id, Adopt: &rpc.AdoptRequest{Apply: apply}}, rpc.EventAdopt)
	if err != nil {
		return rpc.AdoptResult{}, err
	}
	if ev.Adopted == nil {
		return rpc.AdoptResult{}, errors.New("merud sent no plan")
	}
	out := *ev.Adopted
	if out.Changes == nil {
		out.Changes = []string{}
	}
	return out, nil
}

// follow sends req, a connector_set or a connector_fix, emits each
// "connector" event to the page as a KindConnector Update, and returns
// the last, which says where the connector settled. It fails when merud
// can't be reached, refuses, or sends no status.
func (b *Bridge) follow(ctx context.Context, req rpc.Request) (rpc.ConnectorStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, connectorTimeout)
	defer cancel()
	var last *rpc.ConnectorStatus
	for ev, err := range rpc.Do(ctx, b.socket, req, nil) {
		if err != nil {
			return rpc.ConnectorStatus{}, err
		}
		switch ev.Type {
		case rpc.EventConnector:
			if ev.Connector == nil {
				continue
			}
			row := pageConnector(*ev.Connector)
			last = &row
			if b.emit != nil {
				b.emit(UpdateEvent, Update{Kind: KindConnector, Connector: &row})
			}
		case rpc.EventError:
			return rpc.ConnectorStatus{}, errors.New(ev.Error)
		}
	}
	if last == nil {
		return rpc.ConnectorStatus{}, errors.New("merud sent no connector status")
	}
	return *last, nil
}

// pageConnector returns c with every list set, [] rather than null, so
// the page can loop over each without a check.
func pageConnector(c rpc.ConnectorStatus) rpc.ConnectorStatus {
	if c.Fields == nil {
		c.Fields = []rpc.ConnectorField{}
	}
	if c.Fix == nil {
		c.Fix = []string{}
	}
	for i := range c.Fields {
		if c.Fields[i].Choices == nil {
			c.Fields[i].Choices = []string{}
		}
	}
	return c
}

// connectorDots returns the rail's dots: every connector in conns that
// isn't off, in merud's order.
func connectorDots(conns []rpc.ConnectorStatus) []ConnectorDot {
	out := []ConnectorDot{}
	for _, c := range conns {
		if c.State == rpc.ConnectorOff {
			continue
		}
		out = append(out, ConnectorDot{ID: c.ID, Name: c.Name, State: c.State, Words: rpc.ConnectorWords(c.State)})
	}
	return out
}
