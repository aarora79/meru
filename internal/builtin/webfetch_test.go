// This file tests what web_fetch adds to reading a page: the prompt that
// the fast model answers, the URL guard that decides when a fetch asks
// first, and the download mode. Pages come from httptest servers that the
// dial check treats as public, as in web_test.go, and the model is a fake.
// No test leaves the machine.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// The compiler checks that *Tools implements dispatch.CallConfirmer.
var _ dispatch.CallConfirmer = (*Tools)(nil)

// fakeModel is a Generator that records each call and answers with a
// fixed text, or fails with err.
type fakeModel struct {
	mu     sync.Mutex // guards the fields below
	answer string
	err    error
	msgs   [][]engine.Message
	opts   []engine.Options
}

// Generate records msgs and opts and returns m.answer.
func (m *fakeModel) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msgs)
	m.opts = append(m.opts, opts)
	if m.err != nil {
		return engine.Completion{}, m.err
	}
	return engine.Completion{Text: m.answer, DoneReason: "stop", Usage: engine.Usage{PromptTokens: 900, OutputTokens: 12}}, nil
}

// releasePage is the text of a page that states a version.
const releasePage = "Release History. go1.27.1 (released 2026-09-02) includes security fixes."

// promptMux serves /release, a short HTML page, and /long.txt, a text
// longer than promptChars.
func promptMux(long string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><head><title>Release History</title></head><body><p>"+releasePage+"</p></body></html>")
	})
	mux.HandleFunc("/long.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, long)
	})
	return mux
}

func TestWebFetchPrompt(t *testing.T) {
	// The long page is promptChars "a" characters, then a tail the first
	// answer mustn't see.
	long := strings.Repeat("a", promptChars) + "TAIL-" + strings.Repeat("b", 995)
	srv, tools := pageServer(t, promptMux(long))
	model := &fakeModel{answer: "  The latest release is go1.27.1, released 2026-09-02.  "}
	tools.UseModel(model, "fast-model")

	text, isErr := call(t, tools, WebFetch, `{"url":"`+srv.URL+`/release","prompt":"What is the latest Go release?"}`)
	if isErr {
		t.Fatalf("prompt call failed: %s", text)
	}
	want := "From " + srv.URL + "/release (fetched " + time.Now().Format("2006-01-02") +
		"): The latest release is go1.27.1, released 2026-09-02."
	if text != want {
		t.Errorf("result = %q, want %q", text, want)
	}
	opts := model.opts[0]
	if opts.Model != "fast-model" || !opts.NoThink || opts.Temperature == nil || *opts.Temperature != 0 || opts.MaxTokens != answerTokens {
		t.Errorf("options = %+v; want the fast model, NoThink, temperature 0, MaxTokens %d", opts, answerTokens)
	}
	msgs := model.msgs[0]
	if len(msgs) != 2 || msgs[0].Role != engine.RoleSystem || !strings.Contains(msgs[0].Content, "only the web page text") {
		t.Fatalf("messages = %+v; want the instructions, then the page and the question", msgs)
	}
	if user := msgs[1].Content; !strings.Contains(user, releasePage) || !strings.HasSuffix(user, "Question: What is the latest Go release?") {
		t.Errorf("user message lacks the page or ends without the question:\n%s", user)
	}

	// A page longer than the cap: the model sees the first promptChars
	// characters, and the result gives the offset for the rest.
	text, isErr = call(t, tools, WebFetch, `{"url":"`+srv.URL+`/long.txt","prompt":"Summarize."}`)
	if isErr {
		t.Fatalf("long prompt call failed: %s", text)
	}
	user := model.msgs[1][1].Content
	if strings.Contains(user, "TAIL-") || strings.Count(user, "a") < promptChars {
		t.Errorf("the model saw the tail, or less than the first %d characters", promptChars)
	}
	for _, w := range []string{
		fmt.Sprintf("covers characters 0 to %d of the page's %d", promptChars, promptChars+1000),
		fmt.Sprintf("call web_fetch with the same prompt and offset %d", promptChars),
	} {
		if !strings.Contains(text, w) {
			t.Errorf("result lacks %q:\n%s", w, text)
		}
	}

	// The offset reads on: the model sees the tail, and nothing is left.
	text, _ = call(t, tools, WebFetch, fmt.Sprintf(`{"url":"%s/long.txt","prompt":"Summarize.","offset":%d}`, srv.URL, promptChars))
	if user := model.msgs[2][1].Content; !strings.Contains(user, "TAIL-") || strings.Contains(user, "aaaa") {
		t.Errorf("with the offset, the model saw the wrong part:\n%.200s", user)
	}
	if strings.Contains(text, "For the rest") {
		t.Errorf("result offers more after the last part:\n%s", text)
	}
}

