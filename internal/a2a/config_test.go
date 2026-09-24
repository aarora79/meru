// This file tests the agent config checks: which entries Validate and
// ValidateAll accept, and which they refuse.

package a2a

import (
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	// ok returns a valid entry; each row changes one thing.
	ok := func() AgentConfig {
		return AgentConfig{
			Name:    "research",
			URL:     "http://127.0.0.1:9100",
			Allow:   []string{"summarize", "translate"},
			Confirm: []string{"translate"},
		}
	}
	tests := []struct {
		name    string
		edit    func(c *AgentConfig)
		wantErr string // "" means valid; otherwise a piece of the error text
	}{
		{"valid loopback", func(c *AgentConfig) {}, ""},
		{"valid localhost card path", func(c *AgentConfig) { c.URL = "http://localhost:9100/agent-card.json" }, ""},
		{"valid ipv6 loopback", func(c *AgentConfig) { c.URL = "http://[::1]:9100" }, ""},
		{"valid remote", func(c *AgentConfig) { c.URL = "https://agent.example.com"; c.Remote = true }, ""},
		{"valid headers", func(c *AgentConfig) { c.Headers = map[string]string{"Authorization": "Bearer x", "X-Api-Key": "k"} }, ""},
		{"empty allow is valid and gives nothing", func(c *AgentConfig) { c.Allow = nil; c.Confirm = nil }, ""},
		{"skill ID with dots", func(c *AgentConfig) { c.Allow = []string{"docs.summarize_v2"}; c.Confirm = nil }, ""},
		{"valid timeout", func(c *AgentConfig) { c.Timeout = 5 * time.Minute }, ""},
		{"empty url", func(c *AgentConfig) { c.URL = "" }, "url is empty"},
		{"non-loopback url without remote", func(c *AgentConfig) { c.URL = "https://agent.example.com" }, "remote = true"},
		{"private address without remote", func(c *AgentConfig) { c.URL = "http://192.168.1.5:9100" }, "not a loopback"},
		{"url with bad scheme", func(c *AgentConfig) { c.URL = "ftp://127.0.0.1/agent" }, "scheme"},
		{"file url", func(c *AgentConfig) { c.URL = "file:///tmp/card.json" }, "scheme"},
		{"remote url with bad scheme", func(c *AgentConfig) { c.URL = "ftp://agent.example.com"; c.Remote = true }, "http or https"},
		{"remote url without host", func(c *AgentConfig) { c.URL = "http:///card"; c.Remote = true }, "with a host"},
		{"empty name", func(c *AgentConfig) { c.Name = "" }, "name is empty"},
		{"name with dot", func(c *AgentConfig) { c.Name = "my.agent" }, "letters, digits"},
		{"name with space", func(c *AgentConfig) { c.Name = "my agent" }, "letters, digits"},
		{"name non-ascii", func(c *AgentConfig) { c.Name = "rèsearch" }, "letters, digits"},
		{"name too long", func(c *AgentConfig) { c.Name = strings.Repeat("a", 65) }, "longer than"},
		{"wildcard allow", func(c *AgentConfig) { c.Allow = []string{"*"}; c.Confirm = nil }, "wildcards"},
		{"glob allow", func(c *AgentConfig) { c.Allow = []string{"sum*"}; c.Confirm = nil }, "wildcards"},
		{"wildcard confirm", func(c *AgentConfig) { c.Confirm = []string{"*"} }, "wildcards"},
		{"empty allow entry", func(c *AgentConfig) { c.Allow = []string{""}; c.Confirm = nil }, "not a skill ID"},
		{"skill ID too long", func(c *AgentConfig) { c.Allow = []string{strings.Repeat("s", 129)}; c.Confirm = nil }, "not a skill ID"},
		{"skill ID with space", func(c *AgentConfig) { c.Allow = []string{"sum up"}; c.Confirm = nil }, "only letters"},
		{"confirm not in allow", func(c *AgentConfig) { c.Confirm = []string{"delete"} }, "not in allow"},
		{"bad header name", func(c *AgentConfig) { c.Headers = map[string]string{"Bad Header": "x"} }, "header name"},
		{"header value with newline", func(c *AgentConfig) { c.Headers = map[string]string{"X-Key": "a\r\nX-Evil: 1"} }, "line break"},
		{"negative timeout", func(c *AgentConfig) { c.Timeout = -time.Second }, "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok()
			tt.edit(&c)
			err := c.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	c := AgentConfig{Name: "a b", URL: "https://agent.example.com", Allow: []string{"*"}, Timeout: -1}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want errors")
	}
	for _, want := range []string{"letters, digits", "remote = true", "wildcards", "negative"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() = %v, missing %q", err, want)
		}
	}
}

func TestValidateAll(t *testing.T) {
	good := AgentConfig{Name: "research", URL: "http://127.0.0.1:9100"}
	tests := []struct {
		name    string
		agents  []AgentConfig
		wantErr string
	}{
		{"none", nil, ""},
		{"two distinct", []AgentConfig{good, {Name: "travel", URL: "http://127.0.0.1:9200"}}, ""},
		{"duplicate names", []AgentConfig{good, good}, "share this name"},
		{"one bad entry", []AgentConfig{good, {Name: "remote", URL: "https://agent.example.com"}}, `"remote"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAll(tt.agents)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("ValidateAll() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("ValidateAll() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCallTimeoutDefault(t *testing.T) {
	if got := (AgentConfig{}).callTimeout(); got != DefaultCallTimeout {
		t.Errorf("callTimeout() = %v, want %v", got, DefaultCallTimeout)
	}
	if got := (AgentConfig{Timeout: time.Second}).callTimeout(); got != time.Second {
		t.Errorf("callTimeout() = %v, want 1s", got)
	}
}
