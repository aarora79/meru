// Tests for the rule on chats that read outside content: a tool that asks
// asks every time there, remember's ConfirmOutside level, jobs, and the
// text a declined call gives the model. See docs/design/security.md.

package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestEffectiveConfirm(t *testing.T) {
	// Each case: a confirm level, the tool's kind, whether the chat read
	// outside content, and the call's source; the level approve acts on.
	tests := []struct {
		name    string
		confirm Confirm
		kind    string
		outside bool
		source  rpc.Source
		want    Confirm
	}{
		{"ask, clean chat", ConfirmAsk, KindMCP, false, rpc.SourceDesktop, ConfirmAsk},
		{"ask, outside chat", ConfirmAsk, KindMCP, true, rpc.SourceDesktop, ConfirmAlways},
		{"a2a ask, outside chat", ConfirmAsk, KindA2A, true, rpc.SourceDesktop, ConfirmAlways},
		{"command ask, outside chat", ConfirmAsk, KindCommand, true, rpc.SourceDesktop, ConfirmAlways},
		{"built-in ask, outside chat", ConfirmAsk, KindBuiltin, true, rpc.SourceDesktop, ConfirmAsk}, // write_file keeps its session approval
		{"ask, outside job", ConfirmAsk, KindMCP, true, rpc.SourceJob, ConfirmAlways},                // approve then declines it in a job
		{"never stays never", ConfirmNever, KindMCP, true, rpc.SourceDesktop, ConfirmNever},
		{"always stays always", ConfirmAlways, KindMCP, false, rpc.SourceDesktop, ConfirmAlways},
		{"outside level, clean", ConfirmOutside, KindBuiltin, false, rpc.SourceDesktop, ConfirmNever},
		{"outside level, outside", ConfirmOutside, KindBuiltin, true, rpc.SourceDesktop, ConfirmAlways},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := effective(tc.confirm, tc.kind, Call{Outside: tc.outside, Source: tc.source})
			if got != tc.want {
				t.Errorf("effective = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOutsideIgnoresSessionApproval walks one chat: a session approval in
// a clean call stops the next clean call from asking, but once the chat
// read outside content the same tool asks again, with once and deny alone
// and the reason, every time. A built-in in confirm keeps its approval.
func TestOutsideIgnoresSessionApproval(t *testing.T) {
	mail := &fakeBackend{kind: KindMCP, tools: []string{"mail.send"}, confirm: map[string]Confirm{"mail.send": ConfirmAsk}}
	files := &fakeBackend{kind: KindBuiltin, tools: []string{"write_file"}, confirm: map[string]Confirm{"write_file": ConfirmAsk}}
	d := New([]Backend{mail, files}, &fakeRecorder{}, Options{})
	ctx := context.Background()

	steps := []struct {
		tool       string
		outside    bool
		choice     rpc.Choice
		wantAsk    bool
		wantOffr   string
		wantReason string
	}{
		{"mail.send", false, rpc.ChoiceSession, true, "once,session,deny", ""},
		{"mail.send", false, rpc.ChoiceDeny, false, "", ""}, // the session approval holds in a clean chat
		{"mail.send", true, rpc.ChoiceOnce, true, "once,deny", rpc.ReasonOutside},
		{"mail.send", true, rpc.ChoiceOnce, true, "once,deny", rpc.ReasonOutside}, // and again
		{"write_file", true, rpc.ChoiceSession, true, "once,session,deny", ""},
		{"write_file", true, rpc.ChoiceDeny, false, "", ""}, // a built-in keeps its session approval
	}
	for i, s := range steps {
		ap := &approver{choice: s.choice}
		c := newCall(s.tool, &sink{}, ap)
		c.Session, c.Outside = "s1", s.outside
		d.Dispatch(ctx, c)
		if asked := ap.askCount() == 1; asked != s.wantAsk {
			t.Errorf("step %d (%s, outside %v): asked = %v, want %v", i, s.tool, s.outside, asked, s.wantAsk)
			continue
		}
		if !s.wantAsk {
			continue
		}
		var offered []string
		for _, ch := range ap.asked[0].Choices {
			offered = append(offered, string(ch))
		}
		if got := strings.Join(offered, ","); got != s.wantOffr || ap.asked[0].Reason != s.wantReason {
			t.Errorf("step %d: offered %s with reason %q, want %s with %q", i, got, ap.asked[0].Reason, s.wantOffr, s.wantReason)
		}
	}
}

// TestRememberLevel checks ConfirmOutside end to end through Dispatch: no
// question in a clean chat, a question with once and deny in an outside
// one, and a decline in an outside job, which nobody watches.
func TestRememberLevel(t *testing.T) {
	b := &fakeBackend{kind: KindBuiltin, tools: []string{"remember"}, confirm: map[string]Confirm{"remember": ConfirmOutside}}
	d := New([]Backend{b}, &fakeRecorder{}, Options{})
	ctx := context.Background()

	ap := &approver{choice: rpc.ChoiceDeny}
	c := newCall("remember", &sink{}, ap)
	if _, out := d.Dispatch(ctx, c); out.Outcome != OutcomeOK || ap.askCount() != 0 {
		t.Errorf("clean chat: outcome %q, asked %d; want ok without a question", out.Outcome, ap.askCount())
	}

	ap = &approver{choice: rpc.ChoiceDeny}
	c = newCall("remember", &sink{}, ap)
	c.Outside = true
	if _, out := d.Dispatch(ctx, c); out.Outcome != OutcomeDeclined || ap.askCount() != 1 || len(ap.asked[0].Choices) != 2 {
		t.Errorf("outside chat: outcome %q, asked %d; want declined after one question with two choices", out.Outcome, ap.askCount())
	}

	ap = &approver{choice: rpc.ChoiceOnce}
	c = newCall("remember", &sink{}, ap)
	c.Outside, c.Source = true, rpc.SourceJob
	if _, out := d.Dispatch(ctx, c); out.Outcome != OutcomeDeclined || ap.askCount() != 0 {
		t.Errorf("outside job: outcome %q, asked %d; want declined without a question", out.Outcome, ap.askCount())
	}
	if n := b.callCount(); n != 1 {
		t.Errorf("remember ran %d times, want once, in the clean chat", n)
	}
}

// TestJobOutsideSendDeclined checks the job rule: a job's approver says
// yes to a ConfirmAsk call its allow list names, but not once the job has
// read outside content.
func TestJobOutsideSendDeclined(t *testing.T) {
	b := &fakeBackend{kind: KindMCP, tools: []string{"mail.send"}, confirm: map[string]Confirm{"mail.send": ConfirmAsk}}
	d := New([]Backend{b}, &fakeRecorder{}, Options{})
	for _, outside := range []bool{false, true} {
		ap := &approver{choice: rpc.ChoiceOnce}
		c := newCall("mail.send", &sink{}, ap)
		c.Source, c.Outside = rpc.SourceJob, outside
		_, out := d.Dispatch(context.Background(), c)
		want := OutcomeOK
		if outside {
			want = OutcomeDeclined
		}
		if out.Outcome != want {
			t.Errorf("job, outside %v: outcome %q, want %q", outside, out.Outcome, want)
		}
	}
}

// TestDeclineTextNoRetry checks that the model reads, after a no or a
// call nobody could approve, that it shouldn't ask again this turn.
func TestDeclineTextNoRetry(t *testing.T) {
	b := &fakeBackend{kind: KindMCP, tools: []string{"mail.send"}, confirm: map[string]Confirm{"mail.send": ConfirmAsk}}
	d := New([]Backend{b}, &fakeRecorder{}, Options{})
	for _, ap := range []*approver{{choice: rpc.ChoiceDeny}, nil} {
		// newCall leaves Approve nil for a nil approver: nobody can answer.
		res, _ := d.Dispatch(context.Background(), newCall("mail.send", &sink{}, ap))
		if !strings.HasSuffix(strings.ToLower(res.Text), "don't call it again in this turn.") {
			t.Errorf("decline text = %q, want it to end by telling the model not to retry", res.Text)
		}
	}
}
