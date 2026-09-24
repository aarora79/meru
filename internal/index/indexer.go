// This file holds the Indexer: it walks the configured folders, reads each
// file that passes the skip rules, skips it when the store already holds the
// same version, and otherwise chunks it, embeds the chunks in batches and
// hands everything to the store. It also removes files that disappeared.

package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
)

// Sink is the part of the store the indexer writes to. *store.Store
// satisfies it; tests pass a fake. Following the rule "define an interface
// where it's used, with only the methods you call", it lists four methods.
type Sink interface {
	// Document returns what the index holds for path; ok is false when
	// nothing.
	Document(ctx context.Context, path string) (doc store.Document, ok bool, err error)
	// ReplaceDocument stores doc with its chunks and one vector per chunk.
	ReplaceDocument(ctx context.Context, doc store.Document, chunks []store.Chunk, vecs []engine.Vector) error
	// DeleteDocument removes path and its chunks.
	DeleteDocument(ctx context.Context, path string) error
	// Paths lists the indexed paths that start with prefix.
	Paths(ctx context.Context, prefix string) ([]string, error)
}

// ErrOutsideFolders is what IndexPaths reports for a path that isn't inside
// any [index] folder. The store only ever holds files from those folders,
// so the startup scan can tell what to remove.
var ErrOutsideFolders = errors.New("not inside any [index] folder")

// embedBatch is how many chunk texts go to the engine in one Embed call.
// One call per chunk would pay Ollama's per-request cost hundreds of times
// per file; one call per file could send thousands of texts at once. 32
// keeps each request small and the count of requests low.
const embedBatch = 32

// debounce is how long a path must stay quiet before the watcher indexes
// it. Editors often write a file in several steps (truncate, write, rename),
// and 500 ms lets them finish.
const debounce = 500 * time.Millisecond

// Report says what one Scan or IndexPaths call did.
type Report struct {
	Seen      int            // files found, whether skipped or not
	Skipped   map[string]int // files and folders skipped, by reason (see the Reason constants); a skipped folder counts once
	Indexed   int            // files chunked, embedded and stored
	Unchanged int            // files the store already held with the same mtime and hash
	Removed   int            // files dropped from the store because they're gone or now skipped
	Failed    int            // files that couldn't be read or parsed; they keep what the store held
	Chunks    int            // chunks written
	Duration  time.Duration  // wall time of the call
}

// skip counts one skipped entry under reason. A pointer receiver
// (r *Report) lets the method change the Report it's called on.
func (r *Report) skip(reason string) {
	if r.Skipped == nil {
		r.Skipped = map[string]int{}
	}
	r.Skipped[reason]++
}

// Indexer keeps the store's copy of the configured folders up to date. Make
// one with New. Its methods are safe to call from several goroutines: Watch
// can run while Scan does.
type Indexer struct {
	folders  []string      // the configured folders, "~" expanded, cleaned
	ignore   []pattern     // [index] ignore, compiled
	maxBytes int64         // max_file_mb in bytes
	lim      limits        // chunk size and overlap in characters
	watch    bool          // [index] watch
	debounce time.Duration // quiet time before the watcher indexes a path
	sink     Sink          // where documents and chunks go
	eng      engine.Engine // Embed turns chunk texts into vectors
	log      *slog.Logger

	// fileMu lets one goroutine at a time index a file, from reading it to
	// writing it to the store. Without it, Scan and the watcher could read
	// two versions of one file and store the older one last.
	fileMu sync.Mutex

	// rulesMu guards rules, the cache of each folder's parsed .gitignore and
	// .meruignore, keyed by absolute folder path.
	rulesMu sync.Mutex
	rules   map[string][]pattern
}

