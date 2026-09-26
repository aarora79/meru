// This file holds what merud does with the search index outside a turn: the
// startup scan, the file watcher, and the two index ops `meru index` sends.
// It also holds the small adapter that lets the agent search the store.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// indexService runs scans of the [index] folders and answers the index ops.
// Scans take turns: the startup scan and each `meru index` run one at a
// time, so two walks never race over the same files. The watcher runs
// beside them; the indexer itself keeps a file from being indexed twice at
// once.
type indexService struct {
	ix         *index.Indexer
	st         *store.Store
	memories   memoryService // counts the memory files for the status op
	cfg        config.Index  // [index] as merud started with it, for the skip rules CountFiles applies
	configPath string        // config.toml, which the folder ops change
	log        *slog.Logger
	// editConfig runs a change to config.toml under the one lock every
	// writer in merud shares; see builtin.Tools.EditConfig.
	editConfig func(func() error) error

	// changed tells watchAndRescan that the folders changed. It holds at
	// most one signal: two changes before the loop wakes need one rescan.
	changed chan struct{}

	// foldersMu guards folders, which the folder ops replace.
	foldersMu sync.Mutex
	folders   []string // [index] folders as config.toml writes them

	// turn holds one token while a scan runs. A channel with room for one
	// value works as a lock that a waiter can give up on when its ctx ends,
	// which sync.Mutex can't do.
	turn chan struct{}

	// mu guards the fields below it, which the status op reads while a scan
	// writes them.
	mu       sync.Mutex
	scanning bool
	last     *rpc.IndexReport // the last finished run; nil before the first
	lastAt   time.Time        // when it ended
	lastErr  string           // why it stopped early, if it did
}

// newIndexService returns an indexService over ix and st. cfg is the
// [index] section merud started with; its folders are the first list the
// folder ops change. editConfig runs each change to config.toml under
// merud's one config lock; nil runs it with no lock, for tests.
func newIndexService(ix *index.Indexer, st *store.Store, memories memoryService, cfg config.Index, configPath string,
	editConfig func(func() error) error, log *slog.Logger) *indexService {
	if editConfig == nil {
		editConfig = func(f func() error) error { return f() }
	}
	return &indexService{
		ix: ix, st: st, memories: memories, cfg: cfg, folders: slices.Clone(cfg.Folders), configPath: configPath,
		editConfig: editConfig, log: log,
		turn: make(chan struct{}, 1), changed: make(chan struct{}, 1),
	}
}

// currentFolders returns the [index] folders as config.toml writes them
// now. The agent and the router read it on every turn.
func (s *indexService) currentFolders() []string {
	s.foldersMu.Lock()
	defer s.foldersMu.Unlock()
	return slices.Clone(s.folders)
}

// startupScan brings the index up to date when merud starts: a Reembed when
// the store dropped its vectors for a new embedding model, otherwise a Scan.
// It runs in the background while merud answers questions, and returns when
// the scan ends or ctx does. A failed scan is logged, not returned: merud
// still answers questions from whatever the index holds.
func (s *indexService) startupScan(ctx context.Context) {
	if len(s.currentFolders()) == 0 {
		s.log.Info("index: no [index] folders in config; nothing to index")
		return
	}
	if _, err := s.run(ctx, "", nil); err != nil && ctx.Err() == nil {
		s.log.Warn("index: startup scan stopped early", "err", err)
	}
}

// watch re-indexes files as they change, until ctx ends. When the OS can't
// watch at all, it logs why and returns; `meru index` and the next start
// still catch changes.
func (s *indexService) watch(ctx context.Context) {
	if len(s.currentFolders()) == 0 {
		return
	}
	if err := s.ix.Watch(ctx); err != nil {
		s.log.Warn("index: can't watch the folders; run `meru index` to pick up changes", "err", err)
	}
}

