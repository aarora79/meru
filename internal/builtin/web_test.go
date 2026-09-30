// This file tests the web tools. web_search runs against an httptest
// server that plays SearXNG. web_fetch runs against httptest servers on
// 127.0.0.1, with the dial-time check swapped so that one port counts as
// public; every other address goes through the real checkPublic. No test
// leaves the machine.

package builtin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
)

// webTools returns built-in tools with only the web tools configured.
func webTools(t *testing.T, web config.Web, confirm ...string) *Tools {
	t.Helper()
	if web.MaxResults == 0 {
		web.MaxResults = 8
	}
	return New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: config.BuiltinTools(), Confirm: confirm}, web, nil, "", nil, nil, nil)
}

// call runs one built-in call and returns its text and whether it was an
// error. A Call error, which means the name isn't a built-in, fails the
// test.
func call(t *testing.T, tools *Tools, name, args string) (string, bool) {
	t.Helper()
	res, err := tools.Call(context.Background(), name, []byte(args))
	if err != nil {
		t.Fatalf("Call(%s): %v", name, err)
	}
	return res.Text, res.IsError
}

// searxngJSON is a trimmed SearXNG answer: three results, one with a date
// and a long snippet, and the extra keys SearXNG sends.
const searxngJSON = `{"query":"go release","number_of_results":0,"results":[
 {"url":"https://go.dev/doc/devel/release","title":"Release History - The Go Programming Language","content":"` +
	`Go 1.26 was released on 2026-02-10. LONG","engines":["duckduckgo","bing"],"publishedDate":"2026-02-10T00:00:00","score":4.0},
 {"url":"https://go.dev/blog/go1.26","title":"Go 1.26 is released","content":"Today the Go team released Go 1.26.","engines":["google"],"publishedDate":null},
 {"url":"https://en.wikipedia.org/wiki/Go_(programming_language)","title":"","content":"","engines":["wikipedia"]}
],"answers":[],"infoboxes":[],"suggestions":[],"unresponsive_engines":[]}`

