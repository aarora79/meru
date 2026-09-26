// This file answers the two session ops, OpSessions and OpSessionTurns,
// which the desktop app sends to list past conversations and show one
// again. Both read the session transcripts, the source of truth, and never
// meru.db. ARCHITECTURE.md, "Desktop app", says what the app shows.

package main

import (
	"fmt"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// defaultSessionLimit is how many sessions OpSessions lists when the
// request sets no limit. A list longer than a few hundred is more than a
// person scrolls, and each one costs merud a file read.
const defaultSessionLimit = 200

// historyService answers the session ops from the transcripts in dir.
// home is the user's home folder, for showing source paths as ~/...; it
// is "" when unknown, and paths then stay whole.
type historyService struct {
	dir  string
	home string
}

// handleSessions answers OpSessions with one "sessions" event: the
// sessions that hold a question, the most recently changed first, at most
// limit of them (defaultSessionLimit when limit is zero or less). Times go
// out in RFC 3339, in UTC, as the transcripts store them. It fails when
// the sessions folder can't be read.
func (h historyService) handleSessions(limit int, emit func(rpc.Event) error) error {
	if limit <= 0 {
		limit = defaultSessionLimit
	}
	infos, err := transcript.List(h.dir, limit)
	if err != nil {
		return err
	}
	out := make([]rpc.SessionInfo, len(infos))
	for i, s := range infos {
		out[i] = rpc.SessionInfo{
			ID:      s.ID,
			Title:   s.Title,
			Started: s.Started.UTC().Format(time.RFC3339),
			Updated: s.Updated.UTC().Format(time.RFC3339),
			Turns:   s.Turns,
		}
	}
	return emit(rpc.Event{Type: rpc.EventSessions, Sessions: out})
}

// handleTurns answers OpSessionTurns with one "turns" event holding every
// turn of the session id names. It fails when id isn't a session ID, the
// session doesn't exist, or its file can't be read. transcript.Open checks
// the ID's shape, so a client can't name a file outside the sessions
// folder.
func (h historyService) handleTurns(id string, emit func(rpc.Event) error) error {
	if id == "" {
		return fmt.Errorf("session_turns needs a session ID")
	}
	s, err := transcript.Open(h.dir, id)
	if err != nil {
		return err
	}
	lines, err := s.Lines()
	if err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventTurns, Session: id, Turns: turnsOf(lines, h.home)})
}

// turnsOf groups a session's lines into turns. A user line starts a turn;
// the tool_call and tool_result lines after it become its tool steps, paired
// by call ID; the assistant line fills in the answer. Lines before the
// first question, approval lines and summary lines add nothing a reader of
// the conversation needs, so turnsOf skips them.
func turnsOf(lines []transcript.Line, home string) []rpc.TurnInfo {
	var turns []rpc.TurnInfo
	// calls maps a call ID to its step's index in the current turn, so a
	// tool_result line finds the step its tool_call line started.
	calls := map[string]int{}
	for _, l := range lines {
		if l.Type == transcript.TypeUser {
			turns = append(turns, rpc.TurnInfo{Time: l.TS.UTC().Format(time.RFC3339), Question: l.Text})
			clear(calls) // clear empties the map, ready for the new turn
			continue
		}
		if len(turns) == 0 {
			continue
		}
		// cur points into the slice, so the changes below land in it.
		cur := &turns[len(turns)-1]
		switch l.Type {
		case transcript.TypeToolCall:
			calls[l.CallID] = len(cur.Tools)
			cur.Tools = append(cur.Tools, rpc.ToolStep{
				Name: rpc.ToolName(l.Kind, l.Server, l.Tool),
				Kind: l.Kind,
				Args: l.Args,
			})
		case transcript.TypeToolResult:
			// i, ok := m[k] reads a map entry; ok is false when k isn't there.
			if i, ok := calls[l.CallID]; ok {
				cur.Tools[i].Outcome = l.Outcome
				cur.Tools[i].DurationMillis = l.Ms
			}
		case transcript.TypeAssistant:
			cur.Answer = l.Text
			cur.Route = l.Route
			cur.Outcome = l.Outcome
			cur.DurationMillis = l.Ms
			cur.TokensIn = l.TokensIn
			cur.TokensOut = l.TokensOut
			cur.Sources = nil
			for _, p := range l.Sources {
				cur.Sources = append(cur.Sources, rpc.ShortPath(home, p))
			}
		}
	}
	return turns
}
