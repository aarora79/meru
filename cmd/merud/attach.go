// This file answers OpAttachFile, which copies a file the user attached in
// the desktop app into the uploads folder under [skills] output_dir, so
// read_file can read it. The copy is the user's act, not the model's, so it
// is no tool call and doesn't go through dispatch; the read_file call that
// later reads the copy does, and lands in tool_calls as any other. See
// ARCHITECTURE.md, "Desktop app".

package main

import (
	"context"

	"github.com/aarora79/meru/internal/rpc"
)

// handleAttach answers OpAttachFile: it copies req.Path with the built-in
// tools' Upload and replies with one "saved" event naming the copy. It
// fails with Upload's error, which the app shows the user as it stands.
func (s *toolService) handleAttach(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	saved, err := s.bt.Upload(req.Path)
	if err != nil {
		return err
	}
	// The log line names the copy, never its content.
	s.log.DebugContext(ctx, "attached a file", "path", saved)
	return emit(rpc.Event{Type: rpc.EventSaved, Text: saved})
}
