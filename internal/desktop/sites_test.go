// This file tests the Bridge's trusted websites against an in-process rpc
// server, and the approval card's website buttons.

package desktop

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestTrustedSites(t *testing.T) {
	sites := []string{"arxiv.org", "raw.githubusercontent.com"}
	b, _ := newBridge(startServer(t, func(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		switch req.Op {
		case rpc.OpTrustedSites:
			return emit(rpc.Event{Type: rpc.EventTrustedSites, Sites: sites})
		case rpc.OpTrustedSiteRemove:
			if !slices.Contains(sites, req.ID) {
				return errors.New("[web] trusted_sites doesn't list " + req.ID)
			}
			sites = slices.DeleteFunc(slices.Clone(sites), func(h string) bool { return h == req.ID })
			return emit(rpc.Event{Type: rpc.EventTrustedSites, Sites: sites})
		}
		return errors.New("unexpected op")
	}))
	got, err := b.TrustedSites(context.Background())
	if err != nil || !slices.Equal(got, []string{"arxiv.org", "raw.githubusercontent.com"}) {
		t.Fatalf("TrustedSites = %v, %v", got, err)
	}
	left, err := b.RemoveTrustedSite(context.Background(), "arxiv.org")
	if err != nil || !slices.Equal(left, []string{"raw.githubusercontent.com"}) {
		t.Errorf("RemoveTrustedSite = %v, %v", left, err)
	}
	if _, err := b.RemoveTrustedSite(context.Background(), "nope.example"); err == nil {
		t.Error("removing a host the list doesn't hold worked")
	}
	if _, err := b.RemoveTrustedSite(context.Background(), ""); err == nil {
		t.Error("removing an empty host worked")
	}
}

// TestSiteChoiceLabels checks that the website buttons name the host, and
// that Approve takes both website answers.
func TestSiteChoiceLabels(t *testing.T) {
	v := approvalView(rpc.Approval{ID: "a1", Name: "web_fetch", Kind: "builtin",
		Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSite, rpc.ChoiceSiteAlways, rpc.ChoiceDeny}, Site: "arxiv.org"})
	var labels []string
	for _, c := range v.Choices {
		labels = append(labels, c.Label)
	}
	want := []string{"Allow once", "Allow arxiv.org for this chat", "Always allow arxiv.org", "Don't allow"}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %q, want %q", labels, want)
	}
	b := New(Options{Socket: "merud.sock"})
	for _, c := range []string{"site", "site_always"} {
		if err := b.Approve("none", c); err == nil || err.Error() == `"`+c+`" isn't an answer to an approval` {
			t.Errorf("Approve(%q) = %v, want it accepted as an answer (and the card not open)", c, err)
		}
	}
}
