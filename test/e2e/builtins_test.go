//go:build e2e

// This file checks #123 with the real binaries: a config whose [builtin]
// tools leaves out a built-in shows it as New and off, and turning it on
// through the socket writes it into config.toml and clears the mark.

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// builtinCard returns the built-in tools' connection from an OpConnections
// reply.
func builtinCard(t *testing.T, events []rpc.Event) rpc.Connection {
	t.Helper()
	for _, ev := range events {
		for _, c := range ev.Connections {
			if c.Kind == "builtin" {
				return c
			}
		}
	}
	t.Fatal("no built-in connection in the reply")
	return rpc.Connection{}
}

// toolPolicy returns the tool named name on card c.
func toolPolicy(t *testing.T, c rpc.Connection, name string) rpc.ToolPolicy {
	t.Helper()
	for _, tp := range c.Tools {
		if tp.Name == name {
			return tp
		}
	}
	t.Fatalf("%s isn't on the card", name)
	return rpc.ToolPolicy{}
}

func TestNewBuiltinE2E(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	var listed []string
	for _, name := range config.BuiltinTools() {
		if name != "analyze_table" {
			listed = append(listed, fmt.Sprintf("%q", name))
		}
	}
	h.writeConfig(t, fakeConfig(f.url, "[builtin]\ntools = ["+strings.Join(listed, ", ")+"]\n"))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	tp := toolPolicy(t, builtinCard(t, op(t, h.socket, rpc.Request{Op: rpc.OpConnections})), "analyze_table")
	if !tp.New || tp.Policy != rpc.PolicyOff {
		t.Fatalf("analyze_table = %+v, want New and off", tp)
	}
	if !strings.Contains(h.log(), "built-in tools new since config.toml was written") {
		t.Errorf("merud.log doesn't name the new tool:\n%s", h.log())
	}

	change := rpc.Request{Op: rpc.OpToolPolicy, Policy: &rpc.PolicyChange{Kind: "builtin", Server: "meru", Tool: "analyze_table", Policy: rpc.PolicyAllow}}
	tp = toolPolicy(t, builtinCard(t, op(t, h.socket, change)), "analyze_table")
	if tp.New || tp.Policy != rpc.PolicyAllow {
		t.Errorf("after allow, analyze_table = %+v, want on and not New", tp)
	}
	cfg, err := config.Load(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Builtin.Tools, "analyze_table") {
		t.Errorf("[builtin] tools = %q, want analyze_table in it", cfg.Builtin.Tools)
	}
}
