// This file tests NearEdge, the rule for a near-edge endpoint's address.

package loopback

import (
	"net/netip"
	"testing"
)

func TestNearEdge(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1":       true,
		"::1":             true,
		"10.0.0.5":        true,
		"172.20.1.2":      true,
		"192.168.1.40":    true,
		"169.254.10.1":    true,
		"100.101.102.103": true,
		"fd12:3456::1":    true,
		"fe80::1":         true,
		"::ffff:10.0.0.5": true,
		"8.8.8.8":         false,
		"172.32.0.1":      false,
		"100.128.0.1":     false,
		"2001:4860::8888": false,
		"::ffff:1.1.1.1":  false,
	}
	for in, want := range tests {
		if got := NearEdge(netip.MustParseAddr(in)); got != want {
			t.Errorf("NearEdge(%s) = %v, want %v", in, got, want)
		}
	}
}