// fakeSearXNG starts a server that answers /search with status and body,
// and records each query string it gets in *got.
func fakeSearXNG(t *testing.T, status int, body string, got *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		if got != nil {
			*got = r.URL.Query()
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebSearch(t *testing.T) {
	long := strings.Repeat("word ", 100)
	json := strings.Replace(searxngJSON, "LONG", long, 1)
	forbidden := "<!doctype html>\n<html lang=en>\n<title>403 Forbidden</title>\n<h1>Forbidden</h1>"
	tests := []struct {
		name    string
		status  int
		body    string
		args    string
		want    []string // pieces of the text
		notWant []string
		isErr   bool
	}{
		{
			name: "results", status: 200, body: json, args: `{"query":"go release"}`,
			want: []string{
				`Web results for "go release", 3 of 3. Cite each result you use by its URL.`,
				"[1] Release History - The Go Programming Language — https://go.dev/doc/devel/release",
				"Published 2026-02-10.",
				"[2] Go 1.26 is released — https://go.dev/blog/go1.26",
				"[3] (no title) — https://en.wikipedia.org/wiki/Go_(programming_language)",
				"…",
			},
			notWant: []string{long},
		},
		{
			name: "max_results trims", status: 200, body: json, args: `{"query":"go release","max_results":1}`,
			want: []string{"1 of 3", "[1] "}, notWant: []string{"[2] "},
		},
		{
			name: "no results", status: 200, body: `{"query":"zzqx","results":[]}`, args: `{"query":"zzqx"}`,
			want: []string{`SearXNG found no results for "zzqx"`},
		},
		{
			name: "json off answers 403 html", status: 403, body: forbidden, args: `{"query":"x"}`,
			want: []string{"JSON is off", "search: formats:", "settings.yml"}, isErr: true,
		},
		{
			name: "html with 200", status: 200, body: "<html><body>results</body></html>", args: `{"query":"x"}`,
			want: []string{"JSON is off"}, isErr: true,
		},
		{
			name: "server error", status: 500, body: `{"error":"boom"}`, args: `{"query":"x"}`,
			want: []string{"SearXNG answered 500"}, isErr: true,
		},
		{
			name: "not json", status: 200, body: `[1,2]`, args: `{"query":"x"}`,
			want: []string{"isn't the JSON Meru expects"}, isErr: true,
		},
		{name: "empty query", status: 200, body: json, args: `{"query":"  "}`, want: []string{"pass query"}, isErr: true},
		{name: "max_results high", status: 200, body: json, args: `{"query":"x","max_results":21}`, want: []string{"out of range"}, isErr: true},
		{name: "max_results negative", status: 200, body: json, args: `{"query":"x","max_results":-1}`, want: []string{"out of range"}, isErr: true},
		{name: "bad time_range", status: 200, body: json, args: `{"query":"x","time_range":"decade"}`, want: []string{`time_range "decade" is unknown`}, isErr: true},
		{name: "unknown key", status: 200, body: json, args: `{"q":"x"}`, want: []string{"aren't a valid JSON object"}, isErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeSearXNG(t, tt.status, tt.body, nil)
			tools := webTools(t, config.Web{SearXNGURL: srv.URL + "/"})
			text, isErr := call(t, tools, WebSearch, tt.args)
			if isErr != tt.isErr {
				t.Fatalf("IsError = %v, want %v:\n%s", isErr, tt.isErr, text)
			}
			for _, w := range tt.want {
				if !strings.Contains(text, w) {
					t.Errorf("text lacks %q:\n%s", w, text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(text, w) {
					t.Errorf("text holds %q:\n%s", w, text)
				}
			}
		})
	}
}

// TestWebSearchQuery checks what reaches SearXNG: the words, format=json,
// and time_range only when the model passed one.
func TestWebSearchQuery(t *testing.T) {
	var got url.Values
	srv := fakeSearXNG(t, 200, searxngJSON, &got)
	tools := webTools(t, config.Web{SearXNGURL: srv.URL})

	if _, isErr := call(t, tools, WebSearch, `{"query":"go 1.26 & more","time_range":"week"}`); isErr {
		t.Fatal("web_search failed")
	}
	if got.Get("q") != "go 1.26 & more" || got.Get("format") != "json" || got.Get("time_range") != "week" {
		t.Errorf("query = %v", got)
	}
	if _, isErr := call(t, tools, WebSearch, `{"query":"go"}`); isErr {
		t.Fatal("web_search failed")
	}
	if got.Has("time_range") {
		t.Errorf("time_range sent when the model passed none: %v", got)
	}
}

// TestWebSearchRefused points web_search at a port nothing listens on.
func TestWebSearchRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close() // the port is now closed
	tools := webTools(t, config.Web{SearXNGURL: addr})
	text, isErr := call(t, tools, WebSearch, `{"query":"x"}`)
	want := "SearXNG isn't answering on " + addr + `. See "Web search" in docs/running.md.`
	if !isErr || !strings.Contains(text, want) {
		t.Errorf("got %v %q, want an error holding %q", isErr, text, want)
	}
}

// pageServer starts an httptest server for web_fetch with a set of
// pages, and returns it with built-in tools whose dial check treats this
// server's port as public. Every other address, including other httptest
// servers on 127.0.0.1, goes through the real checkPublic.
func pageServer(t *testing.T, mux *http.ServeMux) (*httptest.Server, *Tools) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	public := netip.MustParseAddrPort(srv.Listener.Addr().String())
	tools := webTools(t, config.Web{})
	tools.web.allowAddr = func(ap netip.AddrPort) error {
		if ap == public {
			return nil
		}
		return checkPublic(ap.Addr())
	}
	return srv, tools
}

func TestWebFetch(t *testing.T) {
	pdf, err := os.ReadFile(filepath.Join("testdata", "two-pages.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	// private stands for a machine on the user's network: a server on
	// 127.0.0.1 that the check doesn't treat as public.
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "router admin page")
	}))
	t.Cleanup(private.Close)

	longText := strings.Repeat("abcdefghij", 1300) // 13,000 characters
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "Meru/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><head><title>Go 1.26 is released</title><script>track()</script></head>"+
			"<body><h1>Go 1.26</h1><p>Today the Go team released <b>Go 1.26</b>.</p></body></html>")
	})
	mux.HandleFunc("/paper.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	})
	mux.HandleFunc("/notes.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "plain notes\nline two")
	})
	mux.HandleFunc("/long.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, longText)
	})
	mux.HandleFunc("/image.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG"))
	})
	mux.HandleFunc("/huge.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", pageBodyCap+1)))
	})
	mux.HandleFunc("/missing", http.NotFound)
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/notes.txt", http.StatusFound)
	})
	mux.HandleFunc("/to-private", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/admin", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/set-cookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
		http.Redirect(w, r, "/check-cookie", http.StatusFound)
	})
	mux.HandleFunc("/check-cookie", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if _, err := r.Cookie("track"); err == nil {
			fmt.Fprint(w, "cookie came back")
			return
		}
		fmt.Fprint(w, "no cookie")
	})
	srv, tools := pageServer(t, mux)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	tests := []struct {
		name    string
		args    string
		want    []string
		notWant []string
		isErr   bool
	}{
		{
			name: "html", args: `{"url":"` + srv.URL + `/article"}`,
			want: []string{srv.URL + "/article\n", "Title: Go 1.26 is released\n", "HTML. Characters 0 to",
				"Today the Go team released Go 1.26."},
			notWant: []string{"track()", "<p>"},
		},
		{
			name: "pdf", args: `{"url":"` + srv.URL + `/paper.pdf"}`,
			want: []string{"PDF. Characters 0 to", "--- page 1 ---", "--- page 2 ---"},
		},
		{name: "plain text", args: `{"url":"` + srv.URL + `/notes.txt"}`, want: []string{"plain text.", "plain notes\nline two"}},
		{
			name: "first page of a long text", args: `{"url":"` + srv.URL + `/long.txt"}`,
			want: []string{"Characters 0 to 12000 of 13000.", "[1000 more characters. To read on, call web_fetch with offset 12000.]"},
		},
		{
			name: "offset reads on", args: `{"url":"` + srv.URL + `/long.txt","offset":12000}`,
			want: []string{"Characters 12000 to 13000 of 13000."}, notWant: []string{"more characters"},
		},
		{name: "offset past the end", args: `{"url":"` + srv.URL + `/long.txt","offset":13001}`, want: []string{"past the end"}, isErr: true},
		{name: "redirect followed", args: `{"url":"` + srv.URL + `/moved"}`, want: []string{srv.URL + "/notes.txt\n", "plain notes"}},
		{name: "no cookies kept", args: `{"url":"` + srv.URL + `/set-cookie"}`, want: []string{"no cookie"}},
		{name: "content type refused", args: `{"url":"` + srv.URL + `/image.png"}`, want: []string{`"image/png"`, "HTML, PDF and plain text"}, isErr: true},
		{name: "too large", args: `{"url":"` + srv.URL + `/huge.txt"}`, want: []string{"larger than 5.0 MB"}, isErr: true},
		{name: "not found", args: `{"url":"` + srv.URL + `/missing"}`, want: []string{"404"}, isErr: true},
		{name: "redirect loop", args: `{"url":"` + srv.URL + `/loop"}`, want: []string{"stopped after 5 redirects"}, isErr: true},
		{
			name: "loopback address", args: `{"url":"` + private.URL + `/admin"}`,
			want: []string{"refused", "127.0.0.1 is this machine", "only public pages"}, isErr: true,
		},
		{
			name: "redirect to a private address", args: `{"url":"` + srv.URL + `/to-private"}`,
			want: []string{"127.0.0.1 is this machine"}, notWant: []string{"router admin page"}, isErr: true,
		},
		{name: "private network address", args: `{"url":"http://192.168.1.1/"}`, want: []string{"192.168.1.1 is on a private network"}, isErr: true},
		{name: "cloud metadata address", args: `{"url":"http://169.254.169.254/latest/meta-data/"}`, want: []string{"link-local"}, isErr: true},
		{name: "ipv6 loopback", args: `{"url":"http://[::1]:` + port + `/"}`, want: []string{"is this machine"}, isErr: true},
		{name: "not http", args: `{"url":"file:///etc/passwd"}`, want: []string{"must start with http:// or https://"}, isErr: true},
		{name: "no scheme", args: `{"url":"go.dev"}`, want: []string{"must start with http://"}, isErr: true},
		{name: "no host", args: `{"url":"https:///path"}`, want: []string{"has no host"}, isErr: true},
		{name: "negative offset", args: `{"url":"` + srv.URL + `/notes.txt","offset":-1}`, want: []string{"negative"}, isErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := call(t, tools, WebFetch, tt.args)
			if isErr != tt.isErr {
				t.Fatalf("IsError = %v, want %v:\n%s", isErr, tt.isErr, text)
			}
			for _, w := range tt.want {
				if !strings.Contains(text, w) {
					t.Errorf("text lacks %q:\n%.600s", w, text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(text, w) {
					t.Errorf("text holds %q:\n%.600s", w, text)
				}
			}
		})
	}
}