// run waits for its turn, then indexes path, or every folder when path is
// "". A full scan re-embeds everything when the store needs it (see
// store.NeedsReembed), so a re-embed cut short by a restart finishes on the
// next scan. progress, when not nil, receives one line of news; an error
// from it stops the run. run records a full scan's outcome for the status
// op.
func (s *indexService) run(ctx context.Context, path string, progress func(string) error) (index.Report, error) {
	say := func(line string) error {
		if progress == nil {
			return nil
		}
		return progress(line)
	}

	// select takes the first case that can go: our turn, or ctx ending.
	// The default case runs only when neither is ready at once, which means
	// another scan holds the turn.
	select {
	case s.turn <- struct{}{}:
	default:
		if err := say("waiting for the scan already running"); err != nil {
			return index.Report{}, err
		}
		select {
		case s.turn <- struct{}{}:
		case <-ctx.Done():
			return index.Report{}, ctx.Err()
		}
	}
	// Reading the token back frees the turn for the next scan.
	defer func() { <-s.turn }()

	s.setScanning(true)
	if path == "" {
		rep, err := s.scanAll(ctx, say)
		s.finish(&rep, err)
		return rep, err
	}
	// A run over one path isn't a scan of the folders, so the status op's
	// "last scan" doesn't change.
	defer s.finish(nil, nil)
	if err := say("indexing " + path); err != nil {
		return index.Report{}, err
	}
	rep, err := s.ix.IndexPaths(ctx, []string{path})
	s.log.InfoContext(ctx, "index: indexed path", "seen", rep.Seen, "indexed", rep.Indexed,
		"unchanged", rep.Unchanged, "removed", rep.Removed, "failed", rep.Failed,
		"chunks", rep.Chunks, "ms", rep.Duration.Milliseconds())
	return rep, err
}

// scanAll scans every folder, re-embedding when the store asks for it. The
// indexer logs the scan's counts at info level itself.
func (s *indexService) scanAll(ctx context.Context, say func(string) error) (index.Report, error) {
	reembed, err := s.st.NeedsReembed(ctx)
	if err != nil {
		return index.Report{}, err
	}
	if reembed {
		s.log.InfoContext(ctx, "index: the embedding model changed; embedding every file again")
		if err := say(fmt.Sprintf("embedding every file in %s again for the new embedding model", folderCount(len(s.currentFolders())))); err != nil {
			return index.Report{}, err
		}
		return s.ix.Reembed(ctx)
	}
	if err := say("scanning " + folderCount(len(s.currentFolders()))); err != nil {
		return index.Report{}, err
	}
	return s.ix.Scan(ctx)
}

// setScanning records whether a scan is running.
func (s *indexService) setScanning(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanning = on
}

// finish marks the end of a run. For a full scan, rep is its report and
// err its error, kept for the status op; for any other run rep is nil and
// only the running flag changes.
func (s *indexService) finish(rep *index.Report, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanning = false
	if rep == nil {
		return
	}
	r := reportOf(*rep)
	s.last = &r
	s.lastAt = time.Now()
	s.lastErr = ""
	if err != nil {
		s.lastErr = err.Error()
	}
}

// handleIndex answers OpIndex: it rescans every folder, or indexes
// req.Path, and sends a "report" event. It fails when no folders are
// configured, when req.Path isn't absolute or sits outside every [index]
// folder, and when the scan fails. The messages say what to change, because
// the client prints them as they are.
func (s *indexService) handleIndex(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	if len(s.currentFolders()) == 0 {
		return fmt.Errorf("no folders to index; add one in the desktop app's Library, or list them under [index] folders in %s and restart merud", s.configPath)
	}
	if req.Path != "" && !filepath.IsAbs(req.Path) {
		return fmt.Errorf("index %q: need an absolute path", req.Path)
	}
	progress := func(line string) error {
		return emit(rpc.Event{Type: rpc.EventProgress, Text: line})
	}
	rep, err := s.run(ctx, req.Path, progress)
	if errors.Is(err, index.ErrOutsideFolders) {
		return fmt.Errorf("%s isn't inside any [index] folder; add it to folders under [index] in %s and restart merud",
			req.Path, s.configPath)
	}
	if err != nil {
		return err
	}
	r := reportOf(rep)
	return emit(rpc.Event{Type: rpc.EventReport, Report: &r})
}

