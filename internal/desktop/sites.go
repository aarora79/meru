// This file holds the Bridge's side of the trusted websites: the list of
// hosts web_fetch reads without asking in every session, [web]
// trusted_sites, which the Web search card in Settings shows, and taking
// one off it. merud keeps and writes the list (cmd/merud/sites.go); the
// Bridge only asks. A host joins the list from an approval card's "Always
// allow" button, which goes through Approve.

package desktop

import (
	"context"
	"errors"

	"github.com/aarora79/meru/internal/rpc"
)

// TrustedSites returns the hosts web_fetch reads without asking in every
// session, as config.toml lists them. It fails when merud can't be reached
// or is too old to know the op.
func (b *Bridge) TrustedSites(ctx context.Context) ([]string, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpTrustedSites}, rpc.EventTrustedSites)
	if err != nil {
		return nil, err
	}
	return ev.Sites, nil
}

// RemoveTrustedSite takes host off the list, so web_fetch asks again
// before it reads that site in a new chat, and returns the hosts left. It
// fails for an empty host, one the list doesn't hold, or when merud can't
// be reached.
func (b *Bridge) RemoveTrustedSite(ctx context.Context, host string) ([]string, error) {
	if host == "" {
		return nil, errors.New("no website to remove")
	}
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpTrustedSiteRemove, ID: host}, rpc.EventTrustedSites)
	if err != nil {
		return nil, err
	}
	return ev.Sites, nil
}
