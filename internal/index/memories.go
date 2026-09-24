// This file holds Memories, the syncer that keeps the store's memories
// table in step with the memory folder, ~/.meru/memory: a Sync that
// compares every memory file with the table, and a Watch that runs Sync
// again soon after a file changes.
//
// Memory files are unlike the [index] folders in three ways, which is why
// they get this small syncer instead of the Indexer: they live in one folder
// merud owns, not the user's list; each is one short fact, so it needs no
// chunking and is one row with one vector; and they go to their own tables
// (memories, memory_vec, memory_fts), not documents and chunks. The
// syncer borrows the Indexer's embed batch size and debounce delay.

package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
)

// maxMemoryEmbedBytes caps the text of one memory that goes to the
// embedding model. Meru saves memories up to 4 KiB, but you may make a file
// longer by hand (memory reads up to 64 KiB), and a long text can pass the
// embedding model's context. The first 4 KiB of a fact say what it is about.
// Keyword search still covers the whole text.
const maxMemoryEmbedBytes = 4 << 10

// MemorySink is the part of the store the syncer writes to. *store.Store
// satisfies it; tests pass a fake.
type MemorySink interface {
	// MemoryIDs returns each stored memory's ID with its mtime, hash and
	// whether it has a vector.
	MemoryIDs(ctx context.Context) (map[string]store.MemoryStamp, error)
	// ReplaceMemory stores one memory and its vector.
	ReplaceMemory(ctx context.Context, m store.Memory, v engine.Vector) error
	// DeleteMemory removes one memory.
	DeleteMemory(ctx context.Context, memID string) error
}

// MemoryReport says what one Sync did.
type MemoryReport struct {
	Seen      int           // memory files read
	Indexed   int           // memories embedded and stored
	Unchanged int           // memories the store held with the same mtime, hash and a vector
	Removed   int           // memories dropped because their file is gone
	Duration  time.Duration // wall time of the call
}

// Memories syncs the memory folder into the store. Make one with
// NewMemories. Its methods are safe to call from several goroutines.
type Memories struct {
	mem      *memory.Store
	sink     MemorySink
	eng      engine.Engine
	log      *slog.Logger
	debounce time.Duration

	// mu lets one Sync run at a time. The watcher, a memory op and the
	// remember tool can all ask for one at once; taking turns means two of
	// them never embed the same file or delete what the other has written.
	mu sync.Mutex
}

// NewMemories returns a syncer that reads mem, writes to sink and embeds
// with eng. log may be nil.
func NewMemories(mem *memory.Store, sink MemorySink, eng engine.Engine, log *slog.Logger) *Memories {
	if log == nil {
		log = obs.Discard()
	}
	return &Memories{mem: mem, sink: sink, eng: eng, log: log, debounce: debounce}
}

// Sync brings the store's memories in line with the files. It reads every
// memory file and embeds only those that are new, whose mtime or content
// hash changed, or that lost their vector to an embedding model change. It
// removes the rows of files that are gone.
//
// When some files can't be read, Sync stores the rest, removes nothing,
// and returns the read error with its report: it can't tell a file it
// couldn't read from one that is gone, and a memory shouldn't vanish from
// recall over a permission problem. It fails when the embedding model or
// the store fails.
func (ms *Memories) Sync(ctx context.Context) (MemoryReport, error) {
	ms.mu.Lock()
	// defer runs the Unlock when Sync returns, on every path out.
	defer ms.mu.Unlock()
	start := time.Now()
	var rep MemoryReport

	files, listErr := ms.mem.List()
	if listErr != nil && len(files) == 0 {
		return rep, fmt.Errorf("sync memories: %w", listErr)
	}
	rep.Seen = len(files)
	stored, err := ms.sink.MemoryIDs(ctx)
	if err != nil {
		return rep, fmt.Errorf("sync memories: %w", err)
	}

	var changed []store.Memory
	onDisk := map[string]bool{} // the IDs of the files read, to find the ones gone
	for _, f := range files {
		onDisk[f.ID] = true
		row := store.Memory{
			MemID: f.ID, Kind: f.Kind, Text: f.Text, Created: f.Created,
			Source: f.Source, MTime: f.Modified, Hash: memoryHash(f),
		}
		if st, ok := stored[f.ID]; ok && st.HasVector && st.Hash == row.Hash && sameMTime(st.MTime, row.MTime) {
			rep.Unchanged++
			continue
		}
		changed = append(changed, row)
	}
	if err := ms.store(ctx, changed); err != nil {
		return rep, err
	}
	rep.Indexed = len(changed)

	if listErr == nil {
		for id := range stored {
			if onDisk[id] {
				continue
			}
			if err := ms.sink.DeleteMemory(ctx, id); err != nil {
				return rep, fmt.Errorf("sync memories: %w", err)
			}
			rep.Removed++
		}
	}
	rep.Duration = time.Since(start)
	ms.log.DebugContext(ctx, "memory: synced", "seen", rep.Seen, "indexed", rep.Indexed,
		"unchanged", rep.Unchanged, "removed", rep.Removed, "ms", rep.Duration.Milliseconds())
	if listErr != nil {
		return rep, fmt.Errorf("sync memories: kept every stored memory, because some files couldn't be read: %w", listErr)
	}
	return rep, nil
}

