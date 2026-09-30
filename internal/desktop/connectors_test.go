// This file tests the Bridge methods behind the connector cards against
// an in-process rpc server: each sends the request merud expects, a save
// or a fix passes merud's steps to the page as KindConnector Updates and
// returns where the connector settled, lists reach the page as [] rather
// than null, and the rail gets a dot per connector that isn't off.

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorMerud answers the connector ops and records each request.
type connectorMerud struct {
	mu   sync.Mutex
	reqs []rpc.Request
}

// handle answers one request: the connectors op with Obsidian short of
// its vault and Ollama ok, a set with a step and then ok, a fix with
// the field to ask, and an adopt with its plan, or a refusal for
// "nothing".
func (f *connectorMerud) handle(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	obsidian := rpc.ConnectorStatus{ID: "obsidian", Name: "Obsidian", Kind: "stdio", State: rpc.ConnectorNeedsConfig,
		Sentence: "Obsidian needs your vault folder.", Fix: []string{"vault_path"},
		Fields: []rpc.ConnectorField{{ID: "vault_path", Type: "folder", Label: "Vault folder", Required: true}}}
	if req.ID == "nothing" {
		return errors.New(`merud has no connector called "nothing"`)
	}
	switch req.Op {
	case rpc.OpConnectors:
		off := rpc.ConnectorStatus{ID: "searxng", Name: "Web search", Kind: "container", State: rpc.ConnectorOff}
		ok := rpc.ConnectorStatus{ID: "ollama", Name: "Ollama", Kind: "dependency", State: rpc.ConnectorOK}
		return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: []rpc.ConnectorStatus{obsidian, off, ok}})
	case rpc.OpConnectorSet:
		step := obsidian
		step.State, step.Sentence, step.Fix = rpc.ConnectorStarting, "Meru is installing Obsidian 2.0.1 and checking it.", nil
		if err := emit(rpc.Event{Type: rpc.EventConnector, Connector: &step}); err != nil {
			return err
		}
		done := obsidian
		done.State, done.Sentence, done.Fix = rpc.ConnectorOK, "Obsidian is ready. It starts when a question needs it.", nil
		return emit(rpc.Event{Type: rpc.EventConnector, Connector: &done})
	case rpc.OpConnectorFix:
		return emit(rpc.Event{Type: rpc.EventConnector, Connector: &obsidian})
	case rpc.OpConnectorAdopt:
		return emit(rpc.Event{Type: rpc.EventAdopt, Adopted: &rpc.AdoptResult{ID: req.ID,
			Changes: []string{"Comment out the obsidian entry."}, Applied: req.Adopt != nil && req.Adopt.Apply}})
	case rpc.OpIndexStatus:
		return emit(rpc.Event{Type: rpc.EventStatus, Status: &rpc.IndexStatus{}})
	case rpc.OpMCPStatus:
		return emit(rpc.Event{Type: rpc.EventMCPStatus})
	}
	return nil
}

// last returns the newest request.
func (f *connectorMerud) last() rpc.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func TestConnectorMethods(t *testing.T) {
	f := &connectorMerud{}
	b, rec := newBridge(startServer(t, f.handle))
	ctx := context.Background()

	list, err := b.Connectors(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("Connectors = %+v, %v", list, err)
	}
	// Every list reaches the page as [], never null.
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "null") {
		t.Errorf("the page would get a null: %s", raw)
	}

	on := true
	change := rpc.ConnectorChange{Enabled: &on, Values: map[string]string{"vault_path": "~/Notes"}, Secrets: map[string]string{}}
	st, err := b.SetConnector(ctx, "obsidian", change)
	if err != nil || st.State != rpc.ConnectorOK {
		t.Fatalf("SetConnector = %+v, %v", st, err)
	}
	if got := f.last(); got.Op != rpc.OpConnectorSet || got.ID != "obsidian" || got.Connector == nil ||
		got.Connector.Enabled == nil || !*got.Connector.Enabled || !reflect.DeepEqual(got.Connector.Values, change.Values) {
		t.Errorf("request = %+v, want connector_set with the change", got)
	}
	var steps []string
	for _, u := range rec.all() {
		if u.Kind == KindConnector && u.Connector != nil {
			steps = append(steps, u.Connector.State)
		}
	}
	if !reflect.DeepEqual(steps, []string{rpc.ConnectorStarting, rpc.ConnectorOK}) {
		t.Errorf("the page got steps %v, want starting then ok", steps)
	}

	st, err = b.FixConnector(ctx, "obsidian")
	if err != nil || st.State != rpc.ConnectorNeedsConfig || !reflect.DeepEqual(st.Fix, []string{"vault_path"}) {
		t.Errorf("FixConnector = %+v, %v; want the field to ask", st, err)
	}
	if got := f.last(); got.Op != rpc.OpConnectorFix || got.ID != "obsidian" {
		t.Errorf("request = %+v", got)
	}

	plan, err := b.AdoptConnector(ctx, "obsidian", false)
	if err != nil || plan.Applied || len(plan.Changes) != 1 {
		t.Errorf("the plan = %+v, %v", plan, err)
	}
	if got := f.last(); got.Op != rpc.OpConnectorAdopt || got.Adopt == nil || got.Adopt.Apply {
		t.Errorf("request = %+v, want the plan only", got)
	}
	if plan, err = b.AdoptConnector(ctx, "obsidian", true); err != nil || !plan.Applied {
		t.Errorf("the apply = %+v, %v", plan, err)
	}

	// merud's refusal reaches the page as the error; an empty name never
	// leaves the Bridge.
	if _, err := b.SetConnector(ctx, "nothing", change); err == nil || !strings.Contains(err.Error(), "no connector called") {
		t.Errorf("SetConnector(nothing) = %v", err)
	}
	if _, err := b.FixConnector(ctx, " "); err == nil {
		t.Error("FixConnector with no name = nil, want an error")
	}

	// The rail's dots: every connector but the one that is off.
	s := b.Status(ctx)
	want := []ConnectorDot{{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorNeedsConfig, Words: "needs config"},
		{ID: "ollama", Name: "Ollama", State: rpc.ConnectorOK, Words: "ok"}}
	if !reflect.DeepEqual(s.Connectors, want) {
		t.Errorf("dots = %+v, want %+v", s.Connectors, want)
	}
}
