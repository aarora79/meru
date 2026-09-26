// This file answers the desktop app's folder ops: the [index] folders with
// their file counts and the usual folders worth offering (OpFolders), and
// adding or removing a folder (OpFolderAdd, OpFolderRemove). merud writes
// the change to config.toml, hands the indexer its new folders, and starts
// the watcher again with a scan, all while it keeps answering questions.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// suggestedFolders are the folders the app offers on its first run, when
// they exist and aren't indexed yet: where most people keep documents and
// notes. They are written as config.toml writes them.
var suggestedFolders = []string{"~/Documents", "~/Notes", "~/Desktop"}

// countLimit caps the files CountFiles counts in a suggested folder, and
// countTime the time it may take. A folder with more is "2,000+": the
// number is there to say how big it is, not to be exact, and the whole
// reply must come back while the user waits.
const (
	countLimit = 2000
	countTime  = 3 * time.Second
)

// handleFolders answers OpFolders with one "folders" event.
func (s *indexService) handleFolders(ctx context.Context, emit func(rpc.Event) error) error {
	ev, err := s.foldersEvent(ctx)
	if err != nil {
		return err
	}
	return emit(ev)
}

// foldersEvent builds the "folders" event: each [index] folder with how
// many files the index holds from it, then each suggested folder that
// exists, isn't indexed, and doesn't sit inside or around an indexed one,
// with how many files the indexer would read in it. It fails when the
// store can't count.
func (s *indexService) foldersEvent(ctx context.Context) (rpc.Event, error) {
	ev := rpc.Event{Type: rpc.EventFolders, Folders: []rpc.FolderInfo{}}
	var indexed []string // the [index] folders, expanded
	for _, f := range s.currentFolders() {
		info := rpc.FolderInfo{Path: f}
		abs, err := expandHome(f)
		if err != nil {
			ev.Folders = append(ev.Folders, info)
			continue
		}
		indexed = append(indexed, abs)
		// The store keys files by their path with symlinks resolved.
		real, err := filepath.EvalSymlinks(abs)
		if err == nil {
			info.Exists = true
			if info.Files, err = s.st.CountPaths(ctx, real); err != nil {
				return rpc.Event{}, err
			}
		}
		ev.Folders = append(ev.Folders, info)
	}

	cctx, cancel := context.WithTimeout(ctx, countTime)
	defer cancel()
	for _, f := range suggestedFolders {
		abs, err := expandHome(f)
		if err != nil || overlaps(abs, indexed) {
			continue
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			continue
		}
		info := rpc.FolderInfo{Path: f, Exists: true}
		info.Files, info.More, err = index.CountFiles(cctx, s.cfg, abs, countLimit)
		if errors.Is(err, context.DeadlineExceeded) {
			info.More, err = true, nil // it took too long to count: a big folder
		}
		if err != nil {
			s.log.DebugContext(ctx, "count a suggested folder", "folder", f, "err", err)
			continue
		}
		ev.Suggested = append(ev.Suggested, info)
	}
	return ev, nil
}

// overlaps reports whether dir is one of folders, sits inside one, or
// holds one. Indexing it would index files twice, or not at all.
func overlaps(dir string, folders []string) bool {
	for _, f := range folders {
		if dir == f || within(f, dir) || within(dir, f) {
			return true
		}
	}
	return false
}