func TestWebFetchPromptFails(t *testing.T) {
	srv, tools := pageServer(t, promptMux("x"))
	text, isErr := call(t, tools, WebFetch, `{"url":"`+srv.URL+`/release","prompt":"Version?"}`)
	if !isErr || !strings.Contains(text, "call again without prompt") {
		t.Errorf("with no model: IsError %v, text %q; want the leave-out-prompt error", isErr, text)
	}
	tools.UseModel(&fakeModel{err: errors.New("ollama is down")}, "fast")
	text, isErr = call(t, tools, WebFetch, `{"url":"`+srv.URL+`/release","prompt":"Version?"}`)
	if !isErr || !strings.Contains(text, "ollama is down") {
		t.Errorf("with a failing model: IsError %v, text %q", isErr, text)
	}
	// Without a prompt, the model isn't called and the raw text comes back.
	text, isErr = call(t, tools, WebFetch, `{"url":"`+srv.URL+`/release"}`)
	if isErr || !strings.Contains(text, "Title: Release History") || !strings.Contains(text, releasePage) {
		t.Errorf("raw path: IsError %v, text %q", isErr, text)
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"https://go.dev/doc/devel/release", "https://go.dev/doc/devel/release", true},
		{"HTTPS://Go.Dev/doc/devel/release#go1.27", "https://go.dev/doc/devel/release", true},
		{"https://go.dev", "https://go.dev/", true},
		{"https://example.com/a?b=1&c=2", "https://example.com/a?b=1&c=2", true},
		{"https://example.com/Path", "https://example.com/Path", true},
		{"ftp://example.com/x", "", false},
		{"go.dev/doc", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeURL(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestQuestionURLs(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"what does https://go.dev/doc/devel/release say?", []string{"https://go.dev/doc/devel/release"}},
		{"read https://go.dev/blog/go1.27.", []string{"https://go.dev/blog/go1.27"}},
		{"(see https://go.dev/doc)", []string{"https://go.dev/doc"}},
		{"https://en.wikipedia.org/wiki/Go_(programming_language), please", []string{"https://en.wikipedia.org/wiki/Go_(programming_language)"}},
		{"compare http://a.example/x and <https://b.example/y>", []string{"http://a.example/x", "https://b.example/y"}},
		{"no links here, just go.dev", nil},
	}
	for _, tt := range tests {
		if got := questionURLs(tt.text); !slices.Equal(got, tt.want) {
			t.Errorf("questionURLs(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

// TestKnownURLsBound checks the cap on sessions: the session used longest
// ago goes first, and a lookup counts as a use.
func TestKnownURLsBound(t *testing.T) {
	k := newKnownURLs()
	for i := range maxURLSessions {
		k.add(fmt.Sprintf("s%d", i), "https://go.dev/")
	}
	// Touch s0, so s1 is now the oldest.
	if !k.has("s0", "https://go.dev/") {
		t.Fatal("s0 lost its URL")
	}
	k.add("new", "https://go.dev/")
	if len(k.sessions) != maxURLSessions || !k.has("s0", "https://go.dev/") || k.has("s1", "https://go.dev/") {
		t.Errorf("after one more session: %d sessions, s0 %v, s1 %v; want %d, true, false",
			len(k.sessions), k.has("s0", "https://go.dev/"), k.has("s1", "https://go.dev/"), maxURLSessions)
	}
	// A session takes no more than maxURLsPerSession URLs.
	for i := range maxURLsPerSession + 5 {
		k.add("full", fmt.Sprintf("https://example.com/%d", i))
	}
	if n := len(k.sessions["full"].urls); n != maxURLsPerSession {
		t.Errorf("session holds %d URLs, want %d", n, maxURLsPerSession)
	}
	k.add("", "https://go.dev/")
	if k.has("", "https://go.dev/") {
		t.Error("a call with no session counts as known")
	}
}

// guardSetup starts a page server and a SearXNG whose results link to it,
// and returns a dispatcher over web tools that fetch through both, plus
// the page server's URL.
func guardSetup(t *testing.T, outputDir string) (*dispatch.Dispatcher, *Tools, string) {
	t.Helper()
	mux := promptMux("x")
	mux.HandleFunc("/file.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "a small file")
	})
	srv, tools := pageServer(t, mux)
	results := fmt.Sprintf(`{"results":[{"url":%q,"title":"Release History","content":"Go releases"}]}`, srv.URL+"/release#latest")
	searx := fakeSearXNG(t, 200, results, nil)
	tools.web.searxngURL = searx.URL
	tools.outputDir = outputDir
	return dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{}), tools, srv.URL
}

// asker answers every approval with once and counts the prompts.
type asker struct {
	mu    sync.Mutex // guards asked
	asked []rpc.Approval
}

// approve records ap and approves once.
func (a *asker) approve(ctx context.Context, ap rpc.Approval) (rpc.Choice, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, ap)
	return rpc.ChoiceOnce, nil
}

// count returns how many prompts a saw.
func (a *asker) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.asked)
}

