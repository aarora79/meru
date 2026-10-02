// This file tests how web_fetch hands a JavaScript page to the page
// reader: the check that spots an empty shell, the rendered text, the
// fallback when rendering fails, [web] render = "off", and the progress
// line a first render sends. The renderer is a fake; no browser runs.

package builtin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/render"
)

// fakeRenderer returns html for every page, or fails with err. ready is
// what Ready reports; calls counts Render calls.
type fakeRenderer struct {
	html  string
	err   error
	ready bool
	calls int
}

// Render records the call, sends the install line when not ready, and
// returns f.html or f.err.
func (f *fakeRenderer) Render(ctx context.Context, rawURL string, progress func(string)) (render.Page, error) {
	f.calls++
	if !f.ready && progress != nil {
		progress(render.InstallLine)
	}
	if f.err != nil {
		return render.Page{}, f.err
	}
	return render.Page{URL: rawURL, HTML: f.html, Requests: 3}, nil
}

// Ready reports f.ready.
func (f *fakeRenderer) Ready() bool { return f.ready }

// shellMux serves /job, an empty shell whose script would fill the page,
// and /article, a page with its text in the HTML.
func shellMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/job", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title></title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`)
	})
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><p>"+strings.Repeat("A real article about Go. ", 60)+"</p><script>track()</script></body></html>")
	})
	return mux
}

// renderedJob is the HTML the fake renderer returns for the job page.
const renderedJob = `<html><head><title>Principal Engineer</title></head><body><h1>Principal Engineer</h1><p>Denver, CO. Full time. R73070.</p></body></html>`

func TestNeedsRender(t *testing.T) {
	tests := []struct {
		name string
		page fetched
		body string
		want bool
	}{
		{"empty shell with a script", fetched{kind: "HTML", text: ""}, `<div id=root></div><script src=a.js></script>`, true},
		{"shell in upper case", fetched{kind: "HTML", text: "Skip to main content"}, `<SCRIPT>x()</SCRIPT>`, true},
		{"article", fetched{kind: "HTML", text: strings.Repeat("word ", 200)}, `<p>...</p><script></script>`, false},
		{"short page, no script", fetched{kind: "HTML", text: "Hello"}, `<p>Hello</p>`, false},
		{"PDF", fetched{kind: "PDF", text: ""}, `<script>`, false},
		{"plain text", fetched{kind: "plain text", text: ""}, `<script>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsRender(tt.page, []byte(tt.body)); got != tt.want {
				t.Errorf("needsRender = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebFetchRenders(t *testing.T) {
	srv, tools := pageServer(t, shellMux())
	tools.web.render = config.RenderAuto
	r := &fakeRenderer{html: renderedJob, ready: true}
	tools.UseRenderer(r)

	text, isErr := call(t, tools, WebFetch, `{"url":"`+srv.URL+`/job"}`)
	if isErr {
		t.Fatalf("web_fetch failed: %s", text)
	}
	for _, want := range []string{"HTML, rendered", "Title: Principal Engineer", "Denver, CO. Full time. R73070."} {
		if !strings.Contains(text, want) {
			t.Errorf("result lacks %q:\n%s", want, text)
		}
	}

	// A page with its text in the HTML never reaches the renderer.
	text, _ = call(t, tools, WebFetch, `{"url":"`+srv.URL+`/article"}`)
	if r.calls != 1 || strings.Contains(text, "rendered") {
		t.Errorf("the article went to the renderer (%d calls):\n%.200s", r.calls, text)
	}
}

func TestWebFetchRenderFails(t *testing.T) {
	srv, tools := pageServer(t, shellMux())
	tools.web.render = config.RenderAuto
	tools.UseRenderer(&fakeRenderer{err: errors.New("the page reader couldn't load the page"), ready: true})
	text, isErr := call(t, tools, WebFetch, `{"url":"`+srv.URL+`/job"}`)
	if isErr {
		t.Fatalf("a failed render made web_fetch fail: %s", text)
	}
	if !strings.Contains(text, "builds its text with JavaScript") || !strings.Contains(text, "the page reader couldn't load the page") {
		t.Errorf("result doesn't say why the page is empty:\n%s", text)
	}
}

func TestRenderOff(t *testing.T) {
	srv, tools := pageServer(t, shellMux())
	tools.web.render = config.RenderOff
	r := &fakeRenderer{html: renderedJob, ready: true}
	tools.UseRenderer(r)
	if text, _ := call(t, tools, WebFetch, `{"url":"`+srv.URL+`/job"}`); r.calls != 0 || strings.Contains(text, "rendered") {
		t.Errorf("render = off still rendered:\n%s", text)
	}
}

// TestRenderProgress checks that a first render's install line reaches
// the client through dispatch.Progress.
func TestRenderProgress(t *testing.T) {
	srv, tools := pageServer(t, shellMux())
	tools.web.render = config.RenderAuto
	tools.UseRenderer(&fakeRenderer{html: renderedJob, ready: false})
	var lines []string
	ctx := dispatch.WithProgress(context.Background(), func(s string) { lines = append(lines, s) })
	res, err := tools.Call(ctx, WebFetch, []byte(`{"url":"`+srv.URL+`/job"}`))
	if err != nil || res.IsError {
		t.Fatalf("web_fetch: %v %s", err, res.Text)
	}
	if len(lines) != 1 || lines[0] != render.InstallLine {
		t.Errorf("progress lines = %q, want the install line", lines)
	}
}