// within reports whether p sits inside dir.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// handleFolderAdd answers OpFolderAdd: it checks req.Path, writes the new
// [index] folders to config.toml, and tells the indexer. A scan of the new
// folder starts at once in the background, beside the watcher; the reply,
// one "folders" event, doesn't wait for it. The index status op reports
// the scan.
//
// It refuses a path that isn't absolute or "~/...", doesn't exist, isn't a
// folder, is the file system's root or the home folder itself (too much to
// index, and full of files that aren't the user's writing), lies inside
// the Meru home, or overlaps a folder already indexed.
func (s *indexService) handleFolderAdd(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	abs, err := expandHome(strings.TrimSpace(req.Path))
	if err != nil {
		return err
	}
	if !filepath.IsAbs(abs) {
		return fmt.Errorf("%q isn't a full path", req.Path)
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s doesn't exist", abs)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s isn't a folder", abs)
	}
	home, _ := os.UserHomeDir()
	switch {
	case abs == filepath.Dir(abs): // "/" is its own parent
		return errors.New("that is the whole disk, which Meru won't index; pick a folder inside it")
	case home != "" && abs == home:
		return errors.New("that is the whole home folder, which Meru won't index; pick the folders in it that hold your documents and notes")
	case within(filepath.Dir(s.configPath), abs) || abs == filepath.Dir(s.configPath):
		return errors.New("that folder is Meru's own home; pick one of your folders")
	}
	err = s.changeFolders(func(cur []string) ([]string, error) {
		var expanded []string
		for _, f := range cur {
			if e, err := expandHome(f); err == nil {
				expanded = append(expanded, e)
			}
		}
		if overlaps(abs, expanded) {
			return nil, fmt.Errorf("%s is already indexed, or sits inside or around a folder that is", abs)
		}
		return append(cur, configForm(abs, home)), nil
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "index folder added", "folders", len(s.currentFolders()))
	return s.handleFolders(ctx, emit)
}

// handleFolderRemove answers OpFolderRemove: it takes req.Path, as the
// "folders" event wrote it, out of [index] folders and tells the indexer.
// The scan that follows drops the folder's files from the index. It fails
// when no [index] folder is req.Path.
func (s *indexService) handleFolderRemove(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	err := s.changeFolders(func(cur []string) ([]string, error) {
		i := slices.Index(cur, req.Path)
		if i < 0 {
			return nil, fmt.Errorf("%q isn't one of the [index] folders", req.Path)
		}
		return slices.Delete(cur, i, i+1), nil
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "index folder removed", "folders", len(s.currentFolders()))
	return s.handleFolders(ctx, emit)
}

// changeFolders runs change on a copy of the [index] folders, writes the
// list it returns to config.toml, checks that the file loads with exactly
// that list, hands the list to the indexer and to the agent (through
// currentFolders), and wakes watchAndRescan. change runs under merud's
// config lock, so two changes at once can't both start from the same list
// and lose one. It fails, changing nothing, when change fails or
// config.toml can't be written.
func (s *indexService) changeFolders(change func(cur []string) ([]string, error)) error {
	err := s.editConfig(func() error {
		folders, err := change(s.currentFolders())
		if err != nil {
			return err
		}
		check := func(next config.Config) error {
			if !slices.Equal(next.Index.Folders, folders) {
				return errors.New("[index] folders didn't come out as asked; edit config.toml by hand")
			}
			return nil
		}
		if err := catalog.SetTableLists(s.configPath, "index", map[string][]string{"folders": folders}, check); err != nil {
			return err
		}
		if err := s.ix.SetFolders(folders); err != nil {
			return err
		}
		s.foldersMu.Lock()
		s.folders = folders
		s.foldersMu.Unlock()
		return nil
	})
	if err != nil {
		return err
	}
	// A send on a full channel would wait; select with default drops the
	// signal instead, since one is already waiting.
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return nil
}

// configForm writes abs the way config.toml writes a folder: "~/Notes"
// under the home folder, the full path elsewhere. It uses "/" after the
// "~" on every system, as the config template does.
func configForm(abs, home string) string {
	if home != "" && within(home, abs) {
		rel, _ := filepath.Rel(home, abs)
		return "~/" + filepath.ToSlash(rel)
	}
	return abs
}

// watchAndRescan runs the file watcher until ctx ends, and each time the
// folders change it starts the watcher again over the new folders and
// scans them. The scan adds a new folder's files and drops a removed
// folder's (index.Indexer.Scan prunes them).
//
// The watcher runs in a goroutine of its own, which this function owns:
// stop cancels it and waits on its done channel, so no watcher outlives
// the loop.
func (s *indexService) watchAndRescan(ctx context.Context) {
	var stop func()
	start := func() {
		wctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			// close(done) runs when watch returns, which tells stop the
			// goroutine is finished.
			defer close(done)
			s.watch(wctx)
		}()
		stop = func() { cancel(); <-done }
	}
	start()
	// A deferred func value is read when the deferred call runs, so this
	// stops whichever watcher runs last.
	defer func() { stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.changed:
			stop()
			start()
			// With no folders left the scan still runs: it drops the
			// removed folder's files and keeps none.
			if _, err := s.run(ctx, "", nil); err != nil && ctx.Err() == nil {
				s.log.WarnContext(ctx, "index: scan after a folder change stopped early", "err", err)
			}
		}
	}
}