// guardCall builds a web tool call in session from source, with question
// as the user's words.
func guardCall(name, args, session, question string, source rpc.Source, a *asker) dispatch.Call {
	return dispatch.Call{ID: "c1", Name: name, Args: json.RawMessage(args), Session: session,
		Question: question, Source: source, Approve: a.approve}
}

func TestWebFetchGuard(t *testing.T) {
	d, _, base := guardSetup(t, t.TempDir())
	ctx := context.Background()
	fetch := func(u string) string { return fmt.Sprintf(`{"url":%q}`, u) }

	tests := []struct {
		name     string
		search   bool   // run a web_search in the session first
		question string // the user's words for the fetch call
		session  string
		source   rpc.Source
		args     string
		asks     bool
		outcome  string
	}{
		{name: "search-result URL", search: true, session: "a", args: fetch(base + "/release"), outcome: dispatch.OutcomeOK},
		{name: "search-result URL with other case and a fragment", search: true, session: "a",
			args: fetch(strings.ToUpper(base[:4]) + base[4:] + "/release#top"), outcome: dispatch.OutcomeOK},
		{name: "question URL", session: "b", question: "what does " + base + "/release say?",
			args: fetch(base + "/release"), outcome: dispatch.OutcomeOK},
		{name: "earlier question URL", session: "b2", question: "summarize it\n" + base + "/release is the page",
			args: fetch(base + "/release"), outcome: dispatch.OutcomeOK},
		{name: "made-up URL", search: true, session: "c", args: fetch(base + "/long.txt"), asks: true, outcome: dispatch.OutcomeOK},
		{name: "notes in the query string", search: true, session: "d",
			args: fetch(base + "/release?notes=" + url.QueryEscape("my tax is due")), asks: true, outcome: dispatch.OutcomeOK},
		{name: "job source", session: "e", source: rpc.SourceJob, args: fetch(base + "/release"), outcome: dispatch.OutcomeDeclined},
		{name: "URL from another session", session: "f", args: fetch(base + "/release"), asks: true, outcome: dispatch.OutcomeOK},
		{name: "search-result URL in a job", search: true, session: "g", source: rpc.SourceJob,
			args: fetch(base + "/release"), outcome: dispatch.OutcomeOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := tt.source
			if source == "" {
				source = rpc.SourceTUI
			}
			a := &asker{}
			if tt.search {
				if _, out := d.Dispatch(ctx, guardCall(WebSearch, `{"query":"go release"}`, tt.session, "", source, a)); out.Outcome != dispatch.OutcomeOK {
					t.Fatalf("search outcome %q", out.Outcome)
				}
			}
			// Session "a" searched, so session "f" must not see its URL.
			res, out := d.Dispatch(ctx, guardCall(WebFetch, tt.args, tt.session, tt.question, source, a))
			if out.Outcome != tt.outcome {
				t.Errorf("outcome %q, want %q: %s", out.Outcome, tt.outcome, res.Text)
			}
			if asked := a.count() > 0; asked != tt.asks {
				t.Errorf("asked = %v, want %v", asked, tt.asks)
			}
			if tt.asks && a.count() > 0 {
				if got := a.asked[0].Choices; !slices.Equal(got, []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}) {
					t.Errorf("choices = %v, want once and deny, no session", got)
				}
			}
		})
	}
}

