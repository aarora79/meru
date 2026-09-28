// This file decides which files and folders the indexer skips, and why. The
// rules the owner approved for v0.2: hidden entries, build and dependency
// folders, secret files, anything an ignore file or the config names,
// symlinks, media, binaries and files over max_file_mb.

package index

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The reasons a file or folder gets skipped. Report.Skipped counts by these
// names, and the debug log line for each skip carries one.
const (
	ReasonHidden      = "hidden"       // the name starts with "."
	ReasonBuildFolder = "build-folder" // node_modules, .venv, vendor, target, dist, build, __pycache__
	ReasonSecret      = "secret"       // .env, keys, certificates, password stores
	ReasonIgnored     = "ignored"      // a .gitignore, a .meruignore or [index] ignore
	ReasonSymlink     = "symlink"      // never followed, so never out of the folder
	ReasonMedia       = "media"        // images, audio, video, archives
	ReasonBinary      = "binary"       // executables, databases, or a NUL byte in the first 8 KB
	ReasonUnsupported = "unsupported"  // an extension the indexer has no chunker for
	ReasonTooLarge    = "too-large"    // bigger than max_file_mb
)

// The kinds of file the indexer reads. They match store.Document.Kind.
const (
	KindMarkdown = "markdown"
	KindText     = "text"
	KindCode     = "code"
	KindHTML     = "html"
	KindPDF      = "pdf"
)

// ignoreFiles are the per-folder rule files, read in this order. A later
// file wins where both match, so .meruignore can re-include ("!name") what
// .gitignore leaves out.
var ignoreFiles = []string{".gitignore", ".meruignore"}

// buildFolders hold dependencies or build output: large, generated, and
// rarely what you want an answer from.
var buildFolders = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "vendor": true,
	"target": true, "dist": true, "build": true, "__pycache__": true,
}

// secretNames are glob patterns for files that hold keys, passwords or
// tokens. They are checked before everything else and nothing can
// re-include them: a secret in the index would reach the model's prompt.
// Matching ignores case, so ID_RSA counts too.
var secretNames = []string{
	".env*", "*.pem", "*.key", "*.p12", "*.pfx",
	"id_rsa*", "id_ed25519*", "id_ecdsa*", "*.kdbx",
	"secrets.*", "credentials*", ".netrc", ".npmrc", ".pypirc",
}

// kinds maps a lower-case file extension to the kind of file it holds.
// Anything missing here is skipped as unsupported. Office formats come
// later (see ROADMAP.md).
var kinds = map[string]string{
	".md": KindMarkdown, ".markdown": KindMarkdown,
	".txt": KindText, ".rst": KindText, ".org": KindText,
	".html": KindHTML, ".htm": KindHTML,
	".pdf": KindPDF,
	".go":  KindCode, ".py": KindCode, ".js": KindCode, ".ts": KindCode,
	".tsx": KindCode, ".jsx": KindCode, ".java": KindCode, ".rb": KindCode,
	".rs": KindCode, ".c": KindCode, ".h": KindCode, ".cpp": KindCode,
	".cs": KindCode, ".swift": KindCode, ".kt": KindCode, ".sh": KindCode,
	".sql": KindCode, ".yaml": KindCode, ".yml": KindCode, ".toml": KindCode,
	".json": KindCode,
}

// mediaExts are images, audio, video and archives. Only the reason differs
// from an unsupported extension; naming them makes the report readable.
var mediaExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".tif": true, ".tiff": true, ".webp": true, ".heic": true, ".ico": true,
	".svg": true, ".psd": true, ".raw": true,
	".mp3": true, ".wav": true, ".flac": true, ".aac": true, ".m4a": true,
	".ogg": true, ".opus": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true,
	".m4v": true, ".wmv": true,
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true,
	".xz": true, ".zst": true, ".7z": true, ".rar": true, ".dmg": true,
	".iso": true,
}

// binaryExts are compiled programs, libraries and databases.
var binaryExts = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".o": true,
	".a": true, ".class": true, ".jar": true, ".pyc": true, ".wasm": true,
	".bin": true, ".db": true, ".sqlite": true, ".sqlite3": true,
}

// binarySniffBytes is how much of a file the NUL-byte test reads. Git uses
// the same 8 KB to decide whether a file is binary.
const binarySniffBytes = 8 << 10

// IsSecret reports whether name, a file name without its folder, looks
// like a file that holds a secret. merud also asks it before it copies a
// file the user attaches in the desktop app, so the two rules can't drift.
func IsSecret(name string) bool {
	lower := strings.ToLower(name)
	for _, glob := range secretNames {
		// path.Match fails only on a malformed glob, and these are fixed.
		if ok, _ := path.Match(glob, lower); ok {
			return true
		}
	}
	return false
}

// kindOf returns the kind of file name holds, from its extension. When the
// indexer has no chunker for it, kind is empty and reason says why.
func kindOf(name string) (kind, reason string) {
	ext := strings.ToLower(filepath.Ext(name))
	if k, ok := kinds[ext]; ok {
		return k, ""
	}
	switch {
	case mediaExts[ext]:
		return "", ReasonMedia
	case binaryExts[ext]:
		return "", ReasonBinary
	default:
		return "", ReasonUnsupported
	}
}

