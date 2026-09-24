// This file enforces the privacy non-negotiables from AGENTS.md on the real
// module: no cloud-model SDK, no telemetry or self-update library, no
// cloud-model host and no non-loopback URL in shipped string literals. The
// self-tests at the bottom run the same checks on made-up sources, to prove
// each check fails when it should.

package policy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixHint closes every failure message with where to look next.
const fixHint = "see AGENTS.md \"Non-negotiables\" and ARCHITECTURE.md \"Privacy boundary\""

// TestNoDeniedImports fails when any Go file, tests included, imports a
// package from a deny-list. Tests count here: no Meru test needs a cloud SDK.
func TestNoDeniedImports(t *testing.T) {
	root := moduleRoot(t)
	files := moduleFiles(t, root)

	// Each row is one deny-list. t.Run gives each its own name in the output,
	// so a failure says which list caught it.
	tests := []struct {
		name string
		list string
		what string
	}{
		{"model provider SDKs", "testdata/model_provider_modules.txt", "cloud-model SDK (non-negotiable 1)"},
		{"telemetry and self-update", "testdata/telemetry_modules.txt", "telemetry, crash-report or self-update library (non-negotiable 2)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, finding := range deniedImports(files, readList(t, tt.list), tt.what) {
				t.Errorf("%s; %s", finding, fixHint)
			}
		})
	}
}

// TestGoModRequiresNoDeniedModule fails when go.mod requires a denied module,
// even one no file imports yet. A module in go.mod is one `import` away from
// the binary.
func TestGoModRequiresNoDeniedModule(t *testing.T) {
	root := moduleRoot(t)
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	requires := goModRequires(string(gomod))
	if len(requires) == 0 {
		t.Fatal("found no require lines in go.mod; the parser in scan_test.go needs a fix")
	}
	for _, list := range []string{"testdata/model_provider_modules.txt", "testdata/telemetry_modules.txt"} {
		denied := readList(t, list)
		for _, mod := range requires {
			if prefix, ok := matchModule(mod, denied); ok {
				t.Errorf("go.mod: requires %q (deny-list %s, entry %q); %s", mod, list, prefix, fixHint)
			}
		}
	}
}

// TestNoProviderHostLiterals fails when a shipped Go file holds a string
// literal naming a cloud-model API host.
func TestNoProviderHostLiterals(t *testing.T) {
	root := moduleRoot(t)
	hosts := readList(t, "testdata/model_provider_hosts.txt")
	for _, finding := range providerHostLiterals(moduleFiles(t, root), hosts) {
		t.Errorf("%s; %s", finding, fixHint)
	}
}

// TestNoNonLoopbackURLLiterals fails when a shipped Go file holds a string
// literal with an http(s) or ws(s) URL to a host that isn't loopback, unless
// allowed_urls.txt lists it. This catches telemetry endpoints, update checks
// and hard-coded remote services.
func TestNoNonLoopbackURLLiterals(t *testing.T) {
	root := moduleRoot(t)
	allowed := readList(t, "allowed_urls.txt")
	for _, finding := range nonLoopbackURLs(moduleFiles(t, root), allowed) {
		t.Errorf("%s; if this is a documentation link Meru prints but never fetches, add it to internal/policy/allowed_urls.txt with a reason; %s", finding, fixHint)
	}
}

