// This file tests approvals: the "approval" event, the client's Reply, and
// what counts as a deny.

package rpc

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// askTwice is a Handler that asks about two tool calls and emits each
// answer as a token, so the test can read what the server received.
func askTwice(choices []Choice) Handler {
	return func(ctx context.Context, req Request, emit func(Event) error, approve ApproveFunc) error {
		for _, name := range []string{"notes.search", "mail.send"} {
			c, err := approve(ctx, Approval{Name: name, Kind: "mcp", Choices: choices})
			if err != nil {
				return err
			}
			if err := emit(Event{Type: EventToken, Text: name + "=" + string(c) + ";"}); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestApprovalRoundTrip(t *testing.T) {
	all := []Choice{ChoiceOnce, ChoiceSession, ChoiceDeny}
	tests := []struct {
		name    string
		offered []Choice
		approve ApproveFunc
		want    string
	}{
		{"client answers", all, func(ctx context.Context, a Approval) (Choice, error) {
			if a.Name == "mail.send" {
				return ChoiceDeny, nil
			}
			return ChoiceSession, nil
		}, "notes.search=session;mail.send=deny;"},
		{"nil approve denies", all, nil, "notes.search=deny;mail.send=deny;"},
		{"a choice not offered counts as deny", []Choice{ChoiceOnce, ChoiceDeny},
			func(context.Context, Approval) (Choice, error) { return ChoiceSession, nil },
			"notes.search=deny;mail.send=deny;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := startServer(t, askTwice(tt.offered))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var got string
			var seen []string
			approve := tt.approve
			if approve != nil {
				// Record every approval the client sees, then answer.
				inner := approve
				approve = func(ctx context.Context, a Approval) (Choice, error) {
					if a.ID == "" || !slices.Equal(a.Choices, tt.offered) {
						t.Errorf("approval = %+v, want an ID and choices %v", a, tt.offered)
					}
					seen = append(seen, a.Name)
					return inner(ctx, a)
				}
			}
			for ev, err := range Do(ctx, path, Request{Op: OpAsk, Text: "q"}, approve) {
				if err != nil {
					t.Fatal(err)
				}
				switch ev.Type {
				case EventApproval:
					t.Errorf("the loop saw an approval event; Do should answer it")
				case EventToken:
					got += ev.Text
				case EventError:
					t.Fatalf("error event: %s", ev.Error)
				}
			}
			if got != tt.want {
				t.Errorf("server received %q, want %q", got, tt.want)
			}
			if tt.approve != nil && !slices.Equal(seen, []string{"notes.search", "mail.send"}) {
				t.Errorf("client was asked about %v", seen)
			}
		})
	}
}

func TestApprovalFailsWhenClientGivesUp(t *testing.T) {
	result := make(chan error, 1)
	path := startServer(t, func(ctx context.Context, req Request, emit func(Event) error, approve ApproveFunc) error {
		_, err := approve(ctx, Approval{Name: "mail.send", Choices: []Choice{ChoiceOnce, ChoiceDeny}})
		result <- err
		return err
	})
	stop := errors.New("user pressed ctrl+c")
	for _, err := range Do(context.Background(), path, Request{Op: OpAsk, Text: "q"},
		func(context.Context, Approval) (Choice, error) { return "", stop }) {
		if err == nil {
			continue
		}
		if !errors.Is(err, stop) {
			t.Errorf("Do err = %v, want the approve error", err)
		}
		break
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("approve on the server = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server's approve never returned after the client hung up")
	}
}
