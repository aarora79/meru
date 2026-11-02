// This file holds the second rule the package keeps: which addresses count
// as near the machine, for an answer endpoint with reach = "near-edge".
// Such an endpoint may run on the user's own network or private cloud
// network, never on the public internet. See docs/design/model-router.md.

package loopback

import "net/netip"

// sharedSpace is 100.64.0.0/10, the shared address space carrier-grade NAT
// uses, which mesh networks such as a tailnet also hand out to the user's
// own machines.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// NearEdge reports whether addr may serve a near-edge endpoint: loopback,
// a private address (10/8, 172.16/12, 192.168/16, fc00::/7), a link-local
// one (169.254/16, fe80::/10), or 100.64.0.0/10. Any other address is on
// the public internet. The check reads the address itself, so a host name
// must be resolved first; the engine checks each address it connects to.
func NearEdge(addr netip.Addr) bool {
	// Unmap turns ::ffff:10.0.0.5 back into 10.0.0.5.
	a := addr.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || sharedSpace.Contains(a)
}
