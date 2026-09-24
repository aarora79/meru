// This file holds `meru memory list | add | forget`, and the three calls to
// merud that it and `meru setup user` share. merud owns the memory folder
// and writes the files; this side sends requests and formats the replies.
// See ARCHITECTURE.md, "Memory".

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// errMemoryUsage is the error `meru memory` returns on bad usage.
var errMemoryUsage = errors.New("usage: meru memory list [kind] | meru memory add <kind> <text...> | meru memory forget <id>")

// memoryCmd runs `meru memory ...`. args are the words after "memory". It
// fails on bad usage, when merud can't be reached, or when merud replies
// with an error.
func memoryCmd(ctx context.Context, socket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errMemoryUsage
	}
	switch {
	case args[0] == "list" && len(args) <= 2:
		mems, err := listMemories(ctx, socket)
		if err != nil {
			return err
		}
		if len(args) == 2 {
			mems = ofKinds(mems, args[1])
		}
		_, err = io.WriteString(stdout, memoriesText(mems, args[1:], newLook(stdout)))
		return err
	case args[0] == "add" && len(args) >= 3:
		mem, err := addMemory(ctx, socket, args[1], strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved %s\n", mem.ID)
		return nil
	case args[0] == "forget" && len(args) == 2:
		return forgetCmd(ctx, socket, args[1], stdout)
	}
	return errMemoryUsage
}

// forgetCmd deletes the memory named id and says what it held. It looks
// the memory up first, because merud's reply to a forget carries no text.
// It fails when no memory has that ID.
func forgetCmd(ctx context.Context, socket, id string, stdout io.Writer) error {
	mems, err := listMemories(ctx, socket)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(mems, func(m rpc.MemoryInfo) bool { return m.ID == id })
	if i < 0 {
		return fmt.Errorf("no memory has the ID %q; `meru memory list` shows them", id)
	}
	if err := forgetMemory(ctx, socket, id); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Forgot %s: %s\n", id, oneLine(mems[i].Text))
	return nil
}

// memoriesText writes the memories grouped by kind, with the ID, the text,
// and the date and source dim at the end:
//
//	me
//	  me/name-dana-reyes.md  Name: Dana Reyes  2026-09-24 · meru setup user
//
// The profile kinds come first, since every prompt carries them, then the
// rest in alphabetical order. kinds holds the kind the user asked for, if
// any, so an empty answer can say what it looked for.
func memoriesText(mems []rpc.MemoryInfo, kinds []string, lk look) string {
	if len(mems) == 0 {
		if len(kinds) > 0 {
			return fmt.Sprintf("No memories of kind %q.\n", kinds[0])
		}
		return "Meru has no memories yet. Run `meru setup user` to tell it about you.\n"
	}
	idWidth := 0
	for _, m := range mems {
		idWidth = max(idWidth, len([]rune(m.ID)))
	}
	var b strings.Builder
	for i, kind := range kindOrder(mems) {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(lk.bold.Render(kind) + "\n")
		for _, m := range mems {
			if m.Kind != kind {
				continue
			}
			// %-*s pads the ID with spaces to idWidth columns; the * takes
			// the width from the argument before the ID.
			line := fmt.Sprintf("  %-*s  %s", idWidth, m.ID, oneLine(m.Text))
			if about := memoryAbout(m); about != "" {
				line += "  " + lk.dim.Render(about)
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// kindOrder returns the kinds that mems hold: the profile kinds first, in
// rpc.ProfileKinds order, then the others sorted.
func kindOrder(mems []rpc.MemoryInfo) []string {
	var profile, other []string
	for _, m := range mems {
		switch {
		case slices.Contains(profile, m.Kind) || slices.Contains(other, m.Kind):
			// A kind already listed: nothing to add.
		case slices.Contains(rpc.ProfileKinds(), m.Kind):
			profile = append(profile, m.Kind)
		default:
			other = append(other, m.Kind)
		}
	}
	slices.SortFunc(profile, func(a, b string) int {
		return slices.Index(rpc.ProfileKinds(), a) - slices.Index(rpc.ProfileKinds(), b)
	})
	slices.Sort(other)
	return append(profile, other...)
}

// memoryAbout joins a memory's date and source with " · ", leaving out
// either when merud sent none.
func memoryAbout(m rpc.MemoryInfo) string {
	var parts []string
	for _, p := range []string{m.Created, m.Source} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// oneLine folds a memory's text onto one line, so a memory someone wrote
// over several lines keeps the listing in columns.
func oneLine(s string) string {
	// strings.Fields splits on any run of spaces and new lines.
	return strings.Join(strings.Fields(s), " ")
}

// ofKinds returns the memories whose kind is one of kinds.
func ofKinds(mems []rpc.MemoryInfo, kinds ...string) []rpc.MemoryInfo {
	var out []rpc.MemoryInfo
	for _, m := range mems {
		if slices.Contains(kinds, m.Kind) {
			out = append(out, m)
		}
	}
	return out
}

// listMemories asks merud for every memory. It fails when merud can't be
// reached or replies with an error, as a merud older than the memory ops
// does.
func listMemories(ctx context.Context, socket string) ([]rpc.MemoryInfo, error) {
	return memoryCall(ctx, socket, rpc.Request{Op: rpc.OpMemoryList})
}

// addMemory asks merud to save text as a memory of kind, and returns the
// memory merud wrote. It fails as listMemories does, and when merud's reply
// holds no memory.
func addMemory(ctx context.Context, socket, kind, text string) (rpc.MemoryInfo, error) {
	mems, err := memoryCall(ctx, socket, rpc.Request{Op: rpc.OpMemoryAdd, Kind: kind, Text: text, Source: rpc.SourceCLI})
	if err != nil {
		return rpc.MemoryInfo{}, err
	}
	if len(mems) == 0 {
		return rpc.MemoryInfo{}, errors.New("merud saved the memory but didn't say where")
	}
	return mems[0], nil
}

// forgetMemory asks merud to delete the memory named id.
func forgetMemory(ctx context.Context, socket, id string) error {
	_, err := memoryCall(ctx, socket, rpc.Request{Op: rpc.OpMemoryForget, ID: id})
	return err
}

// memoryCall sends one memory request and returns the memories in merud's
// "memories" event, if it sent one.
func memoryCall(ctx context.Context, socket string, req rpc.Request) ([]rpc.MemoryInfo, error) {
	var mems []rpc.MemoryInfo
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return nil, err
		}
		switch ev.Type {
		case rpc.EventMemories:
			mems = ev.Memories
		case rpc.EventError:
			return nil, errors.New(ev.Error)
		}
	}
	return mems, nil
}
