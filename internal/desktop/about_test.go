// This file tests the About section's data: the paths, the links, the
// version read from the build, and that the window, the page and the
// Bridge all show the same one-line tagline.

package desktop

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/opener"
)

func TestAbout(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "dana")
	meru := filepath.Join(home, ".meru")
	for _, tt := range []struct {
		name string
		opts Options
	}{
		{"from Dir", Options{Socket: filepath.Join(home, "elsewhere", "merud.sock"), Dir: meru, Home: home}},
		{"from the socket", Options{Socket: filepath.Join(meru, "merud.sock"), Home: home}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := New(tt.opts).About()
			if a.Tagline != Tagline || a.Version == "" {
				t.Errorf("about = %+v", a)
			}
			wantConfig := filepath.Join("~", ".meru", "config.toml")
			if a.ConfigPath != wantConfig || a.DataDir != filepath.Join("~", ".meru") {
				t.Errorf("paths = %q, %q; want %q and ~/.meru", a.ConfigPath, a.DataDir, wantConfig)
			}
		})
	}

	// Each link opens through OpenURL, so each must pass its check, and
	// each goes to the project on GitHub.
	a := New(Options{Socket: "merud.sock"}).About()
	if len(a.Links) != 4 || a.License != License {
		t.Fatalf("links = %+v", a.Links)
	}
	for _, l := range a.Links {
		if err := opener.Check(l.URL); err != nil || !strings.HasPrefix(l.URL, "https://github.com/aarora79/meru") || l.Label == "" || l.ID == "" {
			t.Errorf("link %+v: %v", l, err)
		}
	}
}

func TestBuildVersion(t *testing.T) {
	for _, tt := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"no build info", nil, false, "unknown"},
		{"release", &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}, true, "v0.3.0"},
		{"local build", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "9b7be92abcdef"}, {Key: "vcs.modified", Value: "false"}}}, true, "(devel) 9b7be92"},
		{"local build with changes", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "9b7be92abcdef"}, {Key: "vcs.modified", Value: "true"}}}, true, "(devel) 9b7be92, modified"},
		{"no version, no revision", &debug.BuildInfo{}, true, "(devel)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildVersion(tt.info, tt.ok); got != tt.want {
				t.Errorf("buildVersion = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTilde(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "dana")
	for _, tt := range []struct{ path, home, want string }{
		{filepath.Join(home, ".meru"), home, filepath.Join("~", ".meru")},
		{home, home, "~"},
		{filepath.Join(home+"x", ".meru"), home, filepath.Join(home+"x", ".meru")},
		{filepath.Join(home, ".meru"), "", filepath.Join(home, ".meru")},
	} {
		if got := tilde(tt.path, tt.home); got != tt.want {
			t.Errorf("tilde(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
		}
	}
}

// TestTaglineEverywhere checks that the rail's logo in index.html carries
// the tagline as its tooltip and description, and that the window's title
// comes from WindowTitle, so the three places can't drift apart.
func TestTaglineEverywhere(t *testing.T) {
	page := ownFiles(t)["web/index.html"]
	for _, want := range []string{`title="` + Tagline + `"`, `>` + Tagline + `<`} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	main, err := os.ReadFile(filepath.Join("..", "..", "cmd", "meru-desktop", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Title:     desktop.WindowTitle", "Description: desktop.Tagline"} {
		if !strings.Contains(string(main), want) {
			t.Errorf("cmd/meru-desktop/main.go lacks %q", want)
		}
	}
	if !strings.HasPrefix(WindowTitle, "Meru ") {
		t.Errorf("WindowTitle = %q; the name must come first", WindowTitle)
	}
}

// TestAppVersion checks that a version `make release` stamps in wins over
// the build information. It sets the variable the linker sets, and puts it
// back when the test ends; no test in this package runs in parallel.
func TestAppVersion(t *testing.T) {
	// t.Cleanup runs the function after the test, pass or fail.
	t.Cleanup(func() { releaseVersion = "" })
	releaseVersion = "v0.4.1"
	if got := appVersion(); got != "v0.4.1" {
		t.Errorf("appVersion = %q, want v0.4.1", got)
	}
	releaseVersion = ""
	if got := appVersion(); got == "v0.4.1" || got == "" {
		t.Errorf("appVersion without a stamp = %q, want the build's own version", got)
	}
}
