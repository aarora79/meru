// This file holds the settings for one MCP server and the checks they must
// pass before the Pool will use them. The config package will fill
// ServerConfig from a [[mcp.servers]] entry in config.toml.

package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/loopback"
)

// DefaultCallTimeout caps one tool call when ServerConfig.Timeout is zero.
// A minute covers a slow web fetch or search; a call that runs longer is
// more likely stuck than working, and the user is waiting on the answer.
const DefaultCallTimeout = 60 * time.Second

// maxNameLen caps a server name. The name becomes part of every tool name the
// model sees, so a long one wastes prompt tokens.
const maxNameLen = 64

// maxToolNameLen is the longest tool name the MCP spec allows.
const maxToolNameLen = 128

// ServerConfig is one MCP server entry: how to reach it and which of its
// tools the model may use. Exactly one of Command and URL is set.
type ServerConfig struct {
	// Name identifies the server in tool names ("<name>.<tool>"), logs and
	// metrics. Letters, digits, '-' and '_' only, so the '.' in a tool name
	// always marks where the server name ends.
	Name string

	// Command is the program to start for a stdio server, and Args its
	// arguments. Meru runs it directly, with no shell, so quoting and globs
	// in Args reach the program as written.
	Command string
	Args    []string
	// Env holds extra environment variables for a stdio server. The child
	// gets these plus a small base (PATH, HOME and a few more; see
	// childEnv), not merud's whole environment.
	Env map[string]string

	// URL is the endpoint of a Streamable HTTP server that is already
	// running. It must be loopback unless Network is true.
	URL string
	// Network allows a URL that isn't loopback. It is the user's explicit
	// "this server may be on another machine" (ARCHITECTURE.md, "Privacy
	// boundary").
	Network bool
	// Headers go on every HTTP request to a Streamable HTTP server, such
	// as an Authorization header with an API key. merud resolves secrets
	// before they get here, so the values are the real ones.
	Headers map[string]string

	// Allow lists the tools the model may call, by the name the server
	// gives them. An empty list gives the model nothing: tools are
	// deny-by-default (AGENTS.md, non-negotiable 3).
	Allow []string
	// Confirm lists allowed tools that need the user's yes on each call.
	// Every entry must also be in Allow.
	Confirm []string

	// Timeout caps one tool call. Zero means DefaultCallTimeout.
	Timeout time.Duration
}

// transport names the transport this entry uses: "stdio" or "http". Status
// reports it, and spans derive network.transport from it.
func (c ServerConfig) transport() string {
	if c.URL != "" {
		return "http"
	}
	return "stdio"
}

// callTimeout returns the timeout for one call to this server.
func (c ServerConfig) callTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultCallTimeout
}

// Validate reports every problem with the entry at once, joined into one
// error, so the user fixes config.toml in one pass. It returns nil when the
// entry is usable.
func (c ServerConfig) Validate() error {
	// errs collects the problems; errors.Join turns them into one error at
	// the end, and returns nil when the slice is empty.
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := checkServerName(c.Name); err != nil {
		errs = append(errs, err)
	}

	hasCommand := strings.TrimSpace(c.Command) != ""
	hasURL := strings.TrimSpace(c.URL) != ""
	switch {
	case hasCommand && hasURL:
		add("set command or url, not both")
	case !hasCommand && !hasURL:
		add("set command (a stdio server) or url (a Streamable HTTP server)")
	}

	// Settings that only make sense for the other transport are mistakes,
	// most often a half-edited entry. Refuse them instead of ignoring them.
	if !hasCommand && (len(c.Args) > 0 || len(c.Env) > 0) {
		add("args and env apply only to a stdio server (command)")
	}
	if !hasURL && c.Network {
		add("network applies only to a Streamable HTTP server (url)")
	}
	if !hasURL && len(c.Headers) > 0 {
		add("headers apply only to a Streamable HTTP server (url)")
	}
	for k, v := range c.Headers {
		// A newline in a header would let a value start a second header.
		// Go's HTTP client refuses such a request anyway; catching it here
		// names the entry at startup instead of failing each call.
		if k == "" || strings.ContainsAny(k, " :\r\n\x00") {
			add("headers: %q is not a valid header name", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			add("headers: the value of %q holds a line break", k)
		}
	}

	if hasURL {
		if err := checkServerURL(c.URL, c.Network); err != nil {
			errs = append(errs, err)
		}
	}

	for k := range c.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			add("env: %q is not a valid variable name", k)
		}
	}

	allowed := make(map[string]bool, len(c.Allow))
	for _, tool := range c.Allow {
		if err := checkToolName("allow", tool); err != nil {
			errs = append(errs, err)
		}
		allowed[tool] = true
	}
	for _, tool := range c.Confirm {
		if err := checkToolName("confirm", tool); err != nil {
			errs = append(errs, err)
			continue
		}
		// A confirm entry outside allow would never be asked about, because
		// the tool never reaches the model. It is almost always a typo.
		if !allowed[tool] {
			add("confirm: %q is not in allow", tool)
		}
	}

	if c.Timeout < 0 {
		add("timeout must not be negative")
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("mcp server %q: %w", c.Name, err)
	}
	return nil
}

// ValidateAll checks each entry and that no two share a name. Two servers
// with one name would produce the same tool names, and one would hide the
// other.
func ValidateAll(servers []ServerConfig) error {
	var errs []error
	seen := make(map[string]bool, len(servers))
	for _, s := range servers {
		if err := s.Validate(); err != nil {
			errs = append(errs, err)
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("mcp server %q: two servers share this name", s.Name))
		}
		seen[s.Name] = true
	}
	return errors.Join(errs...)
}

// checkServerName fails unless name is 1 to maxNameLen letters, digits,
// dashes and underscores.
func checkServerName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("name is longer than %d characters", maxNameLen)
	}
	// Ranging over a string yields its characters (runes), not bytes.
	for _, r := range name {
		if !isASCIIAlnum(r) && r != '-' && r != '_' {
			return fmt.Errorf("name %q: use only letters, digits, '-' and '_'", name)
		}
	}
	return nil
}

// checkToolName fails unless name could be an MCP tool name: 1 to 128
// letters, digits, '_', '-' or '.', as the MCP spec defines them. field says
// which list the name came from, for the error.
//
// There are no wildcards. "*" in allow would hand the model every tool a
// server offers, including tools it adds in a later release that no one has
// read. Deny-by-default means the user names each tool.
func checkToolName(field, name string) error {
	if strings.Contains(name, "*") {
		return fmt.Errorf("%s: %q: wildcards aren't supported; name each tool", field, name)
	}
	if name == "" || len(name) > maxToolNameLen {
		return fmt.Errorf("%s: %q is not a tool name (1 to %d characters)", field, name, maxToolNameLen)
	}
	for _, r := range name {
		if !isASCIIAlnum(r) && r != '_' && r != '-' && r != '.' {
			return fmt.Errorf("%s: %q is not a tool name (letters, digits, '_', '-' and '.')", field, name)
		}
	}
	return nil
}

// checkServerURL fails unless raw is an http or https URL, and a loopback one
// when network is false. loopback.CheckURL holds the loopback rule, so MCP
// follows the same rule as the engine and the telemetry exporter.
func checkServerURL(raw string, network bool) error {
	if !network {
		if err := loopback.CheckURL(raw); err != nil {
			return fmt.Errorf("url: %w (set network = true to allow a server on another machine)", err)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url: %q must be an http or https URL with a host", raw)
	}
	return nil
}

// isASCIIAlnum reports whether r is an ASCII letter or digit. unicode.IsLetter
// would also accept letters from other scripts, which look alike on screen.
func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