// looksBinary reports whether data has a NUL byte in its first 8 KB. Text
// files never hold one; almost every binary format does.
func looksBinary(data []byte) bool {
	if len(data) > binarySniffBytes {
		data = data[:binarySniffBytes]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// skipReason decides whether to skip the entry at p, found under root. It
// checks what the name, the type and the ignore rules can tell; size and
// content checks come later, in indexFile. It returns "" to keep the entry.
//
// It checks only p itself, not the folders above it. The walk never enters
// a skipped folder, so that is enough there; skipPath covers a lone path.
func (ix *Indexer) skipReason(root, p string, mode fs.FileMode) string {
	name := filepath.Base(p)
	isDir := mode.IsDir()
	// A symlink could point anywhere, including out of the folder. The
	// indexer never follows one; whatever it points to inside the folder
	// gets indexed under its real path.
	if mode&fs.ModeSymlink != 0 {
		return ReasonSymlink
	}
	if !isDir && IsSecret(name) {
		return ReasonSecret
	}
	if strings.HasPrefix(name, ".") {
		return ReasonHidden
	}
	if isDir && buildFolders[name] {
		return ReasonBuildFolder
	}
	if ix.ignored(root, p, isDir) {
		return ReasonIgnored
	}
	if !isDir && !mode.IsRegular() {
		// Sockets, pipes and devices: reading one could block forever.
		return ReasonUnsupported
	}
	if !isDir {
		if _, reason := ix.kindIn(root, name); reason != "" {
			return reason
		}
	}
	return ""
}

// skipPath is skipReason for a path that didn't come from a walk, such as a
// watch event: it also checks every folder between root and p, because a
// file inside a skipped folder is skipped too.
func (ix *Indexer) skipPath(root, p string, mode fs.FileMode) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return ""
	}
	dir := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if err != nil {
			return ReasonUnsupported
		}
		if reason := ix.skipReason(root, dir, info.Mode()); reason != "" {
			return reason
		}
	}
	return ix.skipReason(root, p, mode)
}

// ignored reports whether the config's ignore list or an ignore file in a
// folder between root and p excludes p.
//
// The config's patterns come first and nothing re-includes what they name.
// Then each folder's .gitignore and .meruignore apply from root downwards,
// and the last matching pattern wins, so a deeper file overrides a shallower
// one, as in git.
func (ix *Indexer) ignored(root, p string, isDir bool) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if m, ig := matchPatterns(ix.ignore, rel, isDir); m && ig {
		return true
	}

	ignored := false
	dir, dirRel := root, ""
	parts := strings.Split(rel, "/")
	// One pass per folder from root down to p's parent. relFromDir is p's
	// path relative to the folder whose rules apply.
	for i := 0; i < len(parts); i++ {
		relFromDir := strings.TrimPrefix(rel, dirRel)
		if m, ig := matchPatterns(ix.rulesFor(dir), relFromDir, isDir); m {
			ignored = ig
		}
		if i == len(parts)-1 {
			break
		}
		dir = filepath.Join(dir, parts[i])
		dirRel += parts[i] + "/"
	}
	return ignored
}

// rulesFor returns the patterns from dir's .gitignore and .meruignore, in
// that order, reading them the first time and caching them after. A missing
// file means no rules. A bad line is logged and dropped: one typo in a
// .gitignore shouldn't stop the folder from being indexed.
func (ix *Indexer) rulesFor(dir string) []pattern {
	ix.rulesMu.Lock()
	// defer runs the Unlock when rulesFor returns, on every path.
	defer ix.rulesMu.Unlock()
	if ps, ok := ix.rules[dir]; ok {
		return ps
	}
	var ps []pattern
	for _, name := range ignoreFiles {
		lines, err := readRuleFile(filepath.Join(dir, name))
		if err != nil {
			ix.log.Debug("index: can't read ignore file", "path", filepath.Join(dir, name), "err", err)
			continue
		}
		got, errs := parsePatterns(lines)
		for _, err := range errs {
			ix.log.Debug("index: bad ignore pattern", "path", filepath.Join(dir, name), "err", err)
		}
		ps = append(ps, got...)
	}
	ix.rules[dir] = ps
	return ps
}

// forgetRules drops the cached ignore rules of dir and every folder under
// it, so the next check reads them again. The watcher calls it when a
// .gitignore or .meruignore changes, and every walk calls it for the folder
// it walks, so a scan never works from rules older than the scan.
func (ix *Indexer) forgetRules(dir string) {
	ix.rulesMu.Lock()
	defer ix.rulesMu.Unlock()
	for d := range ix.rules {
		if within(dir, d) {
			delete(ix.rules, d)
		}
	}
}

// maxRuleFileBytes caps how much of one ignore file the indexer reads. Real
// ones are a few KB; the cap keeps a huge file named .gitignore harmless.
const maxRuleFileBytes = 1 << 20

// readRuleFile returns the lines of the ignore file at p, or nothing when
// there is no such file. It refuses symlinks and files over 1 MB.
func readRuleFile(p string) ([]string, error) {
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRuleFileBytes {
		return nil, nil
	}
	data, err := os.ReadFile(p) // #nosec G304 -- an ignore file inside a configured folder
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}
