// This file tests the trusted websites: the trusted_sites and
// trusted_site_remove ops against a whole merud, and saveTrustedSite,
// which "Always this website" at a web_fetch prompt calls.

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

func TestTrustedSiteOps(t *testing.T) {
	dir, _ := meruHome(t)
	d := startDaemon(t, dir, settingsHeader+"\n[web]\ntrusted_sites = [\"go.dev\", \"en.wikipedia.org\"]\n", &fakeEngine{version: "0.40.5"})

	ev := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpTrustedSites}), rpc.EventTrustedSites)
	if !slices.Equal(ev.Sites, []string{"go.dev", "en.wikipedia.org"}) {
		t.Fatalf("sites = %v", ev.Sites)
	}
	ev = eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpTrustedSiteRemove, ID: "go.dev"}), rpc.EventTrustedSites)
	if !slices.Equal(ev.Sites, []string{"en.wikipedia.org"}) {
		t.Errorf("sites after the removal = %v", ev.Sites)
	}
	if got := loadConfig(t, dir).Web.TrustedSites; !slices.Equal(got, []string{"en.wikipedia.org"}) {
		t.Errorf("[web] trusted_sites = %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# my own note, keep me") {
		t.Errorf("the removal lost a comment:\n%s", raw)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpTrustedSiteRemove, ID: "go.dev"}, rpc.ChoiceOnce); !strings.Contains(msg, "doesn't list") {
		t.Errorf("second removal: %q", msg)
	}
}

func TestSaveTrustedSite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(settingsHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	bt := builtin.New(path, cfg.Builtin, cfg.Web, nil, t.TempDir(), nil, nil, nil)
	s := &toolService{configPath: path, bt: bt, log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()
	for _, host := range []string{"go.dev", "go.dev", "en.wikipedia.org"} {
		if err := s.saveTrustedSite(ctx, host); err != nil {
			t.Fatalf("save %s: %v", host, err)
		}
	}
	if got := loadConfig(t, dir).Web.TrustedSites; !slices.Equal(got, []string{"go.dev", "en.wikipedia.org"}) {
		t.Errorf("[web] trusted_sites = %v, want each host once", got)
	}
	for _, bad := range []string{"*.go.dev", "127.0.0.1", "localhost", "printer.local", "Go.Dev"} {
		if err := s.saveTrustedSite(ctx, bad); err == nil {
			t.Errorf("saved %q", bad)
		}
	}
}
