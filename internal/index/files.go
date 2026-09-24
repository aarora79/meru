// This file lets code outside the indexer see the [index] folders the way
// the indexer does: which folders it reads, whether it skips a path and
// why, a walk that applies the skip rules, and a file's text as the
// chunkers read it. The read-only file tools in internal/builtin use it,
// so they reach exactly the files that search reaches.

package index

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Roots returns the [index] folders with symlinks resolved, leaving out the
// ones that don't exist now. These are the paths the store keys files by.
func (ix *Indexer) Roots() []string {
	// The second result lists the missing folders; Roots has no use for it.
	resolved, _ := ix.roots()
	return resolved
}

// Checked is what Check learns about one path.
type Checked struct {
	Path   string      // the path, cleaned, under Root
	Root   string      // the [index] folder that holds Path, as Roots gives it
	Info   fs.FileInfo // what Lstat says about Path
	Reason string      // why the indexer skips Path, one of the Reason constants; "" when it reads it
}

// Check says whether the indexer reads the file or folder at p, an absolute
// path, and why not when it doesn't. It applies every rule the indexer
// applies: the name rules and ignore files for p and each folder between
// its [index] folder and p, then, for a file, the max_file_mb cap and the
// NUL-byte test. It reads the ignore files fresh, so a .meruignore edited a
// moment ago counts.
//
// Check never follows a symlink below the [index] folder: a link at p, or
// a folder link between the folder and p, comes back with ReasonSymlink.
//
// It fails with ErrOutsideFolders when p sits in no [index] folder, and
// with Lstat's error, such as fs.ErrNotExist, when p can't be seen.
func (ix *Indexer) Check(p string) (Checked, error) {
	path, root := ix.locate(filepath.Clean(p))
	if root == "" {
		return Checked{}, fmt.Errorf("%s: %w", p, ErrOutsideFolders)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Checked{}, err
	}
	c := Checked{Path: path, Root: root, Info: info}
	if path == root {
		return c, nil
	}
	ix.forgetRules(root)
	c.Reason = ix.skipPath(root, path, info.Mode())
	if c.Reason == "" && !info.IsDir() {
		c.Reason = ix.contentReason(path, info)
	}
	return c, nil
}

// locate finds the [index] folder that holds p and returns p written under
// that folder's resolved path, or "" for root when no folder holds it.
//
// It first compares p as written with each folder, both as config.toml
// names it and with symlinks resolved, so "~/notes/a.md" works when
// ~/notes links to ~/Dropbox/notes. Only when that finds nothing does it
// resolve the symlinks above p, as IndexPaths does. Either way it leaves p
// itself unresolved, so a link at p stays a link for Check to refuse. When
// folders nest, the deepest one wins.
func (ix *Indexer) locate(p string) (path, root string) {
	for _, f := range ix.folders {
		r, err := filepath.EvalSymlinks(f)
		if err != nil {
			continue // the folder doesn't exist now
		}
		for _, prefix := range []string{f, r} {
			if !within(prefix, p) || len(r) <= len(root) {
				continue
			}
			// within said yes, so Rel can't fail here.
			rel, _ := filepath.Rel(prefix, p)
			path, root = filepath.Join(r, rel), r
		}
	}
	if root != "" {
		return path, root
	}
	abs := p
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		abs = filepath.Join(dir, filepath.Base(p))
	}
	if r := rootFor(ix.Roots(), abs); r != "" {
		return abs, r
	}
	return "", ""
}

// contentReason applies the rules that look past a file's name: the
// max_file_mb cap, and, for anything but a PDF, a NUL byte in the first
// 8 KB. It returns "" to keep the file. A file it can't open counts as
// kept; the read that follows reports the real problem.
func (ix *Indexer) contentReason(p string, info fs.FileInfo) string {
	if info.Size() > ix.maxBytes {
		return ReasonTooLarge
	}
	if kind, _ := kindOf(p); kind == KindPDF {
		return ""
	}
	f, err := os.Open(p) // #nosec G304 -- a path inside an [index] folder that passed skipPath
	if err != nil {
		return ""
	}
	// defer runs f.Close() when contentReason returns. A read-only file has
	// nothing to flush, so its error doesn't matter.
	defer f.Close()
	head := make([]byte, binarySniffBytes)
	n, _ := io.ReadFull(f, head)
	if looksBinary(head[:n]) {
		return ReasonBinary
	}
	return ""
}

