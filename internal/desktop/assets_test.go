// This file tests the page the app serves: the security headers on every
// file, and rules for the page's own code that keep model output from ever
// becoming markup or script, and keep the page off the network.

package desktop

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestAssetsHeaders(t *testing.T) {
	h := Assets()
	for _, path := range []string{"/", "/app.css", "/js/app.js", "/vendor/purify.es.mjs", "/fonts/newsreader-latin-400-normal.woff2", "/missing.js"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if got := rec.Header().Get("Content-Security-Policy"); got != ContentSecurityPolicy {
				t.Errorf("CSP = %q, want the app's policy", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			wantCode := http.StatusOK
			if path == "/missing.js" {
				wantCode = http.StatusNotFound
			}
			if rec.Code != wantCode {
				t.Errorf("status = %d, want %d", rec.Code, wantCode)
			}
		})
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/js/app.js", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("app.js Content-Type = %q; a module script needs a JavaScript type", ct)
	}
}

// TestPolicyIsStrict checks the parts of the policy the page's safety leans
// on.
func TestPolicyIsStrict(t *testing.T) {
	for _, want := range []string{"default-src 'none'", "script-src 'self';", "connect-src 'self';", "img-src 'self' data:;", "form-action 'none'"} {
		if !strings.Contains(ContentSecurityPolicy, want) {
			t.Errorf("policy lacks %q", want)
		}
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:", "*"} {
		if strings.Contains(ContentSecurityPolicy, bad) {
			t.Errorf("policy holds %q", bad)
		}
	}
}

// TestFixHintMatchesRPC fails when the fix hint the Settings page builds
// drifts from rpc.FixHint, which the terminal clients print. The page
// can't call Go, so it builds the same sentence from the same pieces;
// this test checks each fixed piece appears in settings.js, in order.
func TestFixHintMatchesRPC(t *testing.T) {
	// A hint for connector "ID" and field "FIELD" splits into the fixed
	// text around those two words.
	hint := rpc.FixHint("ID", []string{"FIELD"})
	before, rest, ok := strings.Cut(hint, "FIELD")
	if !ok {
		t.Fatalf("FixHint = %q; it doesn't name the field", hint)
	}
	middle, after, ok := strings.Cut(rest, "ID")
	if !ok {
		t.Fatalf("FixHint = %q; it doesn't name the connector", hint)
	}
	js := ownFiles(t)["web/js/settings.js"]
	want := `"` + before + `" + fix.join(" and ") + "` + middle + `" + c.name + "` + after + `"`
	if !strings.Contains(js, want) {
		t.Errorf("settings.js doesn't build the fix hint as rpc.FixHint does; want the line\n%s", want)
	}
}

// ownFiles returns the page's own HTML, CSS and JavaScript, by path, with
// the vendored libraries left out.
func ownFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := fs.WalkDir(web, "web", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == "web/vendor" {
			return fs.SkipDir
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".css")) {
			return nil
		}
		b, err := fs.ReadFile(web, path)
		if err != nil {
			return err
		}
		files[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 6 {
		t.Fatalf("found %d page files; the walk is broken", len(files))
	}
	return files
}

// TestPageCodeRules fails when the page's own code uses a way to turn a
// string into markup or script, or names a host on the network. The
// sanitizer's DocumentFragment is the only path from model output to the
// DOM; everything else uses textContent.
func TestPageCodeRules(t *testing.T) {
	banned := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{regexp.MustCompile(`\.innerHTML|\.outerHTML|insertAdjacentHTML|document\.write`), "turns a string into markup"},
		{regexp.MustCompile(`\beval\(|new Function\(|setTimeout\(\s*["'\x60]`), "turns a string into script"},
		{regexp.MustCompile(`<script>|<script [^>]*>[^<]`), "inline script, which the policy blocks"},
		{regexp.MustCompile(`\son[a-z]+\s*=\s*["']`), "an inline event handler, which the policy blocks"},
		{regexp.MustCompile(`\sstyle\s*=\s*["']`), "an inline style, which the policy blocks"},
		{regexp.MustCompile(`window\.open\(|location\.(href|assign|replace)`), "navigates the page; links open through Go"},
	}
	// The one URL the page may hold is the SVG namespace, which names a
	// standard and is never fetched.
	url := regexp.MustCompile(`https?://[^\s"'<>)]+`)
	for path, src := range ownFiles(t) {
		for _, b := range banned {
			if loc := b.pattern.FindStringIndex(src); loc != nil {
				t.Errorf("%s: %q %s", path, src[loc[0]:loc[1]], b.why)
			}
		}
		for _, u := range url.FindAllString(src, -1) {
			if u != "http://www.w3.org/2000/svg" {
				t.Errorf("%s names %s; the page must load nothing from the network", path, u)
			}
		}
	}
}

// TestSVGPreviewIsAnImage checks that markdown.js draws an SVG code block
// only as an <img> with a data: URL, where the browser runs no script and
// fetches nothing, and never parses the SVG into the page. A JavaScript
// test runner would check the behaviour; this checks the wiring.
func TestSVGPreviewIsAnImage(t *testing.T) {
	src := ownFiles(t)["web/js/markdown.js"]
	for _, want := range []string{
		`document.createElement("img")`,
		`"data:image/svg+xml;charset=utf-8," + encodeURIComponent(text)`,
		"text.length > maxPreview",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("markdown.js lacks %q", want)
		}
	}
	for _, banned := range []string{"DOMParser", "createElementNS", "<object", "<embed", "<iframe"} {
		if strings.Contains(src, banned) {
			t.Errorf("markdown.js uses %q; an SVG preview must stay an image", banned)
		}
	}
}

// TestMarkdownIsSanitized checks that markdown.js sends every answer
// through DOMPurify, with links limited to http, https and file, and that
// raw HTML in an answer is escaped. A JavaScript test runner would check
// the behaviour; the repository has none, so this checks the wiring.
func TestMarkdownIsSanitized(t *testing.T) {
	src := ownFiles(t)["web/js/markdown.js"]
	for _, want := range []string{
		"DOMPurify.sanitize(html, PURIFY)",
		"ALLOWED_URI_REGEXP: /^(?:https?|file):/i",
		"ALLOW_DATA_ATTR: false",
		"RETURN_DOM_FRAGMENT: true",
		"return escapeHTML(token.text)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("markdown.js lacks %q", want)
		}
	}
	turns := ownFiles(t)["web/js/turns.js"]
	if strings.Count(turns, "renderMarkdown(") != 1 {
		t.Error("turns.js should render Markdown in one place, the finished answer")
	}
}

// TestWorkLabelReadsWhatHappened checks that the work strip's label comes
// from what the turn did, its tool calls and the files it searched, and
// never from the route. A real turn on the "tools" route called no tool,
// and the strip said "Used tools" anyway. The page has no test runner, so
// the test reads workLabel's source.
func TestWorkLabelReadsWhatHappened(t *testing.T) {
	turns := ownFiles(t)["web/js/turns.js"]
	// strings.Cut splits the text at the first match: body is what follows.
	_, body, ok := strings.Cut(turns, "export function workLabel(t) {")
	if !ok {
		t.Fatal("turns.js has no workLabel")
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	for _, want := range []string{"t.steps.length > 0", "t.sources.length > 0", `"Used tools"`} {
		if !strings.Contains(body, want) {
			t.Errorf("workLabel lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "t.route") {
		t.Errorf("workLabel reads t.route; the label must say what the turn did:\n%s", body)
	}
	if !strings.Contains(turns, "const label = workLabel(t);") {
		t.Error("drawStrip doesn't take its label from workLabel")
	}
}
