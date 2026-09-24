// This file answers the memory ops that `meru memory` and `meru setup user`
// send, keeps the store's memories table in step with the memory folder,
// and holds the adapter that hands the agent the user's profile and the
// memories recalled for a question.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// memorySource is the source merud writes on a memory the user adds
// through the client. Request.Source can't tell `meru setup user` from
// `meru memory add`: it is a metric attribute with a fixed set of values
// (cli, tui, job). "meru" still marks the memory as one the user typed,
// against "session <id>" for one the model saved with remember.
const memorySource = "meru"

// recallN is how many memories recall puts in a prompt, at most. Five
// short facts fit well inside the section's 600-token cap and leave the
// small model room for the question.
const recallN = 5

// memoryService answers the memory ops. These are the user's own commands
// (ARCHITECTURE.md, "Memory", "Your commands"), not tool calls the model
// makes, so they reach memory.Store straight from the handler and don't go
// through dispatch. The model's way to save a memory, the remember tool,
// does go through dispatch.
//
// It also owns the syncer that copies the memory folder into the store:
// once at start, after each add or forget, after each remember (through
// syncNow, which the tool service passes to the built-in tools), and on
// each hand edit the watcher sees.
type memoryService struct {
	mem  *memory.Store
	sync *index.Memories
	log  *slog.Logger
}

// syncNow syncs the memory folder into the store, so the next turn can
// recall a memory added or forgotten a moment ago. A failed sync only logs
// a warning: the file already holds the change, and the watcher, the next
// sync or the next start copies it over.
func (s memoryService) syncNow(ctx context.Context) {
	if _, err := s.sync.Sync(ctx); err != nil {
		s.log.WarnContext(ctx, "memory: sync after a change failed; the next sync catches up", "err", err)
	}
}

// watch syncs the memory folder once, then again after each change, until
// ctx ends. When the OS can't watch the folder, it logs why and returns;
// adds and forgets through merud still sync, and the next start catches
// hand edits.
func (s memoryService) watch(ctx context.Context) {
	if err := s.sync.Watch(ctx); err != nil {
		s.log.Warn("memory: can't watch the memory folder; hand edits show up after a restart", "err", err)
	}
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

// handleAdd answers OpMemoryAdd: it saves req.Text as a memory of req.Kind,
// syncs it into the store, and replies with a "memories" event holding the
// new memory. It fails when the kind or the text is bad, with memory.Add's
// reason.
func (s memoryService) handleAdd(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	if req.Kind == "" {
		return errors.New("memory_add needs a kind, such as me or preferences")
	}
	m, err := s.mem.Add(req.Kind, req.Text, memorySource)
	if err != nil {
		return fmt.Errorf("add memory: %w", err)
	}
	s.syncNow(ctx)
	return emit(rpc.Event{Type: rpc.EventMemories, Memories: []rpc.MemoryInfo{memoryInfo(m)}})
}

// handleForget answers OpMemoryForget: it deletes the memory req.ID names
// and syncs the store, so recall forgets it too. The reply is "done" alone.
// It fails when the ID names no memory or isn't a memory ID at all, with
// text that says how to find the right one.
func (s memoryService) handleForget(ctx context.Context, req rpc.Request) error {
	err := s.mem.Forget(req.ID)
	switch {
	case errors.Is(err, memory.ErrNotFound):
		return fmt.Errorf("no memory %q; meru memory list shows each memory's ID", req.ID)
	case errors.Is(err, memory.ErrBadID):
		return fmt.Errorf("%q isn't a memory ID; IDs look like me/name-amit-arora.md", req.ID)
	case err != nil:
		return fmt.Errorf("forget %s: %w", req.ID, err)
	}
	s.syncNow(ctx)
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

// profileAdapter serves as the agent's Profile. For the profile it reads
// the folders of the profile kinds from memory.Store and no others; the
// agent sorts and formats what it returns. For recall it searches the
// store's memories table, which the syncer keeps in step with the files.
//
// It reads the profile files on every turn, with no cache. Measured on an
// M4 Max, 20 profile files take about 0.6 ms. Reading every memory folder
// instead took 13 ms with 500 other memories, which is why it reads only
// these two.
type profileAdapter struct {
	mem *memory.Store
	st  *store.Store
	eng engine.Engine
}

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

// Recall returns up to recallN memories outside the profile kinds that fit
// query, by meaning, keyword and recency (retrieve.SearchMemories). The
// profile kinds stay out because every prompt holds them already.
func (p profileAdapter) Recall(ctx context.Context, query string) ([]retrieve.Memory, error) {
	return retrieve.SearchMemories(ctx, p.st, p.eng, query, rpc.ProfileKinds(), recallN)
}