// TestWebFetchSaveAsks checks that save asks even for a search-result URL,
// offering all three choices; that save on a made-up URL offers no session
// choice; and that a job's save is declined without a prompt.
func TestWebFetchSaveAsks(t *testing.T) {
	out := t.TempDir()
	d, tools, base := guardSetup(t, out)
	ctx := context.Background()
	a := &asker{}
	if _, o := d.Dispatch(ctx, guardCall(WebSearch, `{"query":"go"}`, "s", "", rpc.SourceTUI, a)); o.Outcome != dispatch.OutcomeOK {
		t.Fatalf("search outcome %q", o.Outcome)
	}

	save := fmt.Sprintf(`{"url":%q,"save":true}`, base+"/release")
	if _, o := d.Dispatch(ctx, guardCall(WebFetch, save, "s", "", rpc.SourceTUI, a)); o.Outcome != dispatch.OutcomeOK || a.count() != 1 {
		t.Fatalf("save of a known URL: outcome %q after %d prompts; want ok after one", o.Outcome, a.count())
	}
	if got := a.asked[0].Choices; !slices.Equal(got, []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}) {
		t.Errorf("known URL save choices = %v, want once, session and deny", got)
	}

	madeUp := fmt.Sprintf(`{"url":%q,"save":true}`, base+"/file.pdf")
	if c, ok := tools.ConfirmCall(guardCall(WebFetch, madeUp, "s", "", rpc.SourceTUI, a)); !ok || c != dispatch.ConfirmAlways {
		t.Errorf("made-up URL save: ConfirmCall = %v, %v; want ConfirmAlways", c, ok)
	}

	job := &asker{}
	if _, o := d.Dispatch(ctx, guardCall(WebFetch, save, "s", "", rpc.SourceJob, job)); o.Outcome != dispatch.OutcomeDeclined || job.count() != 0 {
		t.Errorf("job save: outcome %q after %d prompts; want declined with none", o.Outcome, job.count())
	}
	entries, _ := os.ReadDir(filepath.Join(out, downloadsFolder))
	if len(entries) != 1 {
		t.Errorf("downloads holds %d files, want the one approved save", len(entries))
	}
}