// Walk walks dir, a folder that Check kept, the way Scan does, and calls fn
// for every file and folder below it, top down in name order. fn gets the
// entry's path, what Lstat says about it, and the reason the indexer skips
// it, or "" when it reads it. Walk never enters a folder it skips and never
// follows a symlink. fn may return fs.SkipDir to pass over a folder's
// contents, or fs.SkipAll to stop the walk.
//
// A file or folder Walk can't read is left out without a word. Walk reads
// the ignore files fresh. It returns ctx's error when ctx ends, fn's error
// when fn fails, and ErrOutsideFolders when dir sits in no [index] folder.
func (ix *Indexer) Walk(ctx context.Context, dir string, fn func(p string, info fs.FileInfo, reason string) error) error {
	root := rootFor(ix.Roots(), dir)
	if root == "" {
		return fmt.Errorf("%s: %w", dir, ErrOutsideFolders)
	}
	ix.forgetRules(root)
	// filepath.WalkDir calls the function below once per file and folder,
	// top down, and never follows a symlink.
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == dir {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == dir {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // it vanished since the folder was listed
		}
		reason := ix.skipReason(root, p, info.Mode())
		if reason == "" && !info.IsDir() {
			reason = ix.contentReason(p, info)
		}
		if err := fn(p, info, reason); err != nil {
			return err
		}
		if reason != "" && info.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

// Text is a file's text as the indexer reads it.
type Text struct {
	Kind string // one of the Kind constants, such as KindMarkdown
	// Pages holds a PDF's pages in order, page 1 first, and for every other
	// kind one entry with the whole text. Markdown, plain text and code come
	// as the file holds them; HTML comes as the text the indexer pulls out.
	Pages []string
}

// ReadText returns the text of the file at p, which Check or Walk kept, the
// same text the chunkers cut up. It opens the file through an os.Root on
// its [index] folder, which refuses any path that leads out of the folder,
// ".." or symlink alike.
//
// reason is set, and Text empty, when the file turns out to be one the
// indexer skips: it changed into a symlink, grew past max_file_mb, or holds
// a NUL byte. It fails with ErrOutsideFolders when p sits in no [index]
// folder, when the file can't be read, and when a PDF can't be parsed or
// holds no text.
func (ix *Indexer) ReadText(p string) (text Text, reason string, err error) {
	root := rootFor(ix.Roots(), p)
	if root == "" {
		return Text{}, "", fmt.Errorf("%s: %w", p, ErrOutsideFolders)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return Text{}, "", err
	}
	data, reason, err := ix.readInRoot(root, rel)
	if err != nil || reason != "" {
		return Text{}, reason, err
	}
	kind, why := kindOf(p)
	if why != "" {
		return Text{}, why, nil
	}
	if kind != KindPDF && looksBinary(data) {
		return Text{}, ReasonBinary, nil
	}
	switch kind {
	case KindPDF:
		pages, err := PDFText(data)
		if err != nil {
			return Text{}, "", err
		}
		return Text{Kind: kind, Pages: pages}, "", nil
	case KindHTML:
		_, text := HTMLText(string(data))
		return Text{Kind: kind, Pages: []string{text}}, "", nil
	default:
		return Text{Kind: kind, Pages: []string{string(data)}}, "", nil
	}
}

// readInRoot reads rel inside the folder root through an os.Root. Like
// readFile, it checks that the file it opened is the plain file Lstat saw,
// and reads no more than max_file_mb. reason is ReasonSymlink when rel is a
// link or anything but a plain file, and ReasonTooLarge when it's over the
// cap.
func (ix *Indexer) readInRoot(root, rel string) (data []byte, reason string, err error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	info, err := r.Lstat(rel)
	if err != nil {
		return nil, "", err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, ReasonSymlink, nil
	case !info.Mode().IsRegular():
		return nil, ReasonUnsupported, nil
	case info.Size() > ix.maxBytes:
		return nil, ReasonTooLarge, nil
	}
	f, err := r.Open(rel)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(info, opened) {
		return nil, ReasonSymlink, nil
	}
	data, err = io.ReadAll(io.LimitReader(f, ix.maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > ix.maxBytes {
		return nil, ReasonTooLarge, nil
	}
	return data, "", nil
}
