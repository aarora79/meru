// This file holds the helpers every policy test shares: finding the module
// root, parsing each Go file in it, reading the deny-lists, and the small
// matchers that decide whether an import path or a URL breaks a rule. The
// checks themselves are plain functions that return findings, so the tests
// can run them on the real module and on small made-up sources alike.

package policy

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sourceFile is one parsed Go file. rel is its path from the module root with
// forward slashes, so failure messages read the same on every OS.
type sourceFile struct {
	rel    string
	isTest bool
	fset   *token.FileSet
	file   *ast.File
}

// where turns a position inside f into "path/to/file.go:12", the form editors
// and terminals turn into a clickable link.
func (f sourceFile) where(pos token.Pos) string {
	return fmt.Sprintf("%s:%d", f.rel, f.fset.Position(pos).Line)
}

// parseSource parses src as the Go file at rel. It fails when src isn't valid
// Go. Comments are kept out: the checks read imports and string literals only.
func parseSource(rel string, src []byte) (sourceFile, error) {
	fset := token.NewFileSet()
	// SkipObjectResolution skips work the checks don't need.
	file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return sourceFile{}, err
	}
	return sourceFile{
		rel:    rel,
		isTest: strings.HasSuffix(rel, "_test.go"),
		fset:   fset,
		file:   file,
	}, nil
}

// moduleRoot walks up from the test's working directory (the package
// directory, which `go test` sets) until it finds go.mod. It stops the test
// when there is none.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("find working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// modulePath returns the module's import path from the "module" line of
// go.mod, for example "github.com/aarora79/meru".
func modulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// moduleFiles parses every .go file that belongs to the module at root.
//
// It skips the directories the go command itself ignores (testdata, vendor,
// and names starting with "." or "_", which also keeps out .git and the
// agent worktrees under .claude), plus any nested directory with its own
// go.mod, because that is a different module. Files behind build tags such as
// e2e are included: the rules hold for every build.
func moduleFiles(t *testing.T, root string) []sourceFile {
	t.Helper()
	var files []sourceFile
	// WalkDir calls the function once for every file and directory under root.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parseSource(filepath.ToSlash(rel), src)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatalf("scan module: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no Go files; is the module root right?")
	}
	return files
}

// readList reads a deny-list or allow-list file: one entry per line, with
// blank lines and anything after "#" ignored.
func readList(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open list: %v", err)
	}
	// defer runs f.Close() when readList returns, however it returns.
	defer f.Close()

	var entries []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		if line = strings.TrimSpace(line); line != "" {
			entries = append(entries, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read list %s: %v", path, err)
	}
	return entries
}

// matchModule reports which prefix in prefixes covers importPath, if any. A
// prefix covers a path when the two are equal or the path continues with "/",
// so "github.com/openai" covers "github.com/openai/openai-go" but not
// "github.com/openaix".
func matchModule(importPath string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if importPath == p || strings.HasPrefix(importPath, p+"/") {
			return p, true
		}
	}
	return "", false
}

// literal is one string literal from a Go file, already unquoted.
type literal struct {
	pos   token.Pos
	value string
}

// stringLiterals returns every string literal in f, in source order. It
// includes struct tags and import paths, which are string literals too, but
// neither ever holds a URL, so the checks don't need to tell them apart.
func stringLiterals(f sourceFile) []literal {
	var out []literal
	// ast.Inspect visits every node in the syntax tree. Returning true means
	// "keep going into this node's children".
	ast.Inspect(f.file, func(n ast.Node) bool {
		// n.(*ast.BasicLit) is a type assertion: ok is true only when n is a
		// literal such as 42, 'x' or "text".
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			value = lit.Value // can't happen for code that parsed; keep the raw text
		}
		out = append(out, literal{pos: lit.Pos(), value: value})
		return true
	})
	return out
}

// urlPattern finds URLs with a scheme that makes a network call. It stops at
// whitespace, quotes and ")", which end a URL inside prose. It keeps "[" and
// "]" because an IPv6 host such as [::1] uses them.
var urlPattern = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>` + "`" + `)]*`)

