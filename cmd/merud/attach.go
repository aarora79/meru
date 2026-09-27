// This file answers OpAttachFile, which copies a file the user attached in
// the desktop app into the uploads folder under [skills] output_dir, so
// read_file can read it, or, for an image, so a question can carry it. The
// copy is the user's act, not the model's, so it is no tool call and
// doesn't go through dispatch; the read_file call that later reads a copy
// does, and lands in tool_calls as any other. It also holds capabilityCheck,
// which tells the agent whether a model can look at images or call tools. See
// ARCHITECTURE.md, "Desktop app".

package main

import (
	"context"
	"slices"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// handleAttach answers OpAttachFile: it copies req.Path with the built-in
// tools' Upload and replies with one "saved" event naming the copy and
// its kind, rpc.AttachImage or rpc.AttachFile. It fails with Upload's
// error, which the app shows the user as it stands.
func (s *toolService) handleAttach(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	up, err := s.bt.Upload(req.Path)
	if err != nil {
		return err
	}
	// The log line names the copy, never its content.
	s.log.DebugContext(ctx, "attached a file", "path", up.Path, "kind", up.Kind)
	return emit(rpc.Event{Type: rpc.EventSaved, Text: up.Path, Kind: up.Kind})
}

// capabilityCheck returns a check of whether a model lists capability,
// such as engine.Vision or engine.ToolUse, among its capabilities. The
// agent runs the vision check before it sends the main model a question's
// images, and the tools check before it offers the main model tools. Only
// OllamaEngine can say, through /api/show, so for any other engine, as in
// tests, capabilityCheck returns nil: the agent then refuses questions
// with images, and offers tools without a check.
//
// eng.(*engine.OllamaEngine) is a type assertion: it asks whether the
// interface value eng holds that concrete type, and ok says whether it
// does. Capabilities sits outside the Engine interface on purpose, which
// keeps that interface at four methods (ARCHITECTURE.md, "Engine layer").
func capabilityCheck(eng engine.Engine, capability string) func(ctx context.Context, model string) (bool, error) {
	oe, ok := eng.(*engine.OllamaEngine)
	if !ok {
		return nil
	}
	return func(ctx context.Context, model string) (bool, error) {
		caps, err := oe.Capabilities(ctx, model)
		if err != nil {
			return false, err
		}
		return slices.Contains(caps, capability), nil
	}
}
