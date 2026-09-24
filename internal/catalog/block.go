// This file turns an Entry into the [[mcp.servers]] text for config.toml,
// and makes an Entry for a server the catalog doesn't know.

package catalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/loopback"
)

// Block renders e as a [[mcp.servers]] block, ready to paste into
// config.toml or hand to AppendServer. It writes the block by hand, not
// through the TOML encoder, so it can put a comment above the entry and
// keep the keys in the order a person reads them.
func Block(e Entry) string {
	var b strings.Builder
	// Fprintf writes formatted text into b; %s inserts a string.
	fmt.Fprintf(&b, "# %s: %s\n", e.Title, e.Description)
	if e.Docs != "" {
		fmt.Fprintf(&b, "# Docs: %s\n", e.Docs)
	}
	b.WriteString("[[mcp.servers]]\n")
	fmt.Fprintf(&b, "name    = %s\n", quote(e.Name))
	if e.URL != "" {
		fmt.Fprintf(&b, "url     = %s\n", quote(e.URL))
		if e.Network {
			b.WriteString("network = true   # the server runs on another machine\n")
		}
	} else {
		fmt.Fprintf(&b, "command = %s\n", quote(e.Command))
		if len(e.Args) > 0 {
			fmt.Fprintf(&b, "args    = %s\n", list(e.Args))
		}
	}
	if len(e.Env) > 0 {
		fmt.Fprintf(&b, "env     = %s\n", table(e.Env))
	}
	if len(e.Allow) == 0 {
		b.WriteString("# allow is empty, so the model gets none of this server's tools yet.\n" +
			"# Restart merud, run `meru tools` to see what the server offers,\n" +
			"# then name the tools to allow here.\n")
	}
	fmt.Fprintf(&b, "allow   = %s\n", list(e.Allow))
	fmt.Fprintf(&b, "confirm = %s\n", list(e.Confirm))
	return b.String()
}

// Custom makes an Entry for a server outside the catalog. commandOrURL is
// either a program to start (a stdio server, with args) or an http(s) URL
// (a Streamable HTTP server; Custom drops args). A URL that isn't loopback
// gets network = true, because the user named it on purpose.
//
// Its allow list is empty: nobody knows the server's tool names until merud
// has connected to it. The block says to run `meru tools` after restarting
// merud and then name the tools to allow.
func Custom(name, commandOrURL string, args []string) Entry {
	e := Entry{
		Name:        name,
		Title:       name,
		Description: "a server you added by hand",
	}
	if strings.HasPrefix(commandOrURL, "http://") || strings.HasPrefix(commandOrURL, "https://") {
		e.Transport = TransportHTTP
		e.URL = commandOrURL
		e.Network = loopback.CheckURL(commandOrURL) != nil
		return e
	}
	e.Transport = TransportStdio
	e.Command = commandOrURL
	// slices.Clone copies args, so the Entry doesn't share the caller's
	// backing array.
	e.Args = slices.Clone(args)
	return e
}

// list renders strings as a TOML array: ["a", "b"].
func list(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = quote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// table renders a string map as a TOML inline table, keys sorted:
// { A = "1", B = "2" }.
func table(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = key(k) + " = " + quote(m[k])
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
}

// key writes a TOML key: bare when it is only letters, digits, '_' and '-',
// quoted otherwise.
func key(k string) string {
	if k == "" {
		return `""`
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return quote(k)
		}
	}
	return k
}

// quote writes s as a TOML basic string. strconv.Quote looks close, but it
// writes escapes such as \x00 that TOML doesn't accept, so this escapes the
// few characters TOML requires and writes other control characters as
// \uXXXX.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