// urlHost pulls the host out of a URL such as "http://user@host:8080/path",
// returning "host". It returns "" when the URL has no host.
func urlHost(raw string) string {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	// The authority part ends at the first "/", "?" or "#".
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if strings.HasPrefix(rest, "[") {
		// An IPv6 address in brackets, such as [::1]:11434.
		if end := strings.Index(rest, "]"); end >= 0 {
			return rest[1:end]
		}
		return rest
	}
	if host, _, err := net.SplitHostPort(rest); err == nil {
		return host
	}
	return rest
}

// dynamicHost reports whether host is a placeholder filled in at run time,
// such as "%s" in a format string or "{host}" in a template. The check can't
// judge those, so it lets them pass.
func dynamicHost(host string) bool {
	return host == "" || strings.ContainsAny(host, "%{}$<>")
}

// safeHost reports whether host is loopback or a name reserved for examples
// (RFC 2606 and RFC 6761), where no real server can answer.
func safeHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	for _, name := range []string{"example.com", "example.net", "example.org"} {
		if h == name || strings.HasSuffix(h, "."+name) {
			return true
		}
	}
	for _, tld := range []string{".example", ".test", ".invalid"} {
		if strings.HasSuffix(h, tld) {
			return true
		}
	}
	return false
}

// deniedImports lists every import in files that a prefix in denied covers.
// what names the kind of library, for the message.
func deniedImports(files []sourceFile, denied []string, what string) []string {
	var findings []string
	for _, f := range files {
		for _, imp := range f.file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if prefix, ok := matchModule(path, denied); ok {
				findings = append(findings, fmt.Sprintf("%s: imports %q, a %s (deny-list entry %q)",
					f.where(imp.Pos()), path, what, prefix))
			}
		}
	}
	return findings
}

// goModRequires returns the module paths go.mod requires, from both the
// one-line form (require x v1) and the block form (require ( ... )). It reads
// the file by hand so the policy tests need no extra module.
func goModRequires(gomod string) []string {
	var mods []string
	inBlock := false
	for line := range strings.Lines(gomod) {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
		case inBlock && fields[0] == ")":
			inBlock = false
		case inBlock:
			mods = append(mods, strings.Trim(fields[0], `"`))
		case fields[0] == "require" && len(fields) >= 2 && fields[1] == "(":
			inBlock = true
		case fields[0] == "require" && len(fields) >= 3:
			mods = append(mods, strings.Trim(fields[1], `"`))
		}
	}
	return mods
}

// providerHostLiterals lists every string literal in the non-test files that
// names a cloud-model API host from hosts.
//
// Test files are skipped on purpose: a config test may need a provider URL to
// prove that config refuses it. Test code never ships in a binary.
func providerHostLiterals(files []sourceFile, hosts []string) []string {
	var findings []string
	for _, f := range files {
		if f.isTest {
			continue
		}
		for _, lit := range stringLiterals(f) {
			lower := strings.ToLower(lit.value)
			for _, h := range hosts {
				if strings.Contains(lower, strings.ToLower(h)) {
					findings = append(findings, fmt.Sprintf("%s: string literal names cloud-model host %q", f.where(lit.pos), h))
				}
			}
		}
	}
	return findings
}

// nonLoopbackURLs lists every http(s) or ws(s) URL inside a string literal in
// the non-test files whose host isn't loopback or reserved, unless it starts
// with an entry in allowed. Test files are skipped for the same reason as in
// providerHostLiterals.
func nonLoopbackURLs(files []sourceFile, allowed []string) []string {
	var findings []string
	for _, f := range files {
		if f.isTest {
			continue
		}
		for _, lit := range stringLiterals(f) {
			for _, u := range urlPattern.FindAllString(lit.value, -1) {
				host := urlHost(u)
				if dynamicHost(host) || safeHost(host) || hasAnyPrefix(u, allowed) {
					continue
				}
				findings = append(findings, fmt.Sprintf("%s: string literal holds %q, whose host %q isn't loopback", f.where(lit.pos), u, host))
			}
		}
	}
	return findings
}

// hasAnyPrefix reports whether s starts with one of prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
