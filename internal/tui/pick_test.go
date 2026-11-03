// This file tests what meru chat does with the model pick: it folds a fast
// answer that ended early when merud escalates, offers the large model
// after a main answer that ended early, and sends /large.

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

func TestEscalateFolds(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{
		{Type: rpc.EventSession, Session: "s1"},
		{Type: rpc.EventToken, Text: "Paris is the cap"},
		{Type: rpc.EventEscalate, Text: "the fast model's answer was cut off, so main-model answers.", Model: "main-model"},
		{Type: rpc.EventToken, Text: "Paris is the capital of France."},
		{Type: rpc.EventDone, Model: "main-model", Outcome: "cut_off", OfferLarge: true},
	}}
	snd := newFakeSender()
	m, result := startTurn(t, testModel(merud.ask, snd), "capital of France?")
	m = waitDone(t, m, snd, result)
	turn := m.turns[0]
	if turn.answer != "Paris is the capital of France." {
		t.Errorf("answer = %q, want the main model's alone", turn.answer)
	}
	view := m.View()
	for _, w := range []string{"▸ first try, folded: the fast model's answer was cut off", "Paris is the cap", largeHint} {
		if !strings.Contains(view, w) {
			t.Errorf("view lacks %q:\n%s", w, view)
		}
	}
}

func TestLargeCommand(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventSession, Session: "s1"}, {Type: rpc.EventToken, Text: "Big."}, {Type: rpc.EventDone}}}
	snd := newFakeSender()
	m := testModel(merud.ask, snd)
	m, cmd := update(t, m, typeText("/large why is the sky blue?"), press(tea.KeyEnter))
	result := make(chan tea.Msg, 1)
	go func() { result <- finishTurn(cmd) }()
	waitDone(t, m, snd, result)
	if len(merud.reqs) != 1 || !merud.reqs[0].Large || merud.reqs[0].Text != "why is the sky blue?" {
		t.Errorf("requests = %+v, want one large ask", merud.reqs)
	}
}
