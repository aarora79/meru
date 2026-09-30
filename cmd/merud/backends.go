// This file connects the MCP client pool to dispatch: mcpBackend lets
// dispatch reach the pool's tools, and mcpServerConfigs turns the
// [[mcp.servers]] entries of config.toml into the pool's settings.
// probeConfig and probeResult do the same for a server the user probes.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/mcp"
	"github.com/aarora79/meru/internal/rpc"
)

// mcpBackend is the dispatch.Backend for the MCP servers in config. The
// pool does the real work: allowlists, timeouts and the one try per turn
// at a server that isn't connected. This type
// only changes the shape of what goes in and out.
type mcpBackend struct {
	pool *mcp.Pool
}

// This line makes the compiler check that mcpBackend has every method of
// dispatch.Backend. The blank name _ throws the value away.
var _ dispatch.Backend = mcpBackend{}

// Kind returns dispatch.KindMCP.
func (b mcpBackend) Kind() string { return dispatch.KindMCP }

// Tools returns the allowed tools of every server, named "<server>.<tool>".
func (b mcpBackend) Tools() []engine.ToolSpec { return b.pool.Tools() }

// Refresh makes mcpBackend a dispatch.Refresher: at the start of a turn
// that offers tools, it lists each connected server's tools again and gives
// each server that isn't connected one try (see mcp.Pool.Refresh).
func (b mcpBackend) Refresh(ctx context.Context) { b.pool.Refresh(ctx) }

// Confirm returns ConfirmAlways for a tool in its server's always_confirm
// list, ConfirmAsk for one in its confirm list, and ConfirmNever for the
// rest.
func (b mcpBackend) Confirm(name string) dispatch.Confirm {
	if b.pool.AlwaysConfirms(name) {
		return dispatch.ConfirmAlways
	}
	if b.pool.NeedsConfirm(name) {
		return dispatch.ConfirmAsk
	}
	return dispatch.ConfirmNever
}

// Locate splits "<server>.<tool>" at the first '.'. A server name can't
// hold a '.', so the rest, dots and all, is the tool's own name.
func (b mcpBackend) Locate(name string) (server, tool string) {
	server, tool, _ = strings.Cut(name, ".")
	return server, tool
}

// Call runs the tool on its server and keeps the text and the error flag.
// The pool's own errors (a timeout, a server that is down) pass through
// for dispatch to sort into outcomes.
func (b mcpBackend) Call(ctx context.Context, name string, args json.RawMessage) (dispatch.Result, error) {
	res, err := b.pool.Call(ctx, name, args)
	if err != nil {
		return dispatch.Result{}, err
	}
	return dispatch.Result{Text: res.Text, IsError: res.IsError}, nil
}

// Status describes each server for `meru tools list`, in config order,
// with its allowed tools and their descriptions.
func (b mcpBackend) Status() []rpc.ServerInfo {
	tools := b.pool.Tools()
	var out []rpc.ServerInfo
	for _, st := range b.pool.Status() {
		info := rpc.ServerInfo{
			Name:      st.Name,
			Kind:      dispatch.KindMCP,
			Transport: st.Transport,
			Connected: st.Connected,
			LastError: st.LastError,
			Offered:   st.Offered,
			Unknown:   st.Unknown,
			Tools:     []rpc.ToolInfo{}, // an empty list, not null, in the JSON
		}
		if st.Managed {
			info.Connector, info.Sentence = st.State, st.Sentence
		}
		for _, t := range st.OfferedTools {
			info.OfferedTools = append(info.OfferedTools, rpc.ToolInfo{Name: st.Name + "." + t.Name, Description: t.Description})
		}
		prefix := st.Name + "."
		for _, t := range tools {
			if strings.HasPrefix(t.Name, prefix) {
				info.Tools = append(info.Tools, rpc.ToolInfo{
					Name:        t.Name,
					Description: t.Description,
					Confirm:     b.pool.NeedsConfirm(t.Name),
					AlwaysAsks:  b.pool.AlwaysConfirms(t.Name),
				})
			}
		}
		out = append(out, info)
	}
	return out
}

