// This file tests the connector rows of the /mcp box: f, the Fix key,
// which asks the fields merud names one at a time, a secret without
// showing it; o, which turns a connector on or off; and a, which shows
// the adopt plan and applies it on a second press.

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorsFixture is merud's answer to the connectors op: Obsidian short
// of its vault folder, Google set up by hand, and web search off.
var connectorsFixture = []rpc.ConnectorStatus{
	{ID: "obsidian", Name: "Obsidian", Kind: "stdio", State: rpc.ConnectorNeedsConfig, Sentence: "Obsidian needs your vault folder.",
		Fix: []string{"vault_path"}, Fields: []rpc.ConnectorField{
			{ID: "vault_path", Type: "folder", Label: "Vault folder", Help: "The folder that holds your Obsidian notes.", Required: true},
			{ID: "vault_name", Type: "text", Label: "Vault name"}}},
	{ID: "google", Name: "Google", Kind: "http", State: rpc.ConnectorByHand,
		Sentence: "Google is set up by hand, as the google entry in [[mcp.servers]]. To have Meru run it, run meru mcp adopt google."},
	{ID: "searxng", Name: "Web search", Kind: "container", State: rpc.ConnectorOff, Sentence: "Web search is off."},
}

// openConnectors opens the /mcp box and fills in the sources and the
// connectors. The marker starts on the first connector, Obsidian.
func openConnectors(t *testing.T, merud *fakeMerud) Model {
	t.Helper()
	m := openMCP(t, merud)
	m, _ = update(t, m, replyMsg{tag: tagConnectors, ev: rpc.Event{Type: rpc.EventConnectors, Connectors: connectorsFixture}})
	if len(m.mcpBox.connectors) != 3 {
		t.Fatalf("connectors = %+v", m.mcpBox.connectors)
	}
	return m
}

// connectorEvent returns a "connector" event for st.
func connectorEvent(st rpc.ConnectorStatus) rpc.Event {
	return rpc.Event{Type: rpc.EventConnector, Connector: &st}
}

