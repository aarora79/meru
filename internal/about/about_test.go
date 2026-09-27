// This file tests the version read from the build, the version cut to fit
// beside the name, the stamp `make release` writes in, and the links.

package about

import (
	"runtime/debug"
	"strings"
	"testing"
)

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

// TestVersion checks that a version `make release` stamps in wins over
// the build information. It sets the variable the linker sets, and puts it
// back when the test ends; no test in this package runs in parallel.
func TestVersion(t *testing.T) {
	// t.Cleanup runs the function after the test, pass or fail.
	t.Cleanup(func() { releaseVersion = "" })
	releaseVersion = "v0.4.1"
	if got := Version(); got != "v0.4.1" {
		t.Errorf("Version = %q, want v0.4.1", got)
	}
	if got := ShortVersion(); got != "v0.4.1" {
		t.Errorf("ShortVersion = %q, want v0.4.1", got)
	}
	releaseVersion = ""
	if got := Version(); got == "v0.4.1" || got == "" {
		t.Errorf("Version without a stamp = %q, want the build's own version", got)
	}
}

func TestShort(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"v0.4.1", "v0.4.1"},
		{"v0.4.2-0.20260927021103-5325b3ef94cb", "dev 5325b3e"},
		{"v0.5.0-20260927021103-5325b3ef94cb", "dev 5325b3e"},
		{"(devel) 5325b3e", "dev 5325b3e"},
		{"(devel) 5325b3e, modified", "dev 5325b3e"},
		{"(devel)", ""},
		{"unknown", ""},
	} {
		if got := short(tt.in); got != tt.want {
			t.Errorf("short(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestLinks checks that every link has a label and an ID and goes to the
// project on GitHub, and that a caller can't change the list for another.
func TestLinks(t *testing.T) {
	links := Links()
	if len(links) != 4 {
		t.Fatalf("links = %+v, want four", links)
	}
	for _, l := range links {
		if l.ID == "" || l.Label == "" || !strings.HasPrefix(l.URL, SourceURL) {
			t.Errorf("link %+v", l)
		}
	}
	links[0].URL = "changed"
	if Links()[0].URL != SourceURL {
		t.Error("changing one caller's list changed the next")
	}
}
