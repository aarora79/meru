// This file holds the Bridge methods that read past conversations from
// merud, Sessions and SessionTurns, and turn merud's replies into the
// views the page draws: the rail's list grouped by day, and each past turn
// in the same shape as a live one.

package desktop

import (
	"context"
	"errors"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// The groups of the rail's conversation list.
const (
	GroupToday     = "Today"
	GroupYesterday = "Yesterday"
	GroupEarlier   = "Earlier"
)

// SessionView is one row of the rail's conversation list.
type SessionView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Updated is when the session last changed (RFC 3339), and Group is
	// GroupToday, GroupYesterday or GroupEarlier, by this machine's
	// calendar.
	Updated string `json:"updated"`
	Group   string `json:"group"`
}

// TurnView is one past turn, in the shape the page draws a live one.
type TurnView struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Route    string `json:"route,omitempty"`
	// Outcome says how a turn ended without a full answer; see
	// rpc.TurnInfo.
	Outcome string `json:"outcome,omitempty"`
	// Notice is the amber note under an answer that claimed an action no
	// tool performed, as the transcript keeps it.
	Notice string `json:"notice,omitempty"`
	// Sources are the files the turn's prompt held, numbered from 1, for
	// the side panel; Cited are the ones the answer cites, for the line
	// under the answer.
	Sources   []rpc.Citation `json:"sources,omitempty"`
	Cited     []rpc.Citation `json:"cited,omitempty"`
	Steps     []Step         `json:"steps,omitempty"`
	Contacted []string       `json:"contacted,omitempty"`
	// DurationMillis and TokensOut come from the transcript. The time to
	// the first token isn't in it, so a past turn's stats line leaves it
	// out.
	DurationMillis int64 `json:"duration_ms,omitempty"`
	TokensOut      int   `json:"tokens_out,omitempty"`
}

// Sessions returns the past conversations, the most recently changed
// first, grouped Today, Yesterday or Earlier. It fails when merud can't be
// reached, or answers with an error; a merud older than this app answers
// `unknown op "sessions"`.
func (b *Bridge) Sessions(ctx context.Context) ([]SessionView, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpSessions}, rpc.EventSessions)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]SessionView, 0, len(ev.Sessions))
	for _, s := range ev.Sessions {
		updated, err := time.Parse(time.RFC3339, s.Updated)
		if err != nil {
			updated = time.Time{} // an unreadable time sorts into Earlier
		}
		out = append(out, SessionView{ID: s.ID, Title: s.Title, Updated: s.Updated, Group: groupOf(updated, now)})
	}
	return out, nil
}

// SessionTurns returns every turn of session id, oldest first. It fails
// when merud can't be reached or doesn't know the session.
func (b *Bridge) SessionTurns(ctx context.Context, id string) ([]TurnView, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpSessionTurns, Session: id}, rpc.EventTurns)
	if err != nil {
		return nil, err
	}
	out := make([]TurnView, 0, len(ev.Turns))
	for _, t := range ev.Turns {
		v := TurnView{
			Question: t.Question, Answer: t.Answer, Route: t.Route, Outcome: t.Outcome, Notice: t.Notice,
			DurationMillis: t.DurationMillis, TokensOut: t.TokensOut,
		}
		for i, p := range t.Sources {
			v.Sources = append(v.Sources, rpc.Citation{N: i + 1, Path: p})
		}
		v.Cited = rpc.Cited(v.Answer, v.Sources)
		for _, s := range t.Tools {
			step := stepOf("", s.Kind, s.Name, s.Args)
			step.Outcome, step.DurationMillis = s.Outcome, s.DurationMillis
			v.Steps = append(v.Steps, step)
		}
		v.Contacted = contacted(v.Steps)
		out = append(out, v)
	}
	return out, nil
}

// groupOf files a session changed at t under Today, Yesterday or Earlier,
// by the calendar in now's time zone.
func groupOf(t, now time.Time) string {
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(midnight):
		return GroupToday
	case !t.Before(midnight.AddDate(0, 0, -1)):
		return GroupYesterday
	}
	return GroupEarlier
}

// requestTimeout bounds a request that only reads from merud, such as the
// session list or the status. merud answers these from files and memory,
// so a slow answer means something is wrong.
const requestTimeout = 5 * time.Second

// changeTimeout bounds a request that changes a setting. merud may start
// or reconnect every MCP server after the change, and each may take a
// while to start.
const changeTimeout = 90 * time.Second

// one sends req and returns the reply event of type want, waiting at most
// requestTimeout, or changeTimeout for an op that changes a setting. It
// fails when merud can't be reached, answers with an error event, or sends
// no event of that type.
func (b *Bridge) one(ctx context.Context, req rpc.Request, want rpc.EventType) (rpc.Event, error) {
	timeout := requestTimeout
	if changes(req.Op) {
		timeout = changeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var got *rpc.Event
	// These requests run no tools, so a nil ApproveFunc is right: it would
	// deny any approval.
	for ev, err := range rpc.Do(ctx, b.socket, req, nil) {
		if err != nil {
			return rpc.Event{}, err
		}
		switch ev.Type {
		case want:
			got = &ev
		case rpc.EventError:
			return rpc.Event{}, errors.New(ev.Error)
		}
	}
	if got == nil {
		return rpc.Event{}, errors.New("merud sent no " + string(want) + " reply")
	}
	return *got, nil
}

// done sends req, which merud answers with "done" alone, such as
// memory_forget or secret_set, and fails as one does.
func (b *Bridge) done(ctx context.Context, req rpc.Request) (rpc.Event, error) {
	return b.one(ctx, req, rpc.EventDone)
}

// changes reports whether op changes a setting or copies a file, and so
// may take longer.
func changes(op rpc.Op) bool {
	switch op {
	case rpc.OpToolPolicy, rpc.OpMCPAdd, rpc.OpMCPRemove, rpc.OpSecretSet, rpc.OpFolderAdd, rpc.OpFolderRemove,
		rpc.OpSkillEnable, rpc.OpSkillDisable, rpc.OpMemoryAdd, rpc.OpMemoryForget, rpc.OpFolders, rpc.OpAttachFile:
		return true
	}
	return false
}