// TestWebFetchNameToLoopback reads a server on 127.0.0.1 through the
// name localhost, with the real dial check in place. The name resolves to
// 127.0.0.1, and the check runs on that resolved address, so the name
// doesn't get the request through.
func TestWebFetchNameToLoopback(t *testing.T) {
	// reached is set from the server's goroutine; atomic.Bool makes that
	// safe to read here.
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		fmt.Fprint(w, "local service")
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	tools := webTools(t, config.Web{})

	text, isErr := call(t, tools, WebFetch, `{"url":"http://localhost:`+port+`/"}`)
	if !isErr || !strings.Contains(text, "is this machine") || reached.Load() {
		t.Errorf("got IsError %v, reached %v, text %q; want a refusal before any request", isErr, reached.Load(), text)
	}
}

// TestCheckPublic pins which addresses count as public.
func TestCheckPublic(t *testing.T) {
	tests := []struct {
		addr   string
		public bool
	}{
		{"8.8.8.8", true},
		{"140.82.112.3", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"127.8.9.10", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"::ffff:192.168.1.1", false},
		{"fd12:3456::1", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"::", false},
		{"100.100.100.100", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := checkPublic(netip.MustParseAddr(tt.addr))
			if (err == nil) != tt.public {
				t.Errorf("checkPublic(%s) = %v, want public=%v", tt.addr, err, tt.public)
			}
		})
	}
}

