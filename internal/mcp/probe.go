// This file holds Probe, which starts a server for a moment to see what
// tools it offers, before the user adds it to config.toml.

package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// ProbeInfo is what a probed server offers.
type ProbeInfo struct {
	// ServerName and ServerVersion are what the server says about itself
	// in the MCP handshake. Either may be empty.
	ServerName    string
	ServerVersion string
	// Tools lists every tool the server offers, sorted by name.
	Tools []ProbeTool
}

// ProbeTool is one tool a probed server offers, with the hints MCP lets a
// server give about it. MCP calls them hints: the server's word, not a
// promise.
type ProbeTool struct {
	Name        string
	Description string
	// ReadOnly is the server's readOnlyHint: the tool changes nothing. It
	// is nil when the tool carries no annotations. The Go SDK reads a
	// missing readOnlyHint as false, which is also MCP's default, so a
	// tool with annotations always has a ReadOnly.
	ReadOnly *bool
	// Destructive is the server's destructiveHint: the tool may delete or
	// overwrite. It is nil when the server leaves the hint out.
	Destructive *bool
}

// Probe starts or connects to the server cfg describes, lists every tool
// it offers, and stops it again. It uses the same transports as the Pool:
// a stdio child with the trimmed environment, or Streamable HTTP with the
// loopback rule and the headers. Probe calls no tool.
//
// cfg needs no allow list; Probe ignores Allow, Confirm and Timeout. It
// fails when the rest of cfg is invalid, when the server won't start or
// answer, or when it takes longer than 30 seconds to connect and list.
// A stdio child never outlives Probe, even when it fails part-way.
func Probe(ctx context.Context, cfg ServerConfig, log *slog.Logger) (ProbeInfo, error) {
	return probe(ctx, cfg, log, dialTransport, connectTimeout)
}

// probe is Probe with the transport builder and the time limit as
// parameters, so tests can make it time out in a second.
func probe(ctx context.Context, cfg ServerConfig, log *slog.Logger, dial dialFunc, limit time.Duration) (ProbeInfo, error) {
	// cfg is a copy, since Go passes structs by value, so clearing these
	// changes nothing for the caller. Without them, Validate checks only
	// how to reach the server.
	cfg.Allow, cfg.Confirm, cfg.Timeout = nil, nil, 0
	if err := cfg.Validate(); err != nil {
		return ProbeInfo{}, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// procCtx bounds the stdio child's life. The session's Close below
	// asks the child to exit; stop, deferred first so it runs last, kills
	// it if it hasn't.
	procCtx, stop := context.WithCancel(context.Background())
	defer stop()
	t, err := dial(procCtx, cfg, log)
	if err != nil {
		return ProbeInfo{}, fmt.Errorf("mcp server %q: %w", cfg.Name, err)
	}

	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// Connect runs the MCP handshake. When it fails, the SDK closes the
	// connection itself, which stops the child.
	cs, err := newClient(log).Connect(cctx, t, nil)
	if err != nil {
		return ProbeInfo{}, probeError(cfg.Name, "connect", cctx.Err(), limit, err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := listTools(cctx, cs)
	if err != nil {
		return ProbeInfo{}, probeError(cfg.Name, "list tools", cctx.Err(), limit, err)
	}

	var info ProbeInfo
	if ir := cs.InitializeResult(); ir != nil && ir.ServerInfo != nil {
		info.ServerName, info.ServerVersion = ir.ServerInfo.Name, ir.ServerInfo.Version
	}
	for _, tool := range tools {
		pt := ProbeTool{Name: tool.Name, Description: tool.Description}
		if a := tool.Annotations; a != nil {
			readOnly := a.ReadOnlyHint
			pt.ReadOnly = &readOnly
			pt.Destructive = a.DestructiveHint
		}
		info.Tools = append(info.Tools, pt)
	}
	slices.SortFunc(info.Tools, func(a, b ProbeTool) int { return cmp.Compare(a.Name, b.Name) })
	log.Info("mcp server probed", "mcp_server", cfg.Name, "transport", cfg.transport(), "tools_offered", len(info.Tools))
	return info, nil
}

// probeError names the server and the step that failed. ctxErr is the
// probe context's own error. When it says the time limit ran out, the
// message says so, and why a first run can be slow: "npx -y" and "uvx"
// download the server before it starts.
func probeError(name, step string, ctxErr error, limit time.Duration, err error) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return fmt.Errorf("mcp server %q: %s: no answer within %s; a first run may still be downloading the server, so try again",
			name, step, limit)
	}
	return fmt.Errorf("mcp server %q: %s: %w", name, step, err)
}