// store embeds rows, embedBatch texts per Embed call, and writes each row
// with its vector.
func (ms *Memories) store(ctx context.Context, rows []store.Memory) error {
	for i := 0; i < len(rows); i += embedBatch {
		batch := rows[i:min(i+embedBatch, len(rows))]
		texts := make([]string, len(batch))
		for j, r := range batch {
			texts[j] = cutText(r.Text, maxMemoryEmbedBytes)
		}
		vecs, err := ms.eng.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("sync memories: embed: %w", err)
		}
		if len(vecs) != len(texts) {
			return fmt.Errorf("sync memories: engine returned %d vectors for %d texts", len(vecs), len(texts))
		}
		for j, r := range batch {
			if err := ms.sink.ReplaceMemory(ctx, r, vecs[j]); err != nil {
				return fmt.Errorf("sync memories: %w", err)
			}
		}
	}
	return nil
}

// memoryHash returns the hex SHA-256 of what the store keeps of a memory:
// its kind, created date, source and text, each followed by a zero byte so
// no two different memories run together into the same bytes. Hashing the
// parsed parts, not the raw file, is enough: they are all the store sees.
func memoryHash(m memory.Memory) string {
	h := sha256.New()
	for _, part := range []string{m.Kind, m.Created.UTC().Format(time.RFC3339), m.Source, m.Text} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cutText returns text cut to at most limit bytes, at a character boundary.
func cutText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	// A UTF-8 character's later bytes are "continuation" bytes; back up
	// until the cut sits at the start of a character.
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// Watch runs Sync once, then again each time the memory folder has been
// quiet for the debounce delay (500 ms) after a change, until ctx ends; then
// it returns nil. That catches hand edits, and files added or deleted by
// anything other than merud. It watches the memory folder and each kind
// folder in it, and starts watching a kind folder when one appears.
//
// A failed Sync is logged, not returned, so one bad file can't stop the
// watch. When the OS can't watch at all, Watch still runs the first Sync,
// then returns the watcher's error; the next start catches later edits.
func (ms *Memories) Watch(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		ms.syncAndLog(ctx)
		return fmt.Errorf("start memory watcher: %w", err)
	}
	// defer runs fw.Close() when Watch returns, which drops every watch.
	defer fw.Close()
	ms.addWatches(ctx, fw)
	ms.syncAndLog(ctx)

	// timer fires once the folder has been quiet for the debounce delay. It
	// starts stopped; each event resets it.
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	// select waits on several channels at once and runs the case for
	// whichever is ready first.
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			timer.Reset(ms.debounce)
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			ms.log.WarnContext(ctx, "memory: watch error; the next change or start catches up", "err", err)
		case <-timer.C:
			// A new kind folder needs a watch before files appear in it.
			ms.addWatches(ctx, fw)
			ms.syncAndLog(ctx)
		}
	}
}

// syncAndLog runs Sync and logs its outcome: info when it stored or removed
// something, a warning when it failed while ctx is still live.
func (ms *Memories) syncAndLog(ctx context.Context) {
	rep, err := ms.Sync(ctx)
	if err != nil {
		if ctx.Err() == nil {
			ms.log.WarnContext(ctx, "memory: sync failed; recall uses what the store holds", "err", err)
		}
		return
	}
	if rep.Indexed > 0 || rep.Removed > 0 {
		ms.log.InfoContext(ctx, "memory: synced", "seen", rep.Seen, "indexed", rep.Indexed,
			"removed", rep.Removed, "ms", rep.Duration.Milliseconds())
	}
}

// addWatches watches the memory folder and each kind folder in it that
// isn't watched yet. memory.Store.Kinds lists only plain folders with a
// valid kind name, so no symbolic link gets a watch. A folder that can't be
// watched is logged at debug level and skipped.
func (ms *Memories) addWatches(ctx context.Context, fw *fsnotify.Watcher) {
	dirs := []string{ms.mem.Dir()}
	kinds, err := ms.mem.Kinds()
	if err != nil {
		ms.log.WarnContext(ctx, "memory: can't list the kind folders to watch", "err", err)
	}
	for _, k := range kinds {
		dirs = append(dirs, filepath.Join(ms.mem.Dir(), k))
	}
	watched := fw.WatchList()
	for _, d := range dirs {
		if slices.Contains(watched, d) {
			continue
		}
		// Kinds includes the default kinds even when their folder is gone.
		if _, err := os.Lstat(d); err != nil {
			continue
		}
		if err := fw.Add(d); err != nil {
			ms.log.DebugContext(ctx, "memory: can't watch", "path", d, "err", err)
		}
	}
}
