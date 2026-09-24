// This file holds the loopback check: the rule that Meru's model traffic and
// metrics may only go to an address on this machine.

package loopback

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// resolveTimeout caps how long CheckURL waits to look up "localhost".
// The answer comes from the hosts file, so it takes microseconds; the cap
// only stops a broken resolver from hanging startup.
const resolveTimeout = 2 * time.Second

// CheckURL returns nil when raw is an http or https URL whose host is
// a loopback address: anything in 127.0.0.0/8, ::1, or the name "localhost"
// when every address it resolves to is loopback. It returns an error saying
// what is wrong otherwise.
//
// It refuses every other host name without looking it up. A name that
// resolves to loopback today could resolve elsewhere tomorrow, and only
// "localhost" is reserved for this machine.
func CheckURL(raw string) error {
	// Go functions can return several values. url.Parse returns the parsed URL
	// and an error, and `:=` declares both variables at once.
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL %q: scheme must be http or https", raw)
	}
	host := u.Hostname() // strips the port and the [ ] around IPv6 addresses
	if host == "" {
		return fmt.Errorf("URL %q has no host", raw)
	}

	if strings.EqualFold(host, "localhost") {
		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		// defer runs cancel() when this function returns, freeing the timer.
		defer cancel()
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return fmt.Errorf("URL %q: resolve localhost: %w", raw, err)
		}
		if len(addrs) == 0 {
			return fmt.Errorf("URL %q: localhost resolves to nothing", raw)
		}
		// range walks a slice; the first value is the index, which `_` drops.
		for _, a := range addrs {
			if !a.Unmap().IsLoopback() {
				return fmt.Errorf("URL %q: localhost resolves to %s, which is not loopback", raw, a)
			}
		}
		return nil
	}

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("URL %q: host %q must be a loopback IP address or localhost", raw, host)
	}
	// Unmap turns an IPv4 address written as IPv6 (::ffff:127.0.0.1) back
	// into plain IPv4, so the loopback test sees it for what it is.
	if !addr.Unmap().IsLoopback() {
		return fmt.Errorf("URL %q: %s is not a loopback address", raw, addr)
	}
	return nil
}

// IsURL reports whether raw passes CheckURL. Use it where a
// yes or no is enough; use CheckURL when the user needs to read why.
func IsURL(raw string) bool {
	return CheckURL(raw) == nil
}
