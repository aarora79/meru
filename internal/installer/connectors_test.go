// This file tests the connector hand-off against a fake merud: each
// hand-off goes out as connector_set or connector_adopt with what its
// step gathered, merud's steps reach the screen, a refusal doesn't stop
// the next connector, and Google's sign-in link stays in Go. The Bridge
// test runs the Start Meru step's order.

package installer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// handMerud is a fake merud for the hand-off. It records each request.
// Obsidian installs and ends ok; Google ends waiting for a sign-in; an
// adopted connector reads ok on the connectors op; and refuse names a
// connector whose set merud refuses.
type handMerud struct {
	refuse string

	mu   sync.Mutex
	reqs []rpc.Request
}

// handle answers one request.
func (f *handMerud) handle(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	name := map[string]string{"searxng": "Web search", "obsidian": "Obsidian", "google": "Google"}[req.ID]
	send := func(state, sentence, link string) error {
		return emit(rpc.Event{Type: rpc.EventConnector, Connector: &rpc.ConnectorStatus{ID: req.ID, Name: name, State: state, Sentence: sentence, Link: link}})
	}
	switch req.Op {
	case rpc.OpConnectorSet:
		if req.ID == f.refuse {
			return errors.New("Obsidian can't find the vault folder ~/Gone.")
		}
		if err := send(rpc.ConnectorStarting, "Meru is installing "+name+" and checking it.", ""); err != nil {
			return err
		}
		if req.ID == "google" {
			return send(rpc.ConnectorNeedsConfig, "Google needs you to sign in.", "https://accounts.example.test/o/oauth2/auth?client_id=x")
		}
		return send(rpc.ConnectorOK, name+" is ready.", "")
	case rpc.OpConnectorAdopt:
		return emit(rpc.Event{Type: rpc.EventAdopt, Adopted: &rpc.AdoptResult{ID: req.ID, Applied: true}})
	case rpc.OpConnectors:
		return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: []rpc.ConnectorStatus{
			{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorOK, Sentence: "Obsidian is ready."}}})
	}
	return nil
}

// ops returns each request's op and ID, "op id".
func (f *handMerud) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, string(r.Op)+" "+r.ID)
	}
	return out
}

func TestHandConnectors(t *testing.T) {
	f := &handMerud{}
	sock := fakeMerud(t, f.handle)
	on := true
	hands := []HandOff{
		{ID: "searxng", Name: "Web search", Change: &rpc.ConnectorChange{Enabled: &on}},
		{ID: "obsidian", Name: "Obsidian", Adopt: true},
		{ID: "google", Name: "Google", Change: &rpc.ConnectorChange{Enabled: &on,
			Values: map[string]string{"email": "dana@example.com"}, Secrets: map[string]string{"client_secret": "invented-secret-7"}}},
	}
	say, lines := collect()
	res := HandConnectors(context.Background(), sock, hands, say)

	// The adopt is followed through the connectors op, which names no
	// connector.
	want := []string{"connector_set searxng", "connector_adopt obsidian", "connectors ", "connector_set google"}
	if got := f.ops(); !slices.Equal(got, want) {
		t.Errorf("requests = %q, want %q", got, want)
	}
	f.mu.Lock()
	adoptReq, googleReq := f.reqs[1], f.reqs[3]
	f.mu.Unlock()
	if adoptReq.Adopt == nil || !adoptReq.Adopt.Apply {
		t.Errorf("adopt = %+v, want it applied", adoptReq.Adopt)
	}
	if googleReq.Connector == nil || googleReq.Connector.Secrets["client_secret"] != "invented-secret-7" {
		t.Errorf("google's change = %+v", googleReq.Connector)
	}

	news := strings.Join(*lines, "\n")
	for _, w := range []string{"Asking merud to set up Web search", "  Meru is installing Web search and checking it.",
		"Web search: ok. Web search is ready.", "Moving your own Obsidian server over to Meru", "Obsidian: ok. Obsidian is ready.",
		"Google: needs config. Google needs you to sign in."} {
		if !strings.Contains(news, w) {
			t.Errorf("the screen never said %q:\n%s", w, news)
		}
	}
	if strings.Contains(news, "invented-secret-7") || strings.Contains(news, "accounts.example.test") {
		t.Errorf("the screen shows the secret or the sign-in link:\n%s", news)
	}
	if len(res) != 3 || res[2].Link != "https://accounts.example.test/o/oauth2/auth?client_id=x" || res[0].Link != "" {
		t.Errorf("results = %+v", res)
	}
}

// TestHandConnectorsRefusal checks that merud's refusal of one connector
// leaves a line that says so and the next connector still goes out.
func TestHandConnectorsRefusal(t *testing.T) {
	f := &handMerud{refuse: "obsidian"}
	sock := fakeMerud(t, f.handle)
	on := true
	res := HandConnectors(context.Background(), sock, []HandOff{
		{ID: "obsidian", Name: "Obsidian", Change: &rpc.ConnectorChange{Enabled: &on}},
		{ID: "searxng", Name: "Web search", Change: &rpc.ConnectorChange{Enabled: &on}},
	}, func(string) {})
	if len(res) != 2 || !strings.Contains(res[0].Line, "can't find the vault folder") || !strings.Contains(res[0].Line, "Settings, Connections") {
		t.Errorf("results = %+v", res)
	}
	if !strings.HasPrefix(res[1].Line, "Web search: ok.") {
		t.Errorf("the next connector = %q", res[1].Line)
	}
}
