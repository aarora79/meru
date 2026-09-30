// This file holds the pinning rule from ARCHITECTURE.md, "Connectors and
// the supervisor": every program Meru installs or tells the user to
// install names one exact version, and every container image names its
// digest. "latest", "@latest", ":latest", a version range, a package run
// with no version, a container image with no digest and a file fetched
// from a branch all fail the test.
//
// It reads the connector manifests, the Go string literals in the
// installer, the catalog and cmd/meru, every file in scripts/ and deploy/,
// and the code blocks of every Markdown file under docs/ and deploy/.
// Prose outside a code block may say "latest".
//
// Some places break the rule on purpose today and keep working as they
// do; pinOffenders lists each one. The list only shrinks: a stale entry
// fails the test too.

package policy

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/connectors"
)

// pinRule is one way to leave a version open, with the message a finding
// shows.
type pinRule struct {
	re   *regexp.Regexp
	what string
}

// pinRules are the patterns the test refuses. Each matches within one
// line.
var pinRules = []pinRule{
	{regexp.MustCompile(`@latest\b`), "@latest takes whatever is newest"},
	{regexp.MustCompile(`:latest\b`), ":latest takes whatever is newest"},
	{regexp.MustCompile(`(?i)\bversion\s*[:=]\s*["']?latest\b`), "a version of latest takes whatever is newest"},
	// A version range: npm's ^ and ~, or Python's >=, ~= and >.
	{regexp.MustCompile(`@[\^~]\d|[A-Za-z0-9](>=|~=|>)\d`), "a version range can pick a new release"},
	// A registry image, or an image: key, with no digest after it.
	{regexp.MustCompile(`\b(docker\.io|ghcr\.io|quay\.io)/[^\s"'` + "`" + `]+|^\s*image\s*[:=]\s*\S+`), "a container image needs @sha256:<digest>"},
	// A package runner that starts a command, followed by a package name
	// with no exact version. The runner must open the line, or follow "$",
	// a quote, exec, -- or &&, with any NAME=value settings before it, so
	// prose such as "an npx or uvx server" passes.
	{regexp.MustCompile(`(^|[$"]\s*|\bexec\s+|--\s+|&&\s+)([A-Z_]+=\S*\s+)*(uvx|npx|bunx|pipx run)\s+(-y\s+|--yes\s+)?[A-Za-z@][^\s"'` + "`" + `]*`),
		"the runner takes the newest release; name an exact version"},
	// A file fetched from a branch, which changes under the same URL.
	{regexp.MustCompile(`raw\.githubusercontent\.com/[^/\s]+/[^/\s]+/(master|main|HEAD)/`), "a branch URL changes; fetch from a tag or commit"},
}

// exactRunnerVersion is what makes a runner line pinned: the package ends
// in @<x.y.z> (npx, uvx) or ==<x.y.z> (pipx, uvx).
var exactRunnerVersion = regexp.MustCompile(`(@|==)v?\d+\.\d+\.\d+\S*$`)

// meruReleases is Meru's own "newest release" link. It isn't a
// dependency: it is how a user gets Meru, and each release is built and
// signed off by its owner. The check removes it before matching.
var meruReleases = regexp.MustCompile(`github\.com/aarora79/meru/releases/latest`)

// pinFinding is one line that breaks a rule.
type pinFinding struct {
	where string // "path:line"
	text  string // the line, trimmed
	what  string // the rule's message
}

// pinOffender is one known place that leaves a version open on purpose
// today. file is the path from the module root; match is a piece of the
// offending line.
type pinOffender struct {
	file, match, why string
}

// pinOffenders lists the existing places that break the pinning rule.
// Each keeps working as it does today until the change named in why
// replaces it, and then its entry goes. Add nothing here for new code.
// Step 5 of #87 took off the Google start commands in the catalog, the
// installer and the docs: each now names workspace-mcp 1.30.0, the
// Google connector's pin. Step 6 took off the installer's SearXNG
// container: merud's connector runs the pinned image now.
var pinOffenders = []pinOffender{
	// Ollama names a model with no tag "<name>:latest". That is a model
	// tag, not a program version, and Ollama itself is the user's to
	// update, so it stays.
	{"internal/installer/ollama.go", ":latest", "an Ollama model tag, not a program version"},
	// An example of adding any server by hand, with a made-up name.
	{"docs/running.md", "npx -y some-mcp", "a made-up server name in an example of meru mcp add"},
	// The local Grafana stack, which only the owner runs to watch merud.
	{"deploy/observability/compose.yaml", "grafana/otel-lgtm:latest", "the observability stack is a developer tool; pinning it is a separate change"},
}

// TestManifestsPinned loads the embedded connector manifests, whose
// Validate refuses an open version or an image with no digest.
func TestManifestsPinned(t *testing.T) {
	if _, err := connectors.Load(); err != nil {
		t.Fatalf("connector manifests: %v", err)
	}
}

// TestNoUnpinnedVersions scans the files the package comment lists and
// fails on each finding no offender covers, and on each offender that
// covers nothing.
func TestNoUnpinnedVersions(t *testing.T) {
	root := moduleRoot(t)
	findings := scanPins(t, root)
	used := make([]bool, len(pinOffenders))
	for _, f := range findings {
		i := offenderFor(f)
		if i < 0 {
			t.Errorf("%s: %s: %s (ARCHITECTURE.md, \"Connectors and the supervisor\")", f.where, f.what, f.text)
			continue
		}
		used[i] = true
	}
	for i, o := range pinOffenders {
		if !used[i] {
			t.Errorf("pinOffenders lists %q in %s, which is gone; take the entry out", o.match, o.file)
		}
	}
}

