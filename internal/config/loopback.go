// This file holds the loopback check for the Ollama and OTLP addresses. Meru
// only talks to this machine (see ARCHITECTURE.md, "Privacy boundary"), so a
// URL that could reach another host is a config error.

package config

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// checkLoopbackURL returns nil when raw is an http or https URL whose host is
// a loopback address: 127.0.0.0/8, ::1, or the name "localhost" when every
// address it resolves to is loopback.
//
// It refuses every other host name without looking it up. A name that
// resolves to 127.0.0.1 today could resolve elsewhere tomorrow, so only
// "localhost", which the operating system answers from its hosts file, gets
// resolved.
func checkLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q isn't a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q must start with http:// or https://", raw)
	}
	host := u.Hostname() // strips the port and the brackets around IPv6
	if host == "" {
		return fmt.Errorf("%q has no host", raw)
	}

	if strings.EqualFold(host, "localhost") {
		return checkLocalhost(raw)
	}

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%q must use a loopback address such as 127.0.0.1, ::1 or localhost", raw)
	}
	// Unmap turns an IPv4 address written in IPv6 form (::ffff:127.0.0.1)
	// back into plain IPv4, so both spellings get the same answer.
	if !addr.Unmap().IsLoopback() {
		return fmt.Errorf("%q isn't a loopback address; Meru only talks to this machine", raw)
	}
	return nil
}

// checkLocalhost resolves "localhost" and returns nil only when it resolves to
// at least one address and every address is loopback. A hosts file that points
// localhost elsewhere is rare, but it would send data off the machine.
func checkLocalhost(raw string) error {
	// A context carries a deadline and a cancel signal into calls that may
	// block. Two seconds is far more than a hosts-file lookup needs.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	// defer runs cancel() when this function returns, which frees the timer.
	defer cancel()

	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", "localhost")
	if err != nil {
		return fmt.Errorf("%q: resolve localhost: %w", raw, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%q: localhost resolved to no address", raw)
	}
	for _, a := range addrs {
		if !a.Unmap().IsLoopback() {
			return fmt.Errorf("%q: localhost resolves to %s, which isn't loopback; use 127.0.0.1 instead", raw, a)
		}
	}
	return nil
}