// New checks cfg and returns an Indexer that writes to sink and embeds with
// eng. It expands a leading "~" in each folder to the home directory. It
// fails when a folder can't be expanded or an ignore pattern is malformed.
// A folder that doesn't exist yet isn't an error: Scan logs it and moves on,
// so an unplugged drive doesn't stop merud.
func New(cfg config.Index, sink Sink, eng engine.Engine, log *slog.Logger) (*Indexer, error) {
	ix := &Indexer{
		maxBytes: int64(cfg.MaxFileMB) << 20,
		lim:      newLimits(cfg.ChunkTokens, cfg.OverlapTokens),
		watch:    cfg.Watch,
		debounce: debounce,
		sink:     sink,
		eng:      eng,
		log:      log,
		rules:    map[string][]pattern{},
	}
	if ix.log == nil {
		ix.log = obs.Discard()
	}
	for _, f := range cfg.Folders {
		abs, err := expandHome(f)
		if err != nil {
			return nil, fmt.Errorf("index folder %q: %w", f, err)
		}
		ix.folders = append(ix.folders, abs)
	}
	ps, errs := parsePatterns(cfg.Ignore)
	if len(errs) > 0 {
		return nil, fmt.Errorf("index.ignore: %w", errors.Join(errs...))
	}
	ix.ignore = ps
	return ix, nil
}

// expandHome turns "~" or "~/x" into an absolute path under the home
// directory and cleans any other absolute path. It fails for a relative
// path, for "~user" forms, and when the OS can't say where home is.
func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		return filepath.Join(home, p[1:]), nil
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("must be an absolute path or start with ~/")
	}
	return filepath.Clean(p), nil
}

// roots returns each configured folder with symlinks resolved, leaving out
// the ones that don't exist now; missing holds one error for each of those.
// Resolving matters because the walk never follows a symlink: a folder such
// as ~/notes that links to ~/Dropbox/notes is walked at its real path, and
// the store keys files by that path.
func (ix *Indexer) roots() (resolved []string, missing []error) {
	for _, f := range ix.folders {
		r, err := filepath.EvalSymlinks(f)
		if err != nil {
			missing = append(missing, err)
			continue
		}
		resolved = append(resolved, r)
	}
	return resolved, missing
}

// Scan indexes every configured folder: new and changed files go in,
// unchanged ones are left alone, and files that disappeared (or that the
// skip rules now exclude) come out. merud runs it at startup.
//
// A file that can't be read or parsed counts as Failed and the scan goes
// on. Scan fails, returning the report so far, when ctx ends or when the
// engine or the store returns an error, since those would fail every file
// after it too.
func (ix *Indexer) Scan(ctx context.Context) (Report, error) {
	return ix.scan(ctx, false)
}

// Reembed is Scan for after a change of embedding model: it chunks and
// embeds every file again, even one the store holds unchanged, because the
// store has dropped the old model's vectors (store.NeedsReembed). merud
// runs it at startup in place of Scan when the store asks for it.
func (ix *Indexer) Reembed(ctx context.Context) (Report, error) {
	return ix.scan(ctx, true)
}

// scan does the work of Scan and Reembed. force skips the "unchanged"
// check, so every file gets chunked and embedded again.
func (ix *Indexer) scan(ctx context.Context, force bool) (Report, error) {
	start := time.Now()
	ctx, span := obs.Tracer().Start(ctx, "meru.index.scan",
		trace.WithAttributes(
			attribute.Int("meru.index.folders", len(ix.folders)),
			attribute.Bool("meru.index.reembed", force),
		))
	defer span.End()

	var rep Report
	var err error
	roots, missing := ix.roots()
	for _, m := range missing {
		// The store keeps what it held for a missing folder: it may sit on
		// a drive that isn't plugged in right now.
		ix.log.WarnContext(ctx, "index: folder unavailable; keeping its index entries", "err", m)
	}
	for _, root := range roots {
		if err = ix.scanTree(ctx, root, root, force, &rep); err != nil {
			break
		}
	}
	rep.Duration = time.Since(start)
	span.SetAttributes(reportAttrs(rep)...)
	obs.EndSpanErr(ctx, span, err)
	ix.log.InfoContext(ctx, "index: scan done", "reembed", force,
		"seen", rep.Seen, "indexed", rep.Indexed, "unchanged", rep.Unchanged,
		"removed", rep.Removed, "failed", rep.Failed, "chunks", rep.Chunks,
		"skipped", rep.Skipped, "ms", rep.Duration.Milliseconds())
	return rep, err
}