// offenderFor returns the index of the offender that covers f, or -1.
func offenderFor(f pinFinding) int {
	file, _, _ := strings.Cut(f.where, ":")
	for i, o := range pinOffenders {
		if o.file == file && strings.Contains(f.text, o.match) {
			return i
		}
	}
	return -1
}

// scanPins reads every file in scope under root and returns what breaks a
// pin rule.
func scanPins(t *testing.T, root string) []pinFinding {
	t.Helper()
	var out []pinFinding

	// Manifests: every line that isn't a comment.
	manifests, err := filepath.Glob(filepath.Join(root, "internal", "connectors", "manifests", "*.toml"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("found no connector manifests (%v); the scan is broken", err)
	}
	for _, path := range manifests {
		out = append(out, pinLines(rel(t, root, path), readFile(t, path), false, 1)...)
	}

	// Go string literals in the packages that write install commands.
	goDirs := []string{"internal/installer", "internal/catalog", "cmd/meru", "cmd/meru-installer"}
	seen := 0
	for _, f := range moduleFiles(t, root) {
		if f.isTest || !inDirs(f.rel, goDirs) {
			continue
		}
		seen++
		for _, lit := range stringLiterals(f) {
			line := f.fset.Position(lit.pos).Line
			out = append(out, pinLines(f.rel, lit.value, true, line)...)
		}
	}
	if seen == 0 {
		t.Fatalf("found no Go files in %v; the scan is broken", goDirs)
	}

	// scripts/ and deploy/: every line of every file; the code blocks of
	// a Markdown file. docs/: the code blocks of each Markdown file.
	for _, dir := range []string{"scripts", "deploy", "docs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			r := rel(t, root, path)
			switch {
			case strings.HasSuffix(path, ".md"):
				out = append(out, pinLines(r, codeBlocks(readFile(t, path)), true, 1)...)
			case dir != "docs":
				out = append(out, pinLines(r, readFile(t, path), true, 1)...)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}
	return out
}

// pinLines checks each line of text, whose first line is line first of
// file, and returns what breaks a rule. With comments false, lines that
// start with "#" are skipped.
func pinLines(file, text string, comments bool, first int) []pinFinding {
	var out []pinFinding
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !comments && strings.HasPrefix(trimmed, "#") {
			continue
		}
		clean := meruReleases.ReplaceAllString(trimmed, "")
		for _, r := range pinRules {
			if what, ok := breaks(r, clean); ok {
				out = append(out, pinFinding{where: file + ":" + strconv.Itoa(first+i), text: trimmed, what: what})
				break
			}
		}
	}
	return out
}

// breaks reports whether line breaks rule r, and the message to show.
// An image with a digest, and a runner with an exact version, pass.
func breaks(r pinRule, line string) (string, bool) {
	for _, m := range r.re.FindAllString(line, -1) {
		switch {
		case strings.Contains(r.what, "container image") && strings.Contains(m, "@sha256:"):
		case strings.Contains(r.what, "runner") && exactRunnerVersion.MatchString(m):
		default:
			return r.what, true
		}
	}
	return "", false
}

// codeBlocks returns the lines inside the ``` fences of a Markdown file,
// with every other line blanked, so line numbers stay right.
func codeBlocks(md string) string {
	lines := strings.Split(md, "\n")
	in := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			in = !in
			lines[i] = ""
			continue
		}
		if !in {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// readFile returns a file's text, and stops the test when it can't.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// rel returns path relative to root, with forward slashes.
func rel(t *testing.T, root, path string) string {
	t.Helper()
	r, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relative path of %s: %v", path, err)
	}
	return filepath.ToSlash(r)
}

// TestPinRulesCatch runs the rules on made-up lines, so the scan can't
// pass by matching nothing.
func TestPinRulesCatch(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		line string
		bad  bool
	}{
		{"npx -y obsidian-mcp@latest serve", true},
		{"npx -y obsidian-mcp serve", true},
		{"npx -y obsidian-mcp@2 serve", true},
		{"npx -y obsidian-mcp@2.0.1 serve", false},
		{"uvx workspace-mcp --transport streamable-http", true},
		{"uvx workspace-mcp==1.30.0 --transport streamable-http", false},
		{"uvx --version", false},
		{"docker pull docker.io/searxng/searxng:latest", true},
		{"docker pull docker.io/searxng/searxng:2026.9.29", true},
		{"docker pull docker.io/searxng/searxng:2026.9.29" + digest, false},
		{"    image: grafana/otel-lgtm:0.8.1", true},
		{"    image: grafana/otel-lgtm" + digest, false},
		{`version = "latest"`, true},
		{`version = "2.0.1"`, false},
		{"npm install obsidian-mcp@^2.0.0", true},
		{"pip install workspace-mcp>=1.30", true},
		{"curl -O https://raw.githubusercontent.com/searxng/searxng/master/container/.env", true},
		{"curl -O https://raw.githubusercontent.com/searxng/searxng/v2026.9.29/container/.env", false},
		{"curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash", false},
		{"the latest release of Go", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := len(pinLines("x", tt.line, true, 1)) > 0
			if got != tt.bad {
				t.Errorf("flagged = %v, want %v", got, tt.bad)
			}
		})
	}
}