// mcpStatus turns the pool's report into the rows `meru mcp` prints. A
// server that isn't connected gets Tools = -1, which the client shows as
// "—": it has no tool list, so any count would be a guess. Allowed and
// Confirm come from config, so they show either way.
func mcpStatus(servers []mcp.ServerStatus) []rpc.MCPStatus {
	out := make([]rpc.MCPStatus, 0, len(servers))
	for _, st := range servers {
		row := rpc.MCPStatus{
			Name:      st.Name,
			Transport: st.Transport,
			State:     rpc.MCPConnected,
			URL:       st.URL,
			Tools:     st.Offered,
			Allowed:   st.Listed,
			Confirm:   st.Confirms,
		}
		if !st.Connected {
			row.State, row.Tools, row.Err = rpc.MCPNotConnected, -1, st.LastError
		}
		if st.Managed {
			// A connector: the supervisor's state and sentence say more
			// than connected or not, such as "starts on first use".
			row.Connector, row.Sentence = st.State, st.Sentence
		}
		out = append(out, row)
	}
	return out
}

// mcpServerConfigs turns the [[mcp.servers]] entries into the pool's
// settings. It parses each timeout and passes every env and header value
// through resolve, which swaps "secret:<name>" for the stored secret and
// leaves other strings alone. Then it checks the lot with mcp.ValidateAll.
//
// It fails, naming the server and the key, when a timeout doesn't parse,
// a secret can't be found, or an entry breaks the pool's rules. It reports
// every problem at once, and never puts a value in an error, since the
// value may be a secret.
func mcpServerConfigs(servers []config.MCPServer, resolve func(string) (string, error)) ([]mcp.ServerConfig, error) {
	var errs []error
	out := make([]mcp.ServerConfig, 0, len(servers))
	for _, s := range servers {
		c := mcp.ServerConfig{
			Name:    s.Name,
			Command: s.Command,
			Args:    s.Args,
			URL:     s.URL,
			Remote:  s.Remote,
			Allow:   s.Allow,
			Confirm: s.Confirm,

			AlwaysConfirm: s.AlwaysConfirm,
		}
		if s.Timeout != "" {
			d, err := time.ParseDuration(s.Timeout)
			if err != nil {
				errs = append(errs, fmt.Errorf("mcp server %q: timeout: %w", s.Name, err))
			}
			c.Timeout = d
		}
		var err error
		if c.Env, err = resolveAll(s.Env, resolve); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: env %w", s.Name, err))
		}
		if c.Headers, err = resolveAll(s.Headers, resolve); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: headers %w", s.Name, err))
		}
		out = append(out, c)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := mcp.ValidateAll(out); err != nil {
		return nil, err
	}
	return out, nil
}

// probeConfig turns the server a client wants to probe into the pool's
// settings, passing each env and header value through resolve as
// mcpServerConfigs does. It leaves the checks to mcp.Probe. It fails,
// naming the key, when a secret can't be found, and never puts a value in
// the error.
func probeConfig(ps rpc.ProbeServer, resolve func(string) (string, error)) (mcp.ServerConfig, error) {
	c := mcp.ServerConfig{
		Name:    ps.Name,
		Command: ps.Command,
		Args:    ps.Args,
		URL:     ps.URL,
		Remote:  ps.Remote,
	}
	var err error
	if c.Env, err = resolveAll(ps.Env, resolve); err != nil {
		return mcp.ServerConfig{}, fmt.Errorf("mcp server %q: env %w", ps.Name, err)
	}
	if c.Headers, err = resolveAll(ps.Headers, resolve); err != nil {
		return mcp.ServerConfig{}, fmt.Errorf("mcp server %q: headers %w", ps.Name, err)
	}
	return c, nil
}

// probeResult copies what mcp.Probe found into the shape the socket
// carries. The two types match field for field; mcp doesn't import rpc, so
// main joins them.
func probeResult(info mcp.ProbeInfo) *rpc.ProbeResult {
	out := &rpc.ProbeResult{
		ServerName:    info.ServerName,
		ServerVersion: info.ServerVersion,
		Tools:         make([]rpc.ProbeTool, 0, len(info.Tools)), // [] rather than null in the JSON
	}
	for _, t := range info.Tools {
		out.Tools = append(out.Tools, rpc.ProbeTool{
			Name:        t.Name,
			Description: t.Description,
			ReadOnly:    t.ReadOnly,
			Destructive: t.Destructive,
		})
	}
	return out
}

// resolveAll returns a copy of m with every value passed through resolve,
// or nil when m is empty. The error names the key whose value failed.
func resolveAll(m map[string]string, resolve func(string) (string, error)) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		r, err := resolve(v)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", k, err)
		}
		out[k] = r
	}
	return out, nil
}