func TestDownloadName(t *testing.T) {
	u := func(s string) *url.URL {
		p, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name, disposition, url, want string
	}{
		{"url path", "", "https://example.com/files/report.pdf", "report.pdf"},
		{"disposition wins", `attachment; filename="Q3 report.pdf"`, "https://example.com/dl?id=7", "Q3_report.pdf"},
		{"disposition with a path", `attachment; filename="../../.ssh/authorized_keys"`, "https://example.com/x", "authorized_keys"},
		{"backslash path", `attachment; filename="..\\..\\evil.exe"`, "https://example.com/x", "evil.exe"},
		{"hidden name", "", "https://example.com/.bashrc", "bashrc"},
		{"no path", "", "https://example.com/", "download"},
		{"only dots", "", "https://example.com/..", "download"},
		{"unicode and spaces", "", "https://example.com/r%C3%A9sum%C3%A9%20final.pdf", "r_sum__final.pdf"},
		{"bad disposition falls back", `attachment; filename=`, "https://example.com/a.txt", "a.txt"},
		{"long name", "", "https://example.com/" + strings.Repeat("x", 300), strings.Repeat("x", maxNameChars)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloadName(tt.disposition, u(tt.url)); got != tt.want {
				t.Errorf("downloadName = %q, want %q", got, tt.want)
			}
		})
	}
}

// downloadTools returns web tools that save in a temp output folder,
// reading from a page server with the given pages.
func downloadTools(t *testing.T, mux *http.ServeMux) (*Tools, string, string) {
	t.Helper()
	srv, tools := pageServer(t, mux)
	out := filepath.Join(t.TempDir(), "meru-output")
	tools.outputDir = out
	return tools, out, srv.URL
}

func TestWebFetchSave(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/notes.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "plain notes "+strings.Repeat("z", 3000))
	})
	mux.HandleFunc("/data.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="data set.bin"`)
		_, _ = w.Write([]byte{0, 1, 2, 3})
	})
	mux.HandleFunc("/huge.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// io.CopyN writes downloadCap+1 zero bytes without holding them
		// in memory.
		_, _ = io.CopyN(w, zeros{}, downloadCap+1)
	})
	tools, out, base := downloadTools(t, mux)
	dl := filepath.Join(out, downloadsFolder)

	text, isErr := call(t, tools, WebFetch, `{"url":"`+base+`/notes.txt","save":true}`)
	if isErr {
		t.Fatalf("save failed: %s", text)
	}
	first := filepath.Join(dl, "notes.txt")
	for _, w := range []string{"Saved " + base + "/notes.txt to " + first, "text/plain", "The first 2000 characters", "plain notes"} {
		if !strings.Contains(text, w) {
			t.Errorf("result lacks %q:\n%.400s", w, text)
		}
	}
	if strings.Count(text, "z") > previewChars {
		t.Errorf("preview holds more than %d characters", previewChars)
	}
	checkMode(t, first, 0o600)
	checkMode(t, dl, 0o700)

	// The same URL again gets a new name; the first file stays.
	text, _ = call(t, tools, WebFetch, `{"url":"`+base+`/notes.txt","save":true}`)
	if !strings.Contains(text, filepath.Join(dl, "notes-2.txt")) {
		t.Errorf("second save didn't pick notes-2.txt:\n%.300s", text)
	}
	text, _ = call(t, tools, WebFetch, `{"url":"`+base+`/notes.txt","save":true}`)
	if !strings.Contains(text, filepath.Join(dl, "notes-3.txt")) {
		t.Errorf("third save didn't pick notes-3.txt:\n%.300s", text)
	}

	// A binary file: saved under the disposition's name, with no preview.
	text, isErr = call(t, tools, WebFetch, `{"url":"`+base+`/data.bin","save":true}`)
	if isErr || !strings.Contains(text, filepath.Join(dl, "data_set.bin")) || !strings.Contains(text, "4 bytes, application/octet-stream.") ||
		strings.Contains(text, "first 2000") {
		t.Errorf("binary save: IsError %v:\n%s", isErr, text)
	}

	// Over the cap: an error, and no file left behind.
	text, isErr = call(t, tools, WebFetch, `{"url":"`+base+`/huge.bin","save":true}`)
	if !isErr || !strings.Contains(text, "larger than 50.0 MB") || !strings.Contains(text, "Nothing was saved") {
		t.Errorf("huge save: IsError %v, text %q", isErr, text)
	}
	if _, err := os.Lstat(filepath.Join(dl, "huge.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("huge.bin left behind: %v", err)
	}

	// save takes url alone.
	if text, isErr := call(t, tools, WebFetch, `{"url":"`+base+`/notes.txt","save":true,"prompt":"x"}`); !isErr || !strings.Contains(text, "save takes url alone") {
		t.Errorf("save with prompt: IsError %v, text %q", isErr, text)
	}
}

