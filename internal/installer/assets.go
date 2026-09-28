// This file serves the installer's page: its own HTML, CSS and JavaScript
// under web/, embedded in the binary. Anything web/ lacks, such as the
// fonts, the logo and app.css with Meru's colours, comes from the handler
// the window passes in, which serves Meru.app's page. So the installer
// looks like Meru.app without a second copy of its files.

package installer

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// web holds the page's files. See docs/coding-notes/go-basics/embed.md.
//
//go:embed web
var web embed.FS

// ContentSecurityPolicy tells the WebView what the page may load and run:
// only the app's own files, and nothing from the network. It matches
// Meru.app's policy (internal/desktop), which a test checks.
const ContentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"font-src 'self'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// Assets returns the handler for the page. A path web/ holds is served
// from there; any other path goes to fallback, which may be nil. Every
// response carries the Content-Security-Policy, nosniff and no-referrer.
// It panics only if the embedded folder is missing, a build mistake.
func Assets(fallback http.Handler) http.Handler {
	root, err := fs.Sub(web, "web")
	if err != nil {
		panic("installer: the embedded web folder is missing: " + err.Error())
	}
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(root, name); err == nil || fallback == nil {
			files.ServeHTTP(w, r)
			return
		}
		fallback.ServeHTTP(w, r)
	})
}
