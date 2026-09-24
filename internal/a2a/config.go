// This file holds the settings for one A2A agent and the checks they must
// pass before the Client will use them. merud fills AgentConfig from an
// [[a2a.agents]] entry in config.toml.

package a2a

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/loopback"
)

// DefaultCallTimeout caps one call when AgentConfig.Timeout is zero. It
// matches the MCP default: an agent that runs longer than a minute is more
// often stuck than working, and the user is waiting on the answer.
const DefaultCallTimeout = 60 * time.Second

// maxNameLen caps an agent name. The name becomes part of every tool name
// the model sees, so a long one wastes prompt tokens.
const maxNameLen = 64

// maxSkillIDLen caps a skill ID in allow and confirm. It matches the longest
// MCP tool name, which keeps "a2a.<agent>.<skill>" a size models accept.
const maxSkillIDLen = 128

// AgentConfig is one A2A agent entry: where to find it and which of its
// skills the model may use.
type AgentConfig struct {
	// Name identifies the agent in tool names ("a2a.<name>.<skill>"), logs
	// and spans. Letters, digits, '-' and '_' only, so the second '.' in a
	// tool name always marks where the agent name ends.
	Name string
	// URL is where the Client reads the agent card: a base URL, which gets
	// "/.well-known/agent-card.json" added, or the card's full URL. It must
	// be loopback unless Remote is true.
	URL string
	// Remote allows an agent that isn't on this machine. It is the user's
	// explicit "my data may leave this machine" (ARCHITECTURE.md, "Privacy
	// boundary"). Without it the Client also refuses to connect to any
	// address off this machine, so a card on loopback can't send the calls
	// elsewhere.
	Remote bool
	// Headers go on every request to the agent, the card fetch included.
	// merud resolves "secret:<name>" values before it builds the Client.
	Headers map[string]string
	// Allow lists the skills, by their IDs on the agent card, that become
	// tools. An empty list gives the model nothing: agents are
	// deny-by-default (AGENTS.md, non-negotiable 3).
	Allow []string
	// Confirm lists allowed skills that need the user's yes on each call.
	// Every entry must also be in Allow.
	Confirm []string
	// Timeout caps one call. Zero means DefaultCallTimeout.
	Timeout time.Duration
}

// callTimeout returns the timeout for one call to this agent.
func (c AgentConfig) callTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultCallTimeout
}

// Validate reports every problem with the entry at once, joined into one
// error, so the user fixes config.toml in one pass. It returns nil when the
// entry is usable.
func (c AgentConfig) Validate() error {
	// errs collects the problems; errors.Join turns them into one error at
	// the end, and returns nil when the slice is empty.
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := checkAgentName(c.Name); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(c.URL) == "" {
		add("url is empty")
	} else if err := checkAgentURL(c.URL, c.Remote); err != nil {
		errs = append(errs, err)
	}

	for k := range c.Headers {
		// A header name is a "token" in HTTP terms. A name with a space,
		// colon or newline would break the request or smuggle in a second
		// header.
		if !validHeaderName(k) {
			add("headers: %q is not a valid header name", k)
		}
	}
	for k, v := range c.Headers {
		if strings.ContainsAny(v, "\r\n\x00") {
			add("headers: the value for %q holds a line break", k)
		}
	}

	allowed := make(map[string]bool, len(c.Allow))
	for _, skill := range c.Allow {
		if err := checkSkillID("allow", skill); err != nil {
			errs = append(errs, err)
		}
		allowed[skill] = true
	}
	for _, skill := range c.Confirm {
		if err := checkSkillID("confirm", skill); err != nil {
			errs = append(errs, err)
			continue
		}
		// A confirm entry outside allow would never be asked about, because
		// the skill never reaches the model. It is almost always a typo.
		if !allowed[skill] {
			add("confirm: %q is not in allow", skill)
		}
	}

	if c.Timeout < 0 {
		add("timeout must not be negative")
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("a2a agent %q: %w", c.Name, err)
	}
	return nil
}

// ValidateAll checks each entry and that no two share a name. Two agents
// with one name would produce the same tool names, and one would hide the
// other.
func ValidateAll(agents []AgentConfig) error {
	var errs []error
	seen := make(map[string]bool, len(agents))
	for _, a := range agents {
		if err := a.Validate(); err != nil {
			errs = append(errs, err)
		}
		if seen[a.Name] {
			errs = append(errs, fmt.Errorf("a2a agent %q: two agents share this name", a.Name))
		}
		seen[a.Name] = true
	}
	return errors.Join(errs...)
}

// checkAgentName fails unless name is 1 to maxNameLen letters, digits,
// dashes and underscores.
func checkAgentName(name string) error {
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

// checkSkillID fails unless id could become the last part of a tool name:
// 1 to maxSkillIDLen letters, digits, '_', '-' or '.'. field says which list
// the ID came from, for the error.
//
// The A2A spec lets a skill ID hold any text, but a model can only call a
// tool whose name uses these characters. A skill with any other ID can't
// become a tool.
//
// There are no wildcards. "*" in allow would hand the model every skill an
// agent offers, including skills it adds later that no one has read.
func checkSkillID(field, id string) error {
	if strings.Contains(id, "*") {
		return fmt.Errorf("%s: %q: wildcards aren't supported; name each skill", field, id)
	}
	if id == "" || len(id) > maxSkillIDLen {
		return fmt.Errorf("%s: %q is not a skill ID (1 to %d characters)", field, id, maxSkillIDLen)
	}
	for _, r := range id {
		if !isASCIIAlnum(r) && r != '_' && r != '-' && r != '.' {
			return fmt.Errorf("%s: %q: a skill ID Meru can use holds only letters, digits, '_', '-' and '.'", field, id)
		}
	}
	return nil
}

// checkAgentURL fails unless raw is an http or https URL, and a loopback one
// when remote is false. loopback.CheckURL holds the loopback rule, so A2A
// follows the same rule as MCP, the engine and the telemetry exporter.
func checkAgentURL(raw string, remote bool) error {
	if !remote {
		if err := loopback.CheckURL(raw); err != nil {
			return fmt.Errorf("url: %w (set remote = true to allow an agent on another machine)", err)
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

// validHeaderName reports whether name is a legal HTTP header name: one or
// more of the "token" characters RFC 9110 allows.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !isASCIIAlnum(r) && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}

// isASCIIAlnum reports whether r is an ASCII letter or digit. unicode.IsLetter
// would also accept letters from other scripts, which look alike on screen.
func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
