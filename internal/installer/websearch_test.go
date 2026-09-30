// This file tests the web search step: the check against a fake
// SearXNG, and the whole step, which runs no program and hands the
// connector to merud.

package installer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// fakeSearXNG answers like SearXNG: JSON when json is true, the HTML page
// SearXNG sends when JSON is off otherwise. It counts the searches.
func fakeSearXNG(json bool, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		if !json {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "<!DOCTYPE html><html>403 Forbidden</html>")
			return
		}
		if r.URL.Query().Get("q") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error": "No query"}`)
			return
		}
		io.WriteString(w, `{"query": "test", "results": [{"title": "Test", "url": "https://example.com/"}]}`)
	}))
}

// TestVerifySearXNG checks the test search against each kind of answer.
func TestVerifySearXNG(t *testing.T) {
	ok := fakeSearXNG(true, nil)
	defer ok.Close()
	html := fakeSearXNG(false, nil)
	defer html.Close()
	noResults := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"query": "test"}`)
	}))
	defer noResults.Close()

	tests := []struct {
		name   string
		client *http.Client
		want   error // nil, or an error errors.Is must match; wantAny for a plain error
		fails  bool
	}{
		{"answers JSON", clientTo(ok), nil, false},
		{"JSON is off", clientTo(html), catalog.ErrSearXNGNoJSON, true},
		{"nothing listens", deadClient(), catalog.ErrSearXNGDown, true},
		{"JSON with no results", clientTo(noResults), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySearXNG(context.Background(), tt.client, SearXNGURL)
			if (err != nil) != tt.fails {
				t.Fatalf("err = %v, want failure %v", err, tt.fails)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestHasDocker checks that a docker command in one of OrbStack's or
// Docker's folders under the home folder counts.
func TestHasDocker(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".orbstack", "bin", "docker"), "")
	if !HasDocker(home) {
		t.Error("HasDocker = false with ~/.orbstack/bin/docker in place")
	}
}

// TestSetUpWebSearch runs the step on a Mac where nothing answers at
// Meru's SearXNG address: it runs no program, writes [web] and [builtin],
// and hands the connector to the Start Meru step, turned on.
func TestSetUpWebSearch(t *testing.T) {
	p := tempHome(t)
	// Take web_fetch out of [builtin] tools, to check the step puts it back.
	tools := slices.DeleteFunc(config.BuiltinTools(), func(s string) bool { return s == "web_fetch" })
	if err := catalog.SetTableLists(p.Config(), "builtin", map[string][]string{"tools": tools}, nil); err != nil {
		t.Fatal(err)
	}
	msg, hand, err := SetUpWebSearch(context.Background(), deadClient(), p, true)
	if err != nil {
		t.Fatal(err)
	}
	if hand == nil || hand.ID != "searxng" || hand.Adopt || hand.Change == nil || hand.Change.Enabled == nil || !*hand.Change.Enabled {
		t.Errorf("hand-off = %+v, want web search turned on", hand)
	}
	if !strings.Contains(msg, "merud downloads SearXNG") {
		t.Errorf("result = %q", msg)
	}
	cfg, err := config.Load(p.Config())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.SearXNGURL != SearXNGURL || !slices.Contains(cfg.Builtin.Tools, "web_search") || !slices.Contains(cfg.Builtin.Tools, "web_fetch") {
		t.Errorf("config: searxng_url %q, tools %v", cfg.Web.SearXNGURL, cfg.Builtin.Tools)
	}
	if _, ok := cfg.Connectors["searxng"]; ok {
		t.Error("the step wrote [connectors.searxng] itself; merud writes it on the hand-off")
	}
	if !strings.Contains(readFile(t, p.Config()), "# Where SearXNG, the search engine behind web_search, answers on this") {
		t.Error("the config lost its comments")
	}
}

// TestSetUpWebSearchNoDocker checks that the step stops before it changes
// anything on a Mac with no Docker.
func TestSetUpWebSearchNoDocker(t *testing.T) {
	p := tempHome(t)
	before := readFile(t, p.Config())
	_, hand, err := SetUpWebSearch(context.Background(), deadClient(), p, false)
	if !errors.Is(err, ErrNoDocker) || hand != nil {
		t.Errorf("err = %v, hand-off %+v; want ErrNoDocker and none", err, hand)
	}
	if readFile(t, p.Config()) != before {
		t.Error("the step changed config.toml")
	}
}

// TestSetUpWebSearchAlreadyRunning checks that a SearXNG the user already
// runs, or one an older installer started, is left alone: no hand-off, so
// merud only watches it, and only the config write.
func TestSetUpWebSearchAlreadyRunning(t *testing.T) {
	srv := fakeSearXNG(true, nil)
	defer srv.Close()
	p := tempHome(t)
	msg, hand, err := SetUpWebSearch(context.Background(), clientTo(srv), p, false)
	if err != nil {
		t.Fatal(err)
	}
	if hand != nil {
		t.Errorf("hand-off = %+v, want none for a SearXNG that already answers", hand)
	}
	if !strings.Contains(msg, "already answers") {
		t.Errorf("result = %q", msg)
	}
	var parsed map[string]any
	if _, err := toml.DecodeFile(p.Config(), &parsed); err != nil {
		t.Fatal(err)
	}
}
