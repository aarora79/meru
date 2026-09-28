// This file answers the ops that organize the chat list: session_delete,
// session_move and session_tag on one chat, and the chat folder ops. Each
// change lands in the files first, the transcript's meta line or the
// folder list, and meru.db follows. ARCHITECTURE.md, "Organizing past
// chats", says what each op does.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// handleDelete answers OpSessionDelete. For an incognito chat it makes the
// agent forget the history it holds; there is nothing else to delete. For
// any other chat it deletes the transcript, then the chat's content in
// meru.db; its tool_calls rows stay, without arguments or results (see
// store.DeleteSession). A file already gone still loses its rows, so a
// delete that stopped half way can run again. It fails when id isn't a
// session ID, or the file or the rows can't be changed.
func (h historyService) handleDelete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("session_delete needs a session ID")
	}
	if transcript.IsIncognito(id) {
		if h.forget != nil {
			h.forget(id)
		}
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// transcript.Delete checks the ID's shape, so a client can't name a
	// file outside the sessions folder.
	if err := transcript.Delete(h.dir, id); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if h.st != nil {
		if err := h.st.DeleteSession(ctx, id); err != nil {
			return err
		}
	}
	h.logger().InfoContext(ctx, "chat deleted", "session", id)
	return nil
}

// handleMove answers OpSessionMove: it puts the chat req.Session names in
// the folder req.Text names, or in none when Text is empty, adds a new
// folder to the list, and answers with the chat's row. It fails when the
// session doesn't exist, the name isn't a valid folder name, or a file
// can't be written.
func (h historyService) handleMove(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	folder := ""
	if req.Text != "" {
		var err error
		if folder, err = transcript.CleanFolder(req.Text); err != nil {
			return err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if folder != "" {
		if err := h.addFolder(folder); err != nil {
			return err
		}
	}
	return h.changeMeta(ctx, req.Session, emit, func(m *transcript.Meta) error {
		m.Folder = folder
		return nil
	})
}

// handleTag answers OpSessionTag: it adds req.Tags.Add to the chat's tags,
// takes out req.Tags.Remove, and answers with the chat's row. It fails
// when the request has no tags, a tag isn't valid, the chat would pass
// transcript.MaxTags, or a file can't be written.
func (h historyService) handleTag(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	if req.Tags == nil || len(req.Tags.Add)+len(req.Tags.Remove) == 0 {
		return errors.New("session_tag needs a tag to add or remove")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changeMeta(ctx, req.Session, emit, func(m *transcript.Meta) error {
		tags, err := transcript.WithTags(m.Tags, req.Tags.Add, req.Tags.Remove)
		m.Tags = tags
		return err
	})
}

// changeMeta opens session id, lets change edit its folder and tags,
// appends the result as a meta line, replays the session into meru.db so
// search sees new tags at once, and emits the chat's row. The caller holds
// h.mu. It fails when id isn't a chat on disk, change fails, or a file
// can't be read or written.
func (h historyService) changeMeta(ctx context.Context, id string, emit func(rpc.Event) error, change func(*transcript.Meta) error) error {
	if transcript.IsIncognito(id) {
		return errors.New("an incognito chat has no folder or tags")
	}
	sess, err := transcript.Open(h.dir, id)
	if err != nil {
		return err
	}
	meta, err := sess.Meta()
	if err != nil {
		return err
	}
	if err := change(&meta); err != nil {
		return err
	}
	if err := sess.SetMeta(meta); err != nil {
		return err
	}
	if h.st != nil {
		// A failed replay costs search the new tags until the next one; the
		// meta line is safe in the transcript.
		if _, err := h.st.ReplaySession(ctx, h.dir, id); err != nil {
			h.logger().WarnContext(ctx, "replay the session after its tags changed", "session", id, "err", err)
		}
	}
	info, err := sess.Info()
	if err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventSessions, Sessions: []rpc.SessionInfo{sessionInfo(info)}})
}

// handleChatFolders answers OpChatFolders with the folder list.
func (h historyService) handleChatFolders(emit func(rpc.Event) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.emitFolders(emit)
}

// handleChatFolderAdd answers OpChatFolderAdd: it adds the folder req.ID
// names to the list, unless it is there already, and answers with the
// list. It fails when the name isn't valid or the list can't be written.
func (h historyService) handleChatFolderAdd(req rpc.Request, emit func(rpc.Event) error) error {
	name, err := transcript.CleanFolder(req.ID)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.addFolder(name); err != nil {
		return err
	}
	return h.emitFolders(emit)
}

// handleChatFolderRename answers OpChatFolderRename: it renames the folder
// req.ID to req.Text in the list and in each chat that sits in it, and
// answers with the list. Each chat gets a new meta line, so its transcript
// says where it is. It fails when either name isn't valid, the new name is
// taken, the old one is in no list and no chat, or a file can't be
// written.
func (h historyService) handleChatFolderRename(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	from, err := transcript.CleanFolder(req.ID)
	if err != nil {
		return err
	}
	to, err := transcript.CleanFolder(req.Text)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	names, err := transcript.LoadFolders(h.dir)
	if err != nil {
		return err
	}
	if from != to && slices.Contains(names, to) {
		return fmt.Errorf("a folder called %q exists already", to)
	}
	moved, err := h.refile(from, to)
	if err != nil {
		return err
	}
	i := slices.Index(names, from)
	switch {
	case i >= 0:
		names[i] = to
	case moved > 0:
		names = append(names, to) // the chats named a folder the list had lost
	default:
		return fmt.Errorf("there is no folder called %q", from)
	}
	if err := transcript.SaveFolders(h.dir, names); err != nil {
		return err
	}
	h.logger().InfoContext(ctx, "chat folder renamed", "chats", moved)
	return h.emitFolders(emit)
}

// handleChatFolderRemove answers OpChatFolderRemove: it takes the folder
// req.ID names out of the list and moves each chat in it back to the main
// list, deleting none, and answers with the list. It fails when the name
// isn't valid or a file can't be written.
func (h historyService) handleChatFolderRemove(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	name, err := transcript.CleanFolder(req.ID)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	moved, err := h.refile(name, "")
	if err != nil {
		return err
	}
	names, err := transcript.LoadFolders(h.dir)
	if err != nil {
		return err
	}
	if err := transcript.SaveFolders(h.dir, slices.DeleteFunc(names, func(n string) bool { return n == name })); err != nil {
		return err
	}
	h.logger().InfoContext(ctx, "chat folder removed", "chats", moved)
	return h.emitFolders(emit)
}

// refile moves every chat in the folder from to the folder to ("" for
// none), one meta line each, and returns how many it moved. The caller
// holds h.mu. It reads every transcript, which is fine for an op the user
// runs now and then. It fails when a transcript can't be read or written.
func (h historyService) refile(from, to string) (int, error) {
	infos, err := transcript.List(h.dir, 0)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, info := range infos {
		if info.Folder != from {
			continue
		}
		sess, err := transcript.Open(h.dir, info.ID)
		if err != nil {
			return moved, err
		}
		if err := sess.SetMeta(transcript.Meta{Folder: to, Tags: info.Tags}); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// addFolder adds name to the folder list unless it is there already. The
// caller holds h.mu.
func (h historyService) addFolder(name string) error {
	names, err := transcript.LoadFolders(h.dir)
	if err != nil {
		return err
	}
	if slices.Contains(names, name) {
		return nil
	}
	return transcript.SaveFolders(h.dir, append(names, name))
}

// emitFolders sends the folder list in a "chat_folders" event. The caller
// holds h.mu. An empty list goes out as an empty event, which the clients
// read as no folders.
func (h historyService) emitFolders(emit func(rpc.Event) error) error {
	names, err := transcript.LoadFolders(h.dir)
	if err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventChatFolders, ChatFolders: names})
}

// sessionInfo turns a transcript.Info into the row the session ops send,
// with times in RFC 3339, in UTC, as the transcripts store them.
func sessionInfo(s transcript.Info) rpc.SessionInfo {
	return rpc.SessionInfo{
		ID:      s.ID,
		Title:   s.Title,
		Started: s.Started.UTC().Format(time.RFC3339),
		Updated: s.Updated.UTC().Format(time.RFC3339),
		Turns:   s.Turns,
		Folder:  s.Folder,
		Tags:    s.Tags,
	}
}
