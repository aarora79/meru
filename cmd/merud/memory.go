// This file answers the memory ops that `meru memory` and `meru setup user`
// send, and holds the adapter that hands the agent the user's profile.

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/rpc"
)

// memorySource is the source merud writes on a memory the user adds
// through the client. Request.Source can't tell `meru setup user` from
// `meru memory add`: it is a metric attribute with a fixed set of values
// (cli, tui, job). "meru" still marks the memory as one the user typed,
// against "session <id>" for one the model saved with remember.
const memorySource = "meru"

// memoryService answers the memory ops. These are the user's own commands
// (ARCHITECTURE.md, "Memory", "Your commands"), not tool calls the model
// makes, so they reach memory.Store straight from the handler and don't go
// through dispatch. The model's way to save a memory, the remember tool,
// does go through dispatch.
type memoryService struct {
	mem *memory.Store
	log *slog.Logger
}

// handleList answers OpMemoryList with one "memories" event holding every
// memory file. A file it can't read is left out and logged; the rest still
// go back.
func (s memoryService) handleList(emit func(rpc.Event) error) error {
	mems, err := s.mem.List()
	if err != nil {
		if len(mems) == 0 {
			return err
		}
		s.log.Warn("memory: some files couldn't be read", "err", err)
	}
	infos := make([]rpc.MemoryInfo, len(mems))
	for i, m := range mems {
		infos[i] = memoryInfo(m)
	}
	return emit(rpc.Event{Type: rpc.EventMemories, Memories: infos})
}

// handleAdd answers OpMemoryAdd: it saves req.Text as a memory of req.Kind
// and replies with a "memories" event holding the new memory. It fails when
// the kind or the text is bad, with memory.Add's reason.
func (s memoryService) handleAdd(req rpc.Request, emit func(rpc.Event) error) error {
	if req.Kind == "" {
		return errors.New("memory_add needs a kind, such as me or preferences")
	}
	m, err := s.mem.Add(req.Kind, req.Text, memorySource)
	if err != nil {
		return fmt.Errorf("add memory: %w", err)
	}
	return emit(rpc.Event{Type: rpc.EventMemories, Memories: []rpc.MemoryInfo{memoryInfo(m)}})
}

// handleForget answers OpMemoryForget: it deletes the memory req.ID names.
// The reply is "done" alone. It fails when the ID names no memory or isn't
// a memory ID at all, with text that says how to find the right one.
func (s memoryService) handleForget(req rpc.Request) error {
	err := s.mem.Forget(req.ID)
	switch {
	case errors.Is(err, memory.ErrNotFound):
		return fmt.Errorf("no memory %q; meru memory list shows each memory's ID", req.ID)
	case errors.Is(err, memory.ErrBadID):
		return fmt.Errorf("%q isn't a memory ID; IDs look like me/name-amit-arora.md", req.ID)
	case err != nil:
		return fmt.Errorf("forget %s: %w", req.ID, err)
	}
	return nil
}

// counts returns how many memory files there are, and how many sit in the
// profile kinds, for the index status. Both are -1 when the memory folder
// can't be read.
func (s memoryService) counts() (all, profile int) {
	mems, err := s.mem.List()
	if err != nil && len(mems) == 0 {
		s.log.Warn("memory: can't count the memory files", "err", err)
		return -1, -1
	}
	return len(mems), len(profileOnly(mems))
}

// memoryInfo copies a memory into the protocol's shape.
func memoryInfo(m memory.Memory) rpc.MemoryInfo {
	info := rpc.MemoryInfo{ID: m.ID, Kind: m.Kind, Text: m.Text, Source: m.Source}
	if !m.Created.IsZero() {
		info.Created = m.Created.Format("2006-01-02")
	}
	return info
}

// profileOnly returns the memories whose kind is in rpc.ProfileKinds.
func profileOnly(mems []memory.Memory) []memory.Memory {
	kinds := rpc.ProfileKinds()
	var out []memory.Memory
	for _, m := range mems {
		if slices.Contains(kinds, m.Kind) {
			out = append(out, m)
		}
	}
	return out
}

// profileAdapter lets memory.Store serve as the agent's Profile: it reads
// the folders of the profile kinds and no others. The agent sorts and
// formats what it returns.
//
// It reads the files on every turn, with no cache. Measured on an M4 Max,
// 20 profile files take about 0.6 ms. Reading every memory folder instead
// took 13 ms with 500 other memories, which is why it reads only these
// two.
type profileAdapter struct{ mem *memory.Store }

// Profile returns the memories in the profile kinds. When some files can't
// be read, it returns the rest with an error naming the ones it skipped.
func (p profileAdapter) Profile() ([]memory.Memory, error) {
	var out []memory.Memory
	var errs []error
	for _, kind := range rpc.ProfileKinds() {
		mems, err := p.mem.ListKind(kind)
		out = append(out, mems...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}