// TestChecksCatchViolations feeds each check a small made-up source and
// counts the findings. Without it, a bug that made a check see nothing would
// pass silently.
//
// No provider host appears as a literal here: the sources are built from the
// deny-list entries at run time, so this file passes its own checks.
func TestChecksCatchViolations(t *testing.T) {
	providerModules := readList(t, "testdata/model_provider_modules.txt")
	telemetryModules := readList(t, "testdata/telemetry_modules.txt")
	hosts := readList(t, "testdata/model_provider_hosts.txt")
	// The scheme and host are split so that no single literal in this file
	// is a remote URL.
	remote := "https" + "://collector.acme" + ".io/v1/traces"
	// quote turns a string into a Go string literal, escapes and all.
	quote := strconv.Quote

	tests := []struct {
		name  string
		file  string // the file name, which decides whether it counts as a test
		src   string
		check func([]sourceFile) []string
		want  int // how many findings the check should report
	}{
		{
			name:  "provider SDK import",
			file:  "internal/engine/cloud.go",
			src:   "package engine\nimport _ " + quote(providerModules[0]+"/openai-go/v2") + "\n",
			check: func(fs []sourceFile) []string { return deniedImports(fs, providerModules, "sdk") },
			want:  1,
		},
		{
			name:  "provider SDK import in a test still counts",
			file:  "internal/engine/cloud_test.go",
			src:   "package engine\nimport _ " + quote(providerModules[1]) + "\n",
			check: func(fs []sourceFile) []string { return deniedImports(fs, providerModules, "sdk") },
			want:  1,
		},
		{
			name:  "similar module name is not a match",
			file:  "internal/engine/fine.go",
			src:   "package engine\nimport _ " + quote(providerModules[0]+"x/tool") + "\n",
			check: func(fs []sourceFile) []string { return deniedImports(fs, providerModules, "sdk") },
			want:  0,
		},
		{
			name:  "telemetry import",
			file:  "internal/obs/crash.go",
			src:   "package obs\nimport _ " + quote(telemetryModules[0]+"/sentry-go") + "\n",
			check: func(fs []sourceFile) []string { return deniedImports(fs, telemetryModules, "telemetry") },
			want:  1,
		},
		{
			name:  "provider host in a literal",
			file:  "internal/engine/cloud.go",
			src:   "package engine\nconst base = " + quote("https://"+strings.ToUpper(hosts[0])+"/v1") + "\n",
			check: func(fs []sourceFile) []string { return providerHostLiterals(fs, hosts) },
			want:  1,
		},
		{
			name:  "provider host in a comment passes",
			file:  "internal/engine/doc.go",
			src:   "// Meru never calls " + hosts[0] + ".\npackage engine\n",
			check: func(fs []sourceFile) []string { return providerHostLiterals(fs, hosts) },
			want:  0,
		},
		{
			name:  "provider host in a test file passes",
			file:  "internal/config/config_test.go",
			src:   "package config\nconst bad = " + quote("https://"+hosts[0]) + "\n",
			check: func(fs []sourceFile) []string { return providerHostLiterals(fs, hosts) },
			want:  0,
		},
		{
			name:  "remote URL in a literal",
			file:  "internal/obs/export.go",
			src:   "package obs\nvar endpoint = " + quote("send to "+remote+" now") + "\n",
			check: func(fs []sourceFile) []string { return nonLoopbackURLs(fs, nil) },
			want:  1,
		},
		{
			name:  "remote URL in a raw string",
			file:  "internal/obs/export.go",
			src:   "package obs\nvar endpoint = `" + remote + "`\n",
			check: func(fs []sourceFile) []string { return nonLoopbackURLs(fs, nil) },
			want:  1,
		},
		{
			name:  "allow-listed URL passes",
			file:  "internal/obs/export.go",
			src:   "package obs\nvar endpoint = " + quote(remote) + "\n",
			check: func(fs []sourceFile) []string { return nonLoopbackURLs(fs, []string{"https" + "://collector.acme"}) },
			want:  0,
		},
		{
			name: "loopback, reserved and templated hosts pass",
			file: "internal/config/defaults.go",
			src: "package config\nvar a = []string{" +
				quote("http://127.0.0.1:11434") + ", " +
				quote("http://localhost:4318/v1/metrics") + ", " +
				quote("http://[::1]:11434/api/chat") + ", " +
				quote("ws://user@127.0.0.2:9000") + ", " +
				quote("https://collector.example.com") + ", " +
				quote("http://merud.test") + ", " +
				quote("http://%s/api/chat") + ", " +
				quote("http://") + "}\n",
			check: func(fs []sourceFile) []string { return nonLoopbackURLs(fs, nil) },
			want:  0,
		},
		{
			name:  "bind-all address is flagged",
			file:  "cmd/merud/main.go",
			src:   "package main\nconst addr = " + quote("http://0.0.0.0:11434") + "\n",
			check: func(fs []sourceFile) []string { return nonLoopbackURLs(fs, nil) },
			want:  1,
		},
		{
			name:  "remote URL in a test file passes",
			file:  "internal/config/config_test.go",
			src:   "package config\nvar bad = " + quote(remote) + "\n",
			check: func(fs []sourceFile) []string { return nonLoopbackURLs(fs, nil) },
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseSource(tt.file, []byte(tt.src))
			if err != nil {
				t.Fatalf("parse made-up source: %v\n%s", err, tt.src)
			}
			got := tt.check([]sourceFile{f})
			if len(got) != tt.want {
				t.Errorf("got %d findings, want %d:\n%s\nsource:\n%s", len(got), tt.want, strings.Join(got, "\n"), tt.src)
			}
			// Every finding must point at a file and line, so a reader can
			// jump straight to it.
			for _, g := range got {
				if !strings.HasPrefix(g, tt.file+":") {
					t.Errorf("finding %q doesn't start with %q", g, tt.file+":")
				}
			}
		})
	}
}

// TestGoModRequires checks the hand-written go.mod reader on both forms of
// the require directive.
func TestGoModRequires(t *testing.T) {
	gomod := "module example.com/m\n\ngo 1.26.0\n\nrequire example.com/one v1.0.0\n\n" +
		"require (\n\texample.com/two v0.1.0 // indirect\n\n\texample.com/three v1.2.3\n)\n"
	got := goModRequires(gomod)
	want := []string{"example.com/one", "example.com/two", "example.com/three"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("goModRequires = %v, want %v", got, want)
	}
}
