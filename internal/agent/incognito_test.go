// This file tests incognito chats: a turn that writes no file and no turns
// row, offers no remember tool and marks its calls incognito, a follow-up
// that reads the history merud holds, and the registry that forgets.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestIncognitoTurn(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("notes.search", `{"q":"pond"}`)}},
		{pieces: []string{"Dig it 60 cm deep."}},
	}}
	tools := &fakeTools{
		specs:   []engine.ToolSpec{spec("notes.search"), spec("remember")},
		results: map[string]fakeResult{"notes.search": {text: "pond.md: 60 cm"}},
	}
	turns := &fakeTurns{}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, turns, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "how deep is a pond?", Incognito: true})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	id := evs[0].Session
	if !transcript.IsIncognito(id) || !evs[0].Incognito {
		t.Fatalf("session event = %+v, want an incognito one", evs[0])
	}
	for _, c := range eng.calls {
		if slices.ContainsFunc(c.tools, func(s engine.ToolSpec) bool { return s.Name == "remember" }) {
			t.Error("an incognito turn offered remember")
		}
	}
	if calls := tools.recorded(); len(calls) != 1 || !calls[0].Incognito || calls[0].Session != id {
		t.Errorf("dispatch calls = %+v, want one incognito call", calls)
	}
	if rows := turns.recorded(); len(rows) != 0 {
		t.Errorf("turns rows = %+v, want none", rows)
	}
	// Nothing under the sessions folder: no file, no folder.
	if _, err := os.Stat(filepath.Join(cfg.Dir, "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sessions folder exists: %v", err)
	}

	// A follow-up reads the history merud holds in memory.
	eng.rounds = nil
	eng.pieces = []string{"Yes."}
	if _, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Session: id, Text: "and the width?"}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	msgs := eng.lastCall().msgs
	if !slices.ContainsFunc(msgs, func(m engine.Message) bool { return m.Content == "how deep is a pond?" }) {
		t.Errorf("the follow-up's prompt lacks the first question: %+v", msgs)
	}

	// Once forgotten, the chat is gone.
	if !a.ForgetIncognito(id) || a.ForgetIncognito(id) {
		t.Error("ForgetIncognito didn't report the chat once")
	}
	if _, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Session: id, Text: "still there?"}); !errors.Is(err, errIncognitoGone) {
		t.Errorf("a question in a forgotten chat = %v, want errIncognitoGone", err)
	}
}

func TestIncognitoChats(t *testing.T) {
	var c incognitoChats
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	first := c.start(now)
	if _, ok := c.get(first.ID(), now.Add(30*time.Minute)); !ok {
		t.Fatal("get lost a chat used half an hour ago")
	}
	if _, ok := c.get(first.ID(), now.Add(30*time.Minute+incognitoIdle+time.Second)); ok {
		t.Error("get kept a chat quiet past incognitoIdle")
	}
	// Past the cap, the chat quiet longest goes first.
	var ids []string
	for i := range maxIncognito + 1 {
		ids = append(ids, c.start(now.Add(time.Duration(i)*time.Second)).ID())
	}
	if _, ok := c.get(ids[0], now.Add(time.Minute)); ok {
		t.Error("the oldest chat survived past the cap")
	}
	if _, ok := c.get(ids[maxIncognito], now.Add(time.Minute)); !ok {
		t.Error("the newest chat is gone")
	}
}
