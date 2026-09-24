// Package a2a is Meru's client for other agents, over A2A (the
// Agent2Agent protocol). See ARCHITECTURE.md, "Other agents (A2A)".
//
// A Client holds one record per [[a2a.agents]] entry. It reads each agent's
// card, the JSON file where an agent lists its skills and the URLs it
// answers on, and turns each skill the entry's allow list names into a
// tool called "a2a.<agent>.<skill>". The tool takes one argument, a message;
// a call sends it to the agent and returns the agent's answer as text.
//
// It uses the A2A project's Go SDK (github.com/a2aproject/a2a-go/v2), which
// speaks version 1.0 of the protocol over JSON-RPC or REST. The Client
// implements dispatch.Backend, and dispatch is its only caller: dispatch
// checks the allow list, asks the user when a skill needs a yes, and writes
// the tool_calls row and the transcript lines (AGENTS.md, non-negotiable 4).
//
// The Client reaches only loopback addresses unless an entry says
// remote = true. It checks the configured URL, and its dialer refuses any
// other address at connect time, so a card can't point the calls elsewhere.
//
// What the package leaves out: it serves no A2A of its own, sends no push
// notification settings, speaks no gRPC and no pre-1.0 protocol, and ends a
// call when the agent asks for more input instead of carrying on the task.
package a2a