// handleStatus answers OpIndexStatus with one "status" event, which
// includes the size of meru.db on disk and how many memories there are, so
// the client can tell whether Meru knows the user yet.
func (s *indexService) handleStatus(ctx context.Context, emit func(rpc.Event) error) error {
	stats, err := s.st.Stats(ctx)
	if err != nil {
		return err
	}
	size, err := s.st.DiskBytes()
	if err != nil {
		return err
	}
	st := rpc.IndexStatus{
		Folders:   s.currentFolders(),
		Documents: stats.Documents,
		Chunks:    stats.Chunks,
		Vectors:   stats.Vectors,
		DBBytes:   size,
	}
	st.Memories, st.Profile = s.memories.counts()
	s.mu.Lock()
	st.Scanning = s.scanning
	st.LastScan = s.last
	if !s.lastAt.IsZero() {
		st.LastScanAt = s.lastAt.Format(time.RFC3339)
	}
	st.LastError = s.lastErr
	s.mu.Unlock()
	return emit(rpc.Event{Type: rpc.EventStatus, Status: &st})
}

// reportOf copies an index.Report into the protocol's shape, adding up the
// skipped entries across reasons.
func reportOf(rep index.Report) rpc.IndexReport {
	skipped := 0
	for _, n := range rep.Skipped {
		skipped += n
	}
	return rpc.IndexReport{
		Seen: rep.Seen, Indexed: rep.Indexed, Unchanged: rep.Unchanged,
		Removed: rep.Removed, Failed: rep.Failed, Skipped: skipped,
		Chunks: rep.Chunks, DurationMillis: rep.Duration.Milliseconds(),
	}
}

// searchAdapter lets retrieve.Search and retrieve.SearchSessions serve as
// the agent's Searcher, and retrieve.Search as search_files' searcher. The agent doesn't hold the store or the list
// sizes; this type in main joins them, the way routerAdapter joins the
// router.
type searchAdapter struct {
	st  *store.Store
	eng engine.Engine
}

// Search runs hybrid search over the store with the default list sizes.
func (s searchAdapter) Search(ctx context.Context, query string) ([]retrieve.Result, error) {
	return retrieve.Search(ctx, s.st, s.eng, query, retrieve.Options{})
}

// SearchFiles runs the same hybrid search for search_files, keeping the
// limit best chunks instead of the default ten.
func (s searchAdapter) SearchFiles(ctx context.Context, query string, limit int) ([]retrieve.Result, error) {
	return retrieve.Search(ctx, s.st, s.eng, query, retrieve.Options{TopN: limit})
}

// SearchSessions recalls the n past sessions that best match query,
// leaving out the session excludeSession.
func (s searchAdapter) SearchSessions(ctx context.Context, query, excludeSession string, n int) ([]retrieve.SessionResult, error) {
	return retrieve.SearchSessions(ctx, s.st, s.eng, query, excludeSession, n)
}

// embedDims asks the embedding model for one vector and returns its size,
// which the store needs to open. Asking the model, rather than keeping the
// size in config, means a new embedding model can't leave a stale number
// behind.
func embedDims(ctx context.Context, eng engine.Engine) (int, error) {
	vecs, err := eng.Embed(ctx, []string{"How many numbers are in this vector?"})
	if err != nil {
		return 0, fmt.Errorf("embed a probe text: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return 0, errors.New("embed a probe text: the model returned no vector")
	}
	return len(vecs[0]), nil
}

// folderCount writes n folders as "1 folder" or "3 folders".
func folderCount(n int) string {
	if n == 1 {
		return "1 folder"
	}
	return fmt.Sprintf("%d folders", n)
}
