// This file serves the page: the HTML, CSS, JavaScript, fonts and the two
// vendored libraries under web/, embedded in the binary, each response
// sent with a strict Content-Security-Policy.

package desktop

import (
	"embed"
	"io/fs"
	"net/http"
)

// web holds the page's files. The //go:embed line tells the compiler to
// copy the web folder into the binary, so the app needs no files on disk
// and works offline. See docs/coding-notes/go-basics/embed.md.
//
//go:embed web
var web embed.FS

// ContentSecurityPolicy tells the WebView what the page may load and run.
// Model output is untrusted, so the policy assumes a bad answer got past
// the sanitizer and limits what it could do:
//
//   - default-src 'none' allows nothing that a later rule doesn't name.
//   - script-src 'self' runs only the app's own files: no inline script,
//     no eval, nothing from the network.
//   - style-src, font-src and img-src 'self' keep styles, fonts and
//     images local; img-src adds data: for inline icons. No remote image
//     can load, so an answer can't use one to report that it was read.
//   - connect-src 'self' lets Wails' runtime reach Go and nothing else.
//   - base-uri, form-action and frame-ancestors 'none' close the other
//     ways a page can send or load elsewhere.
const ContentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"font-src 'self'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// Assets returns the handler that serves the page's files from web/, with
// the Content-Security-Policy and two more headers on every response:
// nosniff, so the WebView trusts each file's declared type, and
// no-referrer, so no request names the page it came from. It panics only
// if the embedded folder is missing, a build mistake.
func Assets() http.Handler {
	// fs.Sub makes web/ the root, so the page is at "/" and not "/web/".
	root, err := fs.Sub(web, "web")
	if err != nil {
		panic("desktop: the embedded web folder is missing: " + err.Error())
	}
	files := http.FileServerFS(root)
	// http.HandlerFunc turns a plain function into an http.Handler.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		files.ServeHTTP(w, r)
	})
}
