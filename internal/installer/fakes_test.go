// This file holds the fakes the installer's tests share: a Runner that
// records each call and runs nothing, an HTTP client that sends every
// request to a test server, and a temporary home folder with a config.

package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRunner stands in for ExecRunner. It records every call, and answer
// decides what each returns; nil answers every call with "" and no error.
type fakeRunner struct {
	mu     sync.Mutex
	calls  [][]string
	answer func(program string, args []string, line func(string)) (string, error)
}

// run is the Runner the tests pass to the code under test.
func (f *fakeRunner) run(_ context.Context, program string, args []string, line func(string)) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{program}, args...))
	f.mu.Unlock()
	if _, ok := Programs()[program]; !ok {
		return "", ErrNotAllowed
	}
	if f.answer == nil {
		return "", nil
	}
	return f.answer(program, args, line)
}

// called returns every recorded call as one line each, "program arg arg".
func (f *fakeRunner) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// clientTo returns an HTTP client that sends every request to srv, whatever
// host the request names. The code under test can then use its fixed
// loopback addresses, such as SearXNGURL, and still reach only the test
// server.
func clientTo(srv *httptest.Server) *http.Client {
	target, _ := url.Parse(srv.URL)
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

// roundTrip turns a function into an http.RoundTripper.
type roundTrip func(*http.Request) (*http.Response, error)

// RoundTrip calls f.
func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// deadClient returns a client whose every request fails, as when nothing
// listens.
func deadClient() *http.Client {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := clientTo(srv)
	srv.Close()
	return c
}

// tempHome returns a temporary home folder with ~/.meru/config.toml made
// from the template.
func tempHome(t *testing.T) Paths {
	t.Helper()
	p := Paths{Home: t.TempDir()}
	if _, err := EnsureConfig(p.Config()); err != nil {
		t.Fatalf("EnsureConfig: %v", err)
	}
	return p
}

// readFile returns the file at path as a string, or stops the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// writeFile writes text to path, making its folder, or stops the test.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// collect returns a say function and the lines it gathered.
func collect() (func(string), *[]string) {
	var mu sync.Mutex
	var lines []string
	return func(s string) {
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	}, &lines
}