// zeros is an io.Reader of endless zero bytes.
type zeros struct{}

// Read fills p with zeros.
func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestWebFetchSaveSymlinks checks that a download never goes through a
// symbolic link: a downloads folder that is a link is refused, and a link
// that holds the file's name is left alone while the file gets the next
// name.
func TestWebFetchSaveSymlinks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/notes.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "downloaded")
	})
	tools, out, base := downloadTools(t, mux)
	elsewhere := t.TempDir()
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(out, downloadsFolder)); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	text, isErr := call(t, tools, WebFetch, `{"url":"`+base+`/notes.txt","save":true}`)
	if !isErr || !strings.Contains(text, "is a symbolic link") {
		t.Errorf("downloads as a link: IsError %v, text %q", isErr, text)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the download went through the link: %d files in its target", len(entries))
	}

	// A real downloads folder that holds a link named notes.txt.
	if err := os.Remove(filepath.Join(out, downloadsFolder)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(out, downloadsFolder), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(elsewhere, "target.txt")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(out, downloadsFolder, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	text, isErr = call(t, tools, WebFetch, `{"url":"`+base+`/notes.txt","save":true}`)
	if isErr || !strings.Contains(text, "notes-2.txt") {
		t.Errorf("save next to a link: IsError %v, text %q", isErr, text)
	}
	if got, _ := os.ReadFile(target); string(got) != "untouched" { // #nosec G304 -- a test temp file
		t.Errorf("the link's target now holds %q", got)
	}
}

// TestReadDownloads checks that read_file and grep reach the downloads
// folder through index.ReadAlso, and that the indexer's rules still apply
// there: a symbolic link in it is off limits.
func TestReadDownloads(t *testing.T) {
	base := t.TempDir()
	notes := filepath.Join(base, "notes")
	dl := filepath.Join(base, "out", downloadsFolder)
	for _, d := range []string{notes, dl} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dl, "release.txt"), []byte("go1.27.1 released 2026-09-02\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.md"), []byte("go1.27.1 secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "secret.md"), filepath.Join(dl, "link.md")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	ix, err := index.New(config.Index{Folders: []string{notes}, MaxFileMB: 1}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ix.ReadAlso(dl)
	tools := New(filepath.Join(base, "config.toml"), config.Builtin{}, config.Web{}, nil, "", ix, nil, nil)
	ctx := context.Background()

	res := callTool(t, tools, ctx, ReadFile, `{"path":`+jsonPath(filepath.Join(dl, "release.txt"))+`}`)
	if res.IsError || !strings.Contains(res.Text, "go1.27.1 released") {
		t.Errorf("read_file on a download: %+v", res)
	}
	res = callTool(t, tools, ctx, Grep, `{"pattern":"go1.27.1","path":`+jsonPath(dl)+`}`)
	if res.IsError || !strings.Contains(res.Text, "release.txt:1:") || strings.Contains(res.Text, "secret") {
		t.Errorf("grep in downloads: %+v", res)
	}
	res = callTool(t, tools, ctx, ReadFile, `{"path":`+jsonPath(filepath.Join(dl, "link.md"))+`}`)
	if !res.IsError || !strings.Contains(res.Text, "symbolic link") {
		t.Errorf("read_file on a link in downloads: %+v", res)
	}
}