// reportAttrs turns a report's counts into span attributes.
func reportAttrs(rep Report) []attribute.KeyValue {
	skipped := 0
	for _, n := range rep.Skipped {
		skipped += n
	}
	return []attribute.KeyValue{
		attribute.Int("meru.index.files_seen", rep.Seen),
		attribute.Int("meru.index.files_skipped", skipped),
		attribute.Int("meru.index.files_indexed", rep.Indexed),
		attribute.Int("meru.index.files_unchanged", rep.Unchanged),
		attribute.Int("meru.index.files_removed", rep.Removed),
		attribute.Int("meru.index.files_failed", rep.Failed),
		attribute.Int("meru.index.chunks", rep.Chunks),
	}
}

// IndexPaths indexes the given files and folders now, with the same rules
// as Scan: a folder is walked, a file is indexed or skipped, and a path that
// no longer exists (or that the rules now skip) is removed from the store,
// along with everything under it. The watcher uses it, and so does
// `meru index <folder>`.
//
// Each path must sit inside an [index] folder; one that doesn't fails with
// ErrOutsideFolders and the rest still run. Like Scan, it stops early when
// ctx ends or the engine or store fails.
func (ix *Indexer) IndexPaths(ctx context.Context, paths []string) (Report, error) {
	start := time.Now()
	var rep Report
	var errs []error
	roots, _ := ix.roots()
	for _, p := range paths {
		if err := ix.indexPath(ctx, roots, p, &rep); err != nil {
			if !errors.Is(err, ErrOutsideFolders) {
				rep.Duration = time.Since(start)
				return rep, errors.Join(append(errs, err)...)
			}
			errs = append(errs, err)
		}
	}
	rep.Duration = time.Since(start)
	return rep, errors.Join(errs...)
}

// indexPath is IndexPaths for one path.
func (ix *Indexer) indexPath(ctx context.Context, roots []string, p string, rep *Report) error {
	abs, err := expandHome(p)
	if err != nil {
		return fmt.Errorf("index %s: %w", p, err)
	}
	// Resolve symlinks in the folders above p but not in p itself: a
	// symlink at p gets skipped like any other.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(dir, filepath.Base(abs))
	}
	root := rootFor(roots, abs)
	if root == "" {
		return fmt.Errorf("index %s: %w", p, ErrOutsideFolders)
	}

	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return ix.remove(ctx, abs, rep)
	}
	if err != nil {
		ix.log.WarnContext(ctx, "index: can't read", "path", abs, "err", err)
		rep.Failed++
		return nil
	}
	if abs != root {
		if reason := ix.skipPath(root, abs, info.Mode()); reason != "" {
			ix.log.DebugContext(ctx, "index: skip", "path", abs, "reason", reason)
			rep.skip(reason)
			return ix.remove(ctx, abs, rep)
		}
	}
	if info.IsDir() {
		return ix.scanTree(ctx, root, abs, false, rep)
	}
	rep.Seen++
	res, err := ix.indexFile(ctx, root, abs, info.Mode(), false)
	if err != nil {
		return err
	}
	rep.add(res)
	if res.outcome == outSkipped {
		return ix.remove(ctx, abs, rep)
	}
	return nil
}

