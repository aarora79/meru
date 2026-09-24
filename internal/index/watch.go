// This file holds Watch: while merud runs, it asks the OS to report changes
// in the indexed folders (through fsnotify) and re-indexes each changed path
// once it has been quiet for a moment.

package index

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watcher is the state of one Watch call. Only Watch's own goroutine
// touches it, so it needs no lock.
type watcher struct {
	ix *Indexer
	w  *fsnotify.Watcher
	// full turns true when the OS refuses another watch. After that the
	// watcher stops adding folders; the startup scan still catches changes
	// in the ones it couldn't watch.
	full bool
	// pending maps each changed path to the time it may be indexed: the
	// time of its last event plus the debounce delay.
	pending map[string]time.Time
}

// Watch re-indexes files as they change, until ctx ends; then it returns
// nil. It watches every folder that the skip rules keep, and starts
// watching new folders as they appear. When [index] watch is false it
// returns nil at once.
//
// Each change waits until its path has been quiet for the debounce delay
// (500 ms), then goes through IndexPaths: new and changed files are
// indexed, deleted ones removed. A change to a .gitignore or .meruignore
// re-checks its whole folder. Errors while indexing are logged, not
// returned, so one bad file can't stop the watch.
//
// The OS caps how many folders one process may watch (on Linux,
// fs.inotify.max_user_watches; on macOS, the open-file limit). When Watch
// hits the cap it logs a warning and watches what it has; merud's startup
// scan still catches changes elsewhere. Watch fails only when it can't
// start watching at all.
func (ix *Indexer) Watch(ctx context.Context) error {
	if !ix.watch {
		return nil
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("start file watcher: %w", err)
	}
	// defer runs fw.Close() when Watch returns, which releases every watch.
	defer fw.Close()

	wt := &watcher{ix: ix, w: fw, pending: map[string]time.Time{}}
	roots, _ := ix.roots()
	for _, root := range roots {
		wt.addTree(ctx, root, root)
	}
	ix.log.InfoContext(ctx, "index: watching", "folders", len(roots), "watches", len(fw.WatchList()), "full", wt.full)

	// timer fires when the earliest pending path is due. It starts stopped;
	// arm sets it whenever pending changes.
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	// select waits on several channels at once and runs the case for
	// whichever is ready first. The loop handles one event at a time, so
	// pending needs no lock.
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil // the watcher closed
			}
			// Chmod alone (Spotlight, backup tools touching metadata)
			// doesn't change the content.
			if ev.Op == fsnotify.Chmod {
				continue
			}
			wt.pending[ev.Name] = time.Now().Add(ix.debounce)
			wt.arm(timer)
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			// An overflow means the OS dropped events. The startup scan
			// catches whatever this run missed.
			ix.log.WarnContext(ctx, "index: watch error; the next scan catches missed changes", "err", err)
		case <-timer.C:
			wt.flush(ctx)
			wt.arm(timer)
		}
	}
}

// arm sets timer to fire when the earliest pending path is due, or stops it
// when nothing is pending. Since Go 1.23, Reset on a timer needs no draining
// of its channel first.
func (wt *watcher) arm(timer *time.Timer) {
	if len(wt.pending) == 0 {
		timer.Stop()
		return
	}
	var first time.Time
	for _, due := range wt.pending {
		if first.IsZero() || due.Before(first) {
			first = due
		}
	}
	timer.Reset(time.Until(first))
}

// flush indexes every pending path that is due and drops it from pending.
func (wt *watcher) flush(ctx context.Context) {
	now := time.Now()
	var due []string
	for p, at := range wt.pending {
		if !at.After(now) {
			due = append(due, p)
			delete(wt.pending, p)
		}
	}
	slices.Sort(due) // parents before children, so watches go on in order
	var paths []string
	for _, p := range due {
		name := filepath.Base(p)
		if slices.Contains(ignoreFiles, name) {
			// A changed ignore file can skip or re-include anything in its
			// folder, so forget its rules and re-check the folder.
			dir := filepath.Dir(p)
			wt.ix.forgetRules(dir)
			paths = append(paths, dir)
			continue
		}
		paths = append(paths, p)
		// A new folder needs watches of its own before files appear in it.
		if info, err := os.Lstat(p); err == nil && info.IsDir() {
			roots, _ := wt.ix.roots()
			if root := rootFor(roots, p); root != "" {
				wt.addTree(ctx, root, p)
			}
		}
	}
	if len(paths) == 0 {
		return
	}
	rep, err := wt.ix.IndexPaths(ctx, paths)
	if err != nil && ctx.Err() == nil {
		wt.ix.log.WarnContext(ctx, "index: watch update failed", "err", err)
	}
	wt.ix.log.DebugContext(ctx, "index: watch update", "paths", len(paths),
		"indexed", rep.Indexed, "removed", rep.Removed, "failed", rep.Failed)
}

// addTree watches start and every folder under it that the skip rules
// keep. It stops adding once the OS refuses a watch for lack of room.
func (wt *watcher) addTree(ctx context.Context, root, start string) {
	// The walk's error is always nil or SkipAll here, so there is nothing to
	// return.
	_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if wt.full || ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil || !d.IsDir() {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// start needs its parents checked too; below start, the walk never
		// enters a skipped folder, so checking the folder itself is enough.
		reason := ""
		switch {
		case p == root:
		case p == start:
			reason = wt.ix.skipPath(root, p, d.Type())
		default:
			reason = wt.ix.skipReason(root, p, d.Type())
		}
		if reason != "" {
			return fs.SkipDir
		}
		if err := wt.w.Add(p); err != nil {
			if watchLimit(err) {
				wt.full = true
				wt.ix.log.WarnContext(ctx, "index: OS watch limit reached; changes in the remaining folders get picked up at the next startup scan",
					"watches", len(wt.w.WatchList()), "err", err)
				return fs.SkipAll
			}
			wt.ix.log.DebugContext(ctx, "index: can't watch", "path", p, "err", err)
		}
		return nil
	})
}

// watchLimit reports whether err means the OS has no room for another
// watch: ENOSPC is Linux's inotify limit, EMFILE and ENFILE the open-file
// limits that macOS's kqueue runs into.
func watchLimit(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}