func TestMCPConnectorRows(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventConnections, Connections: connsFixture}}}
	m, cmd := update(t, testModel(merud.ask, newFakeSender()), tea.WindowSizeMsg{Width: 120, Height: 50}, typeText("/mcp"), press(tea.KeyEnter))
	m, cmd = update(t, m, cmd())
	// The sources' reply asks for the connectors next.
	if cmd == nil {
		t.Fatal("no connectors request after the sources came")
	}
	merud.events = []rpc.Event{{Type: rpc.EventConnectors, Connectors: connectorsFixture}}
	m, _ = update(t, m, cmd())
	if got := lastReq(t, merud); got.Op != rpc.OpConnectors {
		t.Errorf("request = %+v, want connectors", got)
	}
	view := m.View()
	for _, want := range []string{"Connectors", "obsidian · needs config · Obsidian needs your vault folder.",
		"google · set up by hand", "searxng · off · Web search is off.", "f fix", "o on/off", "a adopt"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

// TestMCPFixWalksFields checks f on Obsidian: merud names vault_path, the
// form asks for it, and Enter sends it with connector_set, whose progress
// shows on the note line.
func TestMCPFixWalksFields(t *testing.T) {
	merud := &fakeMerud{}
	snd := newFakeSender()
	m := openConnectors(t, merud)
	m.send = snd
	merud.events = []rpc.Event{connectorEvent(connectorsFixture[0])}
	m, cmd := update(t, m, typeText("f"))
	if cmd == nil || m.mcpBox.busy == "" {
		t.Fatal("f sent nothing")
	}
	m, _ = update(t, m, cmd())
	if got := lastReq(t, merud); got.Op != rpc.OpConnectorFix || got.ID != "obsidian" {
		t.Errorf("request = %+v, want connector_fix obsidian", got)
	}
	w := m.mcpBox.walk
	if w == nil || len(w.fields) != 1 || w.fields[0].ID != "vault_path" {
		t.Fatalf("form = %+v, want vault_path alone", w)
	}
	if view := m.View(); !strings.Contains(view, "Set up Obsidian · 1 of 1") || !strings.Contains(view, "The folder that holds your Obsidian notes.") {
		t.Errorf("view:\n%s", view)
	}
	// q types into the form rather than closing the box.
	m, _ = update(t, m, typeText("~/Notes"))
	if m.mcpBox == nil || m.mcpBox.walk.input.Value() != "~/Notes" {
		t.Fatal("typing didn't reach the form")
	}

	starting := connectorsFixture[0]
	starting.State, starting.Sentence = rpc.ConnectorStarting, "Meru is installing Obsidian 2.0.1 and checking it."
	ready := connectorsFixture[0]
	ready.State, ready.Sentence, ready.Fix = rpc.ConnectorOK, "Obsidian is ready. It starts when a question needs it.", nil
	merud.events = []rpc.Event{connectorEvent(starting), connectorEvent(ready)}
	m, cmd = update(t, m, press(tea.KeyEnter))
	if cmd == nil || m.mcpBox.walk != nil {
		t.Fatal("the last Enter didn't send the form")
	}
	reply := cmd()
	got := lastReq(t, merud)
	if got.Op != rpc.OpConnectorSet || got.ID != "obsidian" || got.Connector == nil ||
		got.Connector.Values["vault_path"] != "~/Notes" || got.Connector.Enabled != nil {
		t.Errorf("request = %+v, want connector_set with the vault", got)
	}
	// The step came through the sender while the reply ran.
	select {
	case msg := <-snd.ch:
		m, _ = update(t, m, msg)
		if m.mcpBox.busy != starting.Sentence {
			t.Errorf("note = %q, want the step", m.mcpBox.busy)
		}
	default:
		t.Error("no progress step reached the model")
	}
	m, _ = update(t, m, reply)
	if !strings.Contains(m.notice, "Obsidian: ok. Obsidian is ready.") || m.mcpBox.busy != "" {
		t.Errorf("notice = %q, busy = %q", m.notice, m.mcpBox.busy)
	}
}

// TestMCPFixSecret checks the form for a connector that is off: it asks
// every field but the sign-in, hides the secret, sends the secret apart
// from the values, and turns the connector on. Esc leaves the form, not
// the box.
func TestMCPFixSecret(t *testing.T) {
	google := rpc.ConnectorStatus{ID: "google", Name: "Google", Kind: "http", State: rpc.ConnectorOff, Fields: []rpc.ConnectorField{
		{ID: "email", Type: "email", Label: "Email address", Required: true},
		{ID: "client_secret", Type: "secret", Label: "OAuth client secret", Required: true},
		{ID: "sign_in", Type: "oauth", Label: "Sign in to Google", Required: true},
	}}
	merud := &fakeMerud{}
	m := openMCP(t, merud)
	m, _ = update(t, m, replyMsg{tag: tagConnectors, ev: rpc.Event{Type: rpc.EventConnectors, Connectors: []rpc.ConnectorStatus{google}}})
	merud.events = []rpc.Event{connectorEvent(google)}
	m, cmd := update(t, m, typeText("f"))
	m, _ = update(t, m, cmd())
	if w := m.mcpBox.walk; w == nil || len(w.fields) != 2 || !w.turnOn {
		t.Fatalf("form = %+v, want email and the secret, turning Google on", w)
	}
	m, _ = update(t, m, press(tea.KeyEsc))
	if m.mcpBox == nil || m.mcpBox.walk != nil {
		t.Fatal("Esc should close the form and keep the box")
	}

	m, cmd = update(t, m, typeText("f"))
	m, _ = update(t, m, cmd())
	m, _ = update(t, m, typeText("dana@example.com"), press(tea.KeyEnter), typeText("invented-secret-3"))
	if strings.Contains(m.View(), "invented-secret-3") {
		t.Error("the view shows the secret")
	}
	merud.events = []rpc.Event{connectorEvent(rpc.ConnectorStatus{ID: "google", Name: "Google", State: rpc.ConnectorNeedsConfig,
		Sentence: "Google needs you to sign in.", Link: "https://accounts.example.test/o/oauth2/auth?client_id=x"})}
	m, cmd = update(t, m, press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	got := lastReq(t, merud).Connector
	if got == nil || got.Enabled == nil || !*got.Enabled || got.Values["email"] != "dana@example.com" ||
		got.Secrets["client_secret"] != "invented-secret-3" || got.Values["client_secret"] != "" {
		t.Errorf("change = %+v", got)
	}
	if !strings.Contains(m.notice, "Sign in: https://accounts.example.test/") {
		t.Errorf("notice = %q, want the sign-in link", m.notice)
	}
}

// TestMCPConnectorOnOff checks o: it turns web search on, and refuses
// Google, which is set up by hand.
func TestMCPConnectorOnOff(t *testing.T) {
	merud := &fakeMerud{}
	m := openConnectors(t, merud)
	m, cmd := update(t, m, press(tea.KeyDown), typeText("o"))
	if cmd != nil || !strings.Contains(m.notice, "press a") {
		t.Errorf("o on Google: cmd %v, notice %q", cmd != nil, m.notice)
	}
	merud.events = []rpc.Event{connectorEvent(rpc.ConnectorStatus{ID: "searxng", Name: "Web search", State: rpc.ConnectorOK, Sentence: "Web search is running in the container meru-searxng."})}
	m, cmd = update(t, m, press(tea.KeyDown), typeText("o"))
	if cmd == nil {
		t.Fatal("o on web search sent nothing")
	}
	m, _ = update(t, m, cmd())
	got := lastReq(t, merud)
	if got.Op != rpc.OpConnectorSet || got.ID != "searxng" || got.Connector == nil || got.Connector.Enabled == nil || !*got.Connector.Enabled {
		t.Errorf("request = %+v, want web search turned on", got)
	}
	if !strings.Contains(m.notice, "Web search: ok.") {
		t.Errorf("notice = %q", m.notice)
	}

	// merud's refusal to turn on a connector short of a field points at f.
	m, _ = update(t, m, replyMsg{tag: tagConnectorSet, err: errorString("Obsidian needs your vault folder.")})
	if !strings.Contains(m.notice, "Press f to set it up.") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestMCPAdoptKey checks a on Google: the first press shows merud's plan
// and changes nothing, the second applies it, and any other key in
// between drops the plan.
func TestMCPAdoptKey(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventAdopt, Adopted: &rpc.AdoptResult{ID: "google",
		Changes: []string{"Comment out the google entry.", "Add this after it:\n[connectors.google]\nenabled = true"}}}}}
	m := openConnectors(t, merud)
	m, cmd := update(t, m, press(tea.KeyDown), typeText("a"))
	m, _ = update(t, m, cmd())
	if got := lastReq(t, merud); got.Op != rpc.OpConnectorAdopt || got.Adopt != nil {
		t.Errorf("request = %+v, want the plan only", got)
	}
	view := m.View()
	for _, want := range []string{"To adopt google, merud will:", "1. Comment out the google entry.", "[connectors.google]", "Press a again"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	// Another key drops the plan, so a later a asks again.
	m, _ = update(t, m, typeText("x"))
	if m.mcpBox.adoptFor != "" {
		t.Error("another key kept the plan")
	}
	m, cmd = update(t, m, typeText("a"))
	m, _ = update(t, m, cmd())
	merud.events[0].Adopted.Applied = true
	m, cmd = update(t, m, typeText("a"))
	m, _ = update(t, m, cmd())
	if got := lastReq(t, merud); got.Adopt == nil || !got.Adopt.Apply {
		t.Errorf("request = %+v, want the apply", got)
	}
	if !strings.Contains(m.notice, "adopted google") {
		t.Errorf("notice = %q", m.notice)
	}
	// a on a connector that isn't set up by hand does nothing.
	m, cmd = update(t, m, press(tea.KeyHome), typeText("a"))
	if cmd != nil || !strings.Contains(m.notice, "only a server set up by hand") {
		t.Errorf("a on Obsidian: cmd %v, notice %q", cmd != nil, m.notice)
	}
}

// errorString is an error with fixed text, for a scripted refusal.
type errorString string

// Error returns the text.
func (e errorString) Error() string { return string(e) }