// rootFor returns the root that holds p, preferring the deepest when
// folders nest, or "" when none does.
func rootFor(roots []string, p string) string {
	best := ""
	for _, r := range roots {
		if within(r, p) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// within reports whether p is dir or sits somewhere under it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// underPrefix returns dir with one trailing separator, the prefix every path
// inside dir starts with. Without the separator, "/notes" would also match
// "/notes-old/x".
func underPrefix(dir string) string {
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return dir
	}
	return dir + string(filepath.Separator)
}

// remove deletes p from the store, and every indexed path under p in case p
// was a folder. It counts each path it removes.
func (ix *Indexer) remove(ctx context.Context, p string, rep *Report) error {
	if _, ok, err := ix.sink.Document(ctx, p); err != nil {
		return fmt.Errorf("look up %s: %w", p, err)
	} else if ok {
		if err := ix.sink.DeleteDocument(ctx, p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		ix.log.DebugContext(ctx, "index: removed", "path", p)
		rep.Removed++
	}
	under, err := ix.sink.Paths(ctx, underPrefix(p))
	if err != nil {
		return fmt.Errorf("list indexed paths under %s: %w", p, err)
	}
	for _, q := range under {
		if !within(p, q) {
			continue
		}
		if err := ix.sink.DeleteDocument(ctx, q); err != nil {
			return fmt.Errorf("remove %s: %w", q, err)
		}
		ix.log.DebugContext(ctx, "index: removed", "path", q)
		rep.Removed++
	}
	return nil
}

// scanTree walks the folder start, which sits inside root (or is root),
// indexing every file the skip rules keep; force re-embeds unchanged files
// too (see Reembed). Then it removes from the store
// every path under start that the walk didn't keep, except under folders it
// couldn't read: a folder with a permission problem today shouldn't lose
// its entries.
func (ix *Indexer) scanTree(ctx context.Context, root, start string, force bool, rep *Report) error {
	ix.forgetRules(start)
	kept := map[string]bool{} // files indexed, unchanged or failed: the store keeps them
	var unreadable []string   // folders the walk couldn't list
	// filepath.WalkDir calls the function below once per file and folder,
	// top down. Returning fs.SkipDir from it skips a folder's contents.
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == start {
				return err
			}
			ix.log.WarnContext(ctx, "index: can't read", "path", p, "err", err)
			if d != nil && d.IsDir() {
				unreadable = append(unreadable, p)
				return fs.SkipDir
			}
			rep.Failed++
			kept[p] = true
			return nil
		}
		if p == start {
			return nil
		}
		if d.IsDir() {
			if reason := ix.skipReason(root, p, d.Type()); reason != "" {
				ix.log.DebugContext(ctx, "index: skip", "path", p, "reason", reason)
				rep.skip(reason)
				return fs.SkipDir
			}
			return nil
		}
		rep.Seen++
		res, err := ix.indexFile(ctx, root, p, d.Type(), force)
		if err != nil {
			return err
		}
		rep.add(res)
		if res.outcome != outSkipped {
			kept[p] = true
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		// The folder vanished between listing and walking: nothing to keep.
		err = nil
	}
	if err != nil {
		return err
	}

	indexed, err := ix.sink.Paths(ctx, underPrefix(start))
	if err != nil {
		return fmt.Errorf("list indexed paths under %s: %w", start, err)
	}
	for _, p := range indexed {
		if kept[p] || !within(start, p) || underAny(unreadable, p) {
			continue
		}
		if err := ix.sink.DeleteDocument(ctx, p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		ix.log.DebugContext(ctx, "index: removed", "path", p)
		rep.Removed++
	}
	return nil
}

// underAny reports whether p sits inside any of dirs.
func underAny(dirs []string, p string) bool {
	for _, d := range dirs {
		if within(d, p) {
			return true
		}
	}
	return false
}

// outcome says what happened to one file.
type outcome int

// The four outcomes. iota counts up from 0 inside a const block, so each
// name gets its own number without writing them out.
const (
	outIndexed outcome = iota
	outUnchanged
	outSkipped
	outFailed
)

// fileResult is what indexFile reports for one file.
type fileResult struct {
	outcome outcome
	reason  string // why, when outcome is outSkipped
	chunks  int    // chunks written, when outcome is outIndexed
}

// add counts one file's result in the report.
func (r *Report) add(res fileResult) {
	switch res.outcome {
	case outIndexed:
		r.Indexed++
		r.Chunks += res.chunks
	case outUnchanged:
		r.Unchanged++
	case outSkipped:
		r.skip(res.reason)
	case outFailed:
		r.Failed++
	}
}

// indexFile brings the store's copy of the file at p up to date. It checks
// the skip rules, reads the file, and compares its mtime and SHA-256 with
// what the store holds; when both match it stops there, unless force is
// set. Otherwise it chunks the file, embeds the chunks and replaces the
// store's copy.
//
// A problem with the file itself (unreadable, unparseable PDF) comes back as
// outFailed with a nil error. An error from the engine, the store or ctx
// comes back as the error, because it would fail the next file too.
func (ix *Indexer) indexFile(ctx context.Context, root, p string, mode fs.FileMode, force bool) (fileResult, error) {
	if reason := ix.skipReason(root, p, mode); reason != "" {
		ix.log.DebugContext(ctx, "index: skip", "path", p, "reason", reason)
		return fileResult{outcome: outSkipped, reason: reason}, nil
	}

	ix.fileMu.Lock()
	defer ix.fileMu.Unlock()

	ctx, span := obs.Tracer().Start(ctx, "meru.index.file")
	defer span.End()
	res, bytesRead, kind, err := ix.indexFileLocked(ctx, p, force)
	span.SetAttributes(
		attribute.String("meru.index.kind", kind),
		attribute.String("meru.index.outcome", outcomeName(res.outcome)),
		attribute.Int("meru.index.chunks", res.chunks),
		attribute.Int("meru.index.bytes", bytesRead),
	)
	if res.reason != "" {
		span.SetAttributes(attribute.String("meru.index.skip_reason", res.reason))
	}
	obs.EndSpanErr(ctx, span, err)
	return res, err
}

// indexFileLocked does indexFile's work once the name-based skip rules have
// passed and fileMu is held. It also returns the file's size in bytes and
// its kind, for the span.
func (ix *Indexer) indexFileLocked(ctx context.Context, p string, force bool) (fileResult, int, string, error) {
	kind, _ := kindOf(p)
	info, err := os.Lstat(p)
	if err != nil {
		ix.log.WarnContext(ctx, "index: can't read", "path", p, "err", err)
		return fileResult{outcome: outFailed}, 0, kind, nil
	}
	if info.Size() > ix.maxBytes {
		ix.log.DebugContext(ctx, "index: skip", "path", p, "reason", ReasonTooLarge, "bytes", info.Size())
		return fileResult{outcome: outSkipped, reason: ReasonTooLarge}, 0, kind, nil
	}
	if !info.Mode().IsRegular() {
		// p changed into a symlink or something stranger since the walk.
		return fileResult{outcome: outSkipped, reason: ReasonSymlink}, 0, kind, nil
	}
	data, err := ix.readFile(p, info)
	if err != nil {
		ix.log.WarnContext(ctx, "index: can't read", "path", p, "err", err)
		return fileResult{outcome: outFailed}, 0, kind, nil
	}
	if kind != KindPDF && looksBinary(data) {
		ix.log.DebugContext(ctx, "index: skip", "path", p, "reason", ReasonBinary)
		return fileResult{outcome: outSkipped, reason: ReasonBinary}, len(data), kind, nil
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	old, ok, err := ix.sink.Document(ctx, p)
	if err != nil {
		return fileResult{}, len(data), kind, fmt.Errorf("look up %s: %w", p, err)
	}
	if !force && ok && old.Hash == hash && sameMTime(old.MTime, info.ModTime()) {
		ix.log.DebugContext(ctx, "index: unchanged", "path", p)
		return fileResult{outcome: outUnchanged}, len(data), kind, nil
	}

	chunks, err := chunkFile(kind, p, data, ix.lim)
	if err != nil {
		ix.log.WarnContext(ctx, "index: can't parse", "path", p, "kind", kind, "err", err)
		return fileResult{outcome: outFailed}, len(data), kind, nil
	}
	for i := range chunks {
		chunks[i].Ordinal = i
	}
	vecs, err := ix.embed(ctx, chunks)
	if err != nil {
		return fileResult{}, len(data), kind, fmt.Errorf("embed %s: %w", p, err)
	}
	doc := store.Document{Path: p, MTime: info.ModTime(), Hash: hash, Kind: kind}
	if err := ix.sink.ReplaceDocument(ctx, doc, chunks, vecs); err != nil {
		return fileResult{}, len(data), kind, fmt.Errorf("store %s: %w", p, err)
	}
	ix.log.DebugContext(ctx, "index: indexed", "path", p, "kind", kind, "chunks", len(chunks),
		"bytes", len(data), "tokens", tokens(chunks))
	return fileResult{outcome: outIndexed, chunks: len(chunks)}, len(data), kind, nil
}

// outcomeName names an outcome for span attributes.
func outcomeName(o outcome) string {
	switch o {
	case outIndexed:
		return "indexed"
	case outUnchanged:
		return "unchanged"
	case outSkipped:
		return "skipped"
	default:
		return "failed"
	}
}

// sameMTime compares modification times to the second. The content hash
// catches any real change, so second precision is enough, and it keeps the
// check working whatever precision the store keeps times at.
func sameMTime(a, b time.Time) bool {
	return a.Unix() == b.Unix()
}

// readFile reads the file at p, which Lstat described as info. It refuses to
// read more than maxBytes even if the file grew since the check. It also
// checks that the file it opened is the one Lstat saw: if someone swapped p
// for a symlink in between, os.Open would follow it, and SameFile catches
// that. This works the same on every OS, with no build tags.
func (ix *Indexer) readFile(p string, info fs.FileInfo) ([]byte, error) {
	f, err := os.Open(p) // #nosec G304 -- a file found inside a configured folder, checked by skipReason
	if err != nil {
		return nil, err
	}
	// defer runs f.Close() when readFile returns. A read-only file has
	// nothing to flush, so Close's error doesn't matter here.
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("file changed into a link or another file while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(f, ix.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > ix.maxBytes {
		return nil, fmt.Errorf("grew past max_file_mb while being read")
	}
	return data, nil
}

// chunkFile picks the chunker for kind.
func chunkFile(kind, p string, data []byte, lim limits) ([]store.Chunk, error) {
	switch kind {
	case KindMarkdown:
		return chunkMarkdown(string(data), lim), nil
	case KindCode:
		return chunkCode(p, string(data), lim), nil
	case KindHTML:
		return chunkHTML(string(data), lim), nil
	case KindPDF:
		return chunkPDF(data, lim)
	default:
		return chunkText(string(data), lim), nil
	}
}

// embed turns the chunks into vectors, embedBatch texts per Embed call. The
// result has one vector per chunk, in order.
func (ix *Indexer) embed(ctx context.Context, chunks []store.Chunk) ([]engine.Vector, error) {
	vecs := make([]engine.Vector, 0, len(chunks))
	for i := 0; i < len(chunks); i += embedBatch {
		end := min(i+embedBatch, len(chunks))
		texts := make([]string, 0, end-i)
		for _, c := range chunks[i:end] {
			texts = append(texts, embedText(c))
		}
		got, err := ix.eng.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		if len(got) != len(texts) {
			return nil, fmt.Errorf("engine returned %d vectors for %d texts", len(got), len(texts))
		}
		vecs = append(vecs, got...)
	}
	return vecs, nil
}

// embedText is what gets embedded for a chunk: its heading path, then its
// text. The second chunk of a long "Budget > Q3" section doesn't repeat the
// heading line, and without the path its vector would lose what the section
// is about.
func embedText(c store.Chunk) string {
	if c.Heading == "" {
		return c.Text
	}
	return c.Heading + "\n\n" + c.Text
}

// tokens totals the estimated tokens in chunks, for the debug log.
func tokens(chunks []store.Chunk) int {
	n := 0
	for _, c := range chunks {
		n += estimateTokens(c.Text)
	}
	return n
}