// TestWebToolsOffered checks which web tools [web] and [builtin] tools
// turn on, what `meru tools` shows, and that they run without asking
// unless [builtin] confirm lists them.
func TestWebToolsOffered(t *testing.T) {
	searxng := config.Web{SearXNGURL: "http://127.0.0.1:8888", MaxResults: 8}
	tests := []struct {
		name  string
		web   config.Web
		tools []string // [builtin] tools
		want  []string
	}{
		{"search and fetch", searxng, config.BuiltinTools(), []string{WebSearch, WebFetch}},
		{"no searxng url", config.Web{MaxResults: 8}, config.BuiltinTools(), []string{WebFetch}},
		{"fetch left out", searxng, []string{"datetime", WebSearch}, []string{WebSearch}},
		{"search left out", searxng, []string{WebFetch}, []string{WebFetch}},
		{"both left out", searxng, []string{"datetime"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: tt.tools}, tt.web, nil, "", nil, nil, nil)
			tools.UseWebCheck(func() (bool, string) { return true, "" })
			var specs, listed []string
			for _, s := range tools.Tools() {
				if s.Name == WebSearch || s.Name == WebFetch {
					specs = append(specs, s.Name)
				}
			}
			for _, ti := range tools.Status()[0].Tools {
				if ti.Name == WebSearch || ti.Name == WebFetch {
					listed = append(listed, ti.Name)
					if ti.Confirm {
						t.Errorf("%s asks by default", ti.Name)
					}
				}
			}
			if strings.Join(specs, ",") != strings.Join(tt.want, ",") || strings.Join(listed, ",") != strings.Join(tt.want, ",") {
				t.Errorf("specs %v, listed %v; want %v", specs, listed, tt.want)
			}
			if !slices.Contains(tt.want, WebFetch) {
				// A tool that is off isn't a built-in at all, and its
				// guard has nothing to say.
				if _, err := tools.Call(context.Background(), WebFetch, []byte(`{"url":"https://go.dev"}`)); err == nil {
					t.Error("web_fetch ran while [builtin] tools leaves it out")
				}
				if _, ok := tools.ConfirmCall(dispatch.Call{Name: WebFetch, Args: []byte(`{"url":"https://go.dev"}`)}); ok {
					t.Error("ConfirmCall decided for a web_fetch that is off")
				}
			}
		})
	}

	tools := webTools(t, config.Web{SearXNGURL: "http://127.0.0.1:8888"}, WebFetch)
	if tools.Confirm(WebSearch) != dispatch.ConfirmNever || tools.Confirm(WebFetch) != dispatch.ConfirmAsk {
		t.Errorf("Confirm = %v, %v; want web_search never, web_fetch ask", tools.Confirm(WebSearch), tools.Confirm(WebFetch))
	}
}

// TestWebSearchFollowsCheck checks that web_search is offered, listed and
// run only while the check UseWebCheck gave passes, as merud's SearXNG
// connector says, and that web_fetch doesn't depend on it.
func TestWebSearchFollowsCheck(t *testing.T) {
	tools := webTools(t, config.Web{SearXNGURL: "http://127.0.0.1:8888"})
	ok, why := false, "Web search can't start: Docker isn't running."
	tools.UseWebCheck(func() (bool, string) { return ok, why })
	offered := func() (spec, listed bool) {
		for _, s := range tools.Tools() {
			spec = spec || s.Name == WebSearch
		}
		for _, ti := range tools.Status()[0].Tools {
			listed = listed || ti.Name == WebSearch
		}
		return spec, listed
	}
	if spec, listed := offered(); spec || listed {
		t.Errorf("web_search offered %v, listed %v while the check fails", spec, listed)
	}
	if !slices.ContainsFunc(tools.Tools(), func(s engine.ToolSpec) bool { return s.Name == WebFetch }) {
		t.Error("web_fetch went with web_search")
	}
	text, isErr := call(t, tools, WebSearch, `{"query":"go"}`)
	if !isErr || !strings.Contains(text, why) {
		t.Errorf("web_search while off = %q, %v; want an error with the sentence", text, isErr)
	}
	ok = true
	if spec, listed := offered(); !spec || !listed {
		t.Errorf("web_search offered %v, listed %v once the check passes", spec, listed)
	}
}
