// This file holds Set, the dispatch.Backend for the declared commands: the
// tool specs the model sees, the confirm rule, the call itself, the argv
// dispatch records, and the listing `meru tools` shows.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// server is what Locate and Status report as the server of every command:
// merud runs them itself, as it does the built-in tools.
const server = "meru"

// sourceName names the commands' block in `meru tools`.
const sourceName = "commands"

// Set is the checked [[commands]] entries, in config order. Build it with
// New. It never changes after New, so its methods need no lock.
type Set struct {
	cmds   []Command
	byTool map[string]int // tool name → index in cmds
}

// These lines make the compiler check that *Set has every method of
// dispatch.Backend and dispatch.Auditor.
var (
	_ dispatch.Backend = (*Set)(nil)
	_ dispatch.Auditor = (*Set)(nil)
)

// newSet indexes cmds by tool name.
func newSet(cmds []Command) *Set {
	s := &Set{cmds: cmds, byTool: map[string]int{}}
	for i, c := range cmds {
		s.byTool[c.ToolName()] = i
	}
	return s
}

// Len returns how many commands the set holds.
func (s *Set) Len() int { return len(s.cmds) }

// lookup returns the command behind a tool name, and false for a name the
// set doesn't hold.
func (s *Set) lookup(name string) (Command, bool) {
	i, ok := s.byTool[name]
	if !ok {
		return Command{}, false
	}
	return s.cmds[i], true
}

// Kind returns dispatch.KindCommand.
func (s *Set) Kind() string { return dispatch.KindCommand }

// Tools returns one spec per command, named "cmd.<name>". The description
// ends with the argv template, so the model knows what the tool runs.
func (s *Set) Tools() []engine.ToolSpec {
	specs := make([]engine.ToolSpec, 0, len(s.cmds))
	for _, c := range s.cmds {
		specs = append(specs, engine.ToolSpec{
			Name:        c.ToolName(),
			Description: c.toolDescription(),
			Parameters:  c.schema(),
		})
	}
	return specs
}

// Confirm returns ConfirmAsk for a command with confirm = true and
// ConfirmNever for the rest. No command asks every time: a command runs
// one fixed program, so approving it for the session approves only that.
func (s *Set) Confirm(name string) dispatch.Confirm {
	if c, ok := s.lookup(name); ok && c.Confirm {
		return dispatch.ConfirmAsk
	}
	return dispatch.ConfirmNever
}

// Locate returns ("meru", name): merud runs the program itself, so rows
// and metrics name merud as the server and keep the "cmd." name whole.
func (s *Set) Locate(name string) (string, string) { return server, name }

// Call renders the model's arguments into the command's argv, runs it and
// returns the result as text. A bad argument, and a program that exits
// with a non-zero code, come back as a Result the model reads; the first
// with IsError set. err covers a name the set doesn't hold, a program that
// won't start, the timeout and a cancelled turn, which dispatch sorts into
// outcomes.
//
// Call puts the exit code and the truncation flag on the meru.dispatch
// span, which ctx carries. The argv goes on the span only as the call's
// arguments, and only when capture_content is on: dispatch does that.
func (s *Set) Call(ctx context.Context, name string, raw json.RawMessage) (dispatch.Result, error) {
	c, ok := s.lookup(name)
	if !ok {
		return dispatch.Result{}, fmt.Errorf("%q is not a declared command", name)
	}
	args, err := ArgsFromJSON(raw)
	if err != nil {
		return dispatch.Result{IsError: true, Text: fmt.Sprintf("%s: %v. It didn't run.", name, err)}, nil
	}
	argv, err := c.Render(args)
	if err != nil {
		return dispatch.Result{IsError: true, Text: err.Error() + ". It didn't run."}, nil
	}
	res, err := Run(ctx, c, argv)
	if err != nil {
		return dispatch.Result{}, err
	}
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.Int("meru.command.exit_code", res.ExitCode),
		attribute.Bool("meru.command.truncated", res.Truncated),
	)
	return dispatch.Result{Text: res.Text()}, nil
}

// audit is what AuditArgs returns: the argv that will run, then the
// model's arguments. The argv comes first so a log line cut short still
// shows the program.
type audit struct {
	Argv   []string          `json:"argv"`
	Params map[string]string `json:"params,omitempty"`
}

// AuditArgs returns the argv the call will run, with the model's
// arguments, as {"argv":[...],"params":{...}}. dispatch records it in place
// of the model's arguments. It returns nil when the arguments don't render;
// Call then fails with the reason, and dispatch records what the model
// sent.
//
// Call renders again when it runs. Both renders read the same arguments and
// happen a moment apart, so they give the same argv unless a path changes
// in between.
func (s *Set) AuditArgs(name string, raw json.RawMessage) json.RawMessage {
	c, ok := s.lookup(name)
	if !ok {
		return nil
	}
	args, err := ArgsFromJSON(raw)
	if err != nil {
		return nil
	}
	argv, err := c.Render(args)
	if err != nil {
		return nil
	}
	b, err := json.Marshal(audit{Argv: argv, Params: args})
	if err != nil {
		return nil
	}
	return b
}

// Status describes the commands for `meru tools`, as one source named
// "commands", with each command's argv template. With no commands it
// returns nil, so `meru tools` shows no empty block.
func (s *Set) Status() []rpc.ServerInfo {
	if len(s.cmds) == 0 {
		return nil
	}
	tools := make([]rpc.ToolInfo, 0, len(s.cmds))
	for _, c := range s.cmds {
		tools = append(tools, rpc.ToolInfo{
			Name:        c.ToolName(),
			Description: c.Description,
			Confirm:     c.Confirm,
			Argv:        slices.Clone(c.Argv),
		})
	}
	return []rpc.ServerInfo{{
		Name:      sourceName,
		Kind:      dispatch.KindCommand,
		Connected: true,
		Tools:     tools,
		Offered:   len(tools),
	}}
}

// toolDescription is the description the model reads: the user's own, then
// the program the tool runs, placeholders and all.
func (c Command) toolDescription() string {
	d := c.Description
	if d == "" {
		d = "Runs a program the user declared."
	}
	if !strings.HasSuffix(d, ".") {
		d += "."
	}
	d += " It runs: " + rpc.ArgvLine(c.Argv) + "."
	if len(c.Params) > 0 {
		d += " Each parameter fills its {placeholder}."
	}
	return d + " The result gives the exit code, stdout and stderr."
}

// schema returns the JSON Schema for the command's arguments: one property
// per parameter, all required, nothing else allowed. json.Marshal can't
// fail on these plain values, so schema drops its error.
func (c Command) schema() json.RawMessage {
	props := map[string]any{}
	required := []string{}
	for name, p := range c.Params {
		prop := map[string]any{"type": "string"}
		desc := p.Description
		switch p.Type {
		case TypeInt:
			prop["type"] = "integer"
			if p.Min != nil {
				prop["minimum"] = *p.Min
			}
			if p.Max != nil {
				prop["maximum"] = *p.Max
			}
		case TypeEnum:
			prop["enum"] = p.Values
		case TypePath:
			desc = strings.TrimSpace(desc + " A path inside " + p.Under +
				"; a relative path starts there.")
		}
		if desc != "" {
			prop["description"] = desc
		}
		props[name] = prop
		required = append(required, name)
	}
	slices.Sort(required)
	b, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	})
	return b
}
