// This file tests the manifest: the four real manifests load, and
// Validate refuses each thing its rules name.

package connectors

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestLoadRealManifests loads the manifests compiled into the package and
// checks the facts later changes depend on: the IDs, the kinds and the
// pins.
func TestLoadRealManifests(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byID := map[string]Manifest{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	tests := []struct {
		id, kind, install string
		required          bool
	}{
		{"google", KindHTTP, InstallPip, false},
		{"obsidian", KindStdio, InstallNPM, false},
		{"ollama", KindDependency, InstallNone, true},
		{"searxng", KindContainer, InstallContainer, false},
	}
	if len(ms) != len(tests) {
		t.Errorf("Load returned %d manifests, want %d", len(ms), len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			m, ok := byID[tt.id]
			if !ok {
				t.Fatalf("no manifest %q", tt.id)
			}
			if m.Kind != tt.kind || m.Install.Type != tt.install || m.Required != tt.required {
				t.Errorf("kind %q, install %q, required %v; want %q, %q, %v",
					m.Kind, m.Install.Type, m.Required, tt.kind, tt.install, tt.required)
			}
		})
	}
	// The tool names are load-bearing: they appear in tool_calls,
	// transcripts and the router, so the IDs must stay google and obsidian.
	if got := byID["obsidian"].Launch.Args; strings.Join(got, " ") != "serve --vault {field.vault_name}={field.vault_path}" {
		t.Errorf("obsidian args = %q", got)
	}
	if got := byID["google"].Launch.Env["WORKSPACE_ATTACHMENT_DIR"]; got != "~/meru-output/attachments" {
		t.Errorf("google WORKSPACE_ATTACHMENT_DIR = %q", got)
	}
}

// valid returns a stdio manifest that passes Validate, for the refusal
// tests to break one piece at a time.
func valid() Manifest {
	return Manifest{
		ID:          "notes",
		Name:        "Notes",
		Kind:        KindStdio,
		IdleTimeout: "10m",
		Install:     Install{Type: InstallNPM, Package: "notes-mcp", Version: "1.2.3"},
		Launch: Launch{
			Command: "notes-mcp",
			Args:    []string{"--dir", "{field.dir}"},
			Env:     map[string]string{"NOTES_KEY": "{field.key}"},
		},
		Fields: []Field{
			{ID: "dir", Type: FieldFolder, Label: "Folder", Required: true},
			{ID: "key", Type: FieldSecret, Label: "Key"},
		},
		Health: Health{Tool: "list", Expect: "nonempty"},
		MCP:    MCP{Allow: []string{"list", "write"}, Confirm: []string{"write"}},
	}
}

// httpManifest returns an http manifest that passes Validate.
func httpManifest() Manifest {
	m := valid()
	m.Kind = KindHTTP
	m.Install = Install{Type: InstallPip, Package: "notes-mcp", Version: "1.30.0"}
	m.Launch.URL = "http://127.0.0.1:8000/mcp"
	m.Launch.Port = 8000
	m.Launch.Auth = AuthNone
	return m
}

// containerManifest returns a container manifest that passes Validate.
func containerManifest() Manifest {
	return Manifest{
		ID:      "search",
		Name:    "Search",
		Kind:    KindContainer,
		Install: Install{Type: InstallContainer, Image: "docker.io/acme/search:2026.1@sha256:" + strings.Repeat("a", 64)},
		Launch:  Launch{URL: "http://127.0.0.1:8888", Port: 8080, Volumes: []string{"{meru_dir}/search:/etc/search"}},
		Health:  Health{Path: "/healthz", Expect: "contains:OK"},
	}
}

// binaryManifest returns a binary-install manifest that passes Validate.
func binaryManifest() Manifest {
	m := valid()
	m.Install = Install{Type: InstallBinary, Version: "v0.4.2", Binaries: map[string]Binary{
		"darwin_arm64": {URL: "https://example.org/notes-darwin-arm64.tar.gz", SHA256: strings.Repeat("b", 64)},
	}}
	m.Launch.Command = "{pkg}/notes-darwin-arm64"
	return m
}

// TestValidate breaks one rule per case and checks that Validate names it.
// An empty want means the manifest must pass.
func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		base func() Manifest
		edit func(m *Manifest)
		want string
	}{
		{"stdio passes", valid, func(m *Manifest) {}, ""},
		{"http passes", httpManifest, func(m *Manifest) {}, ""},
		{"container passes", containerManifest, func(m *Manifest) {}, ""},
		{"binary passes", binaryManifest, func(m *Manifest) {}, ""},
		{"prerelease npm passes", valid, func(m *Manifest) { m.Install.Version = "2.0.0-beta.4" }, ""},

		{"missing id", valid, func(m *Manifest) { m.ID = "" }, "id is missing"},
		{"bad id", valid, func(m *Manifest) { m.ID = "My-Notes" }, "may hold only"},
		{"missing name", valid, func(m *Manifest) { m.Name = " " }, "name is missing"},
		{"bad idle timeout", valid, func(m *Manifest) { m.IdleTimeout = "soon" }, "idle_timeout"},
		{"unknown kind", valid, func(m *Manifest) { m.Kind = "socket" }, `kind "socket" is unknown`},
		{"unknown install type", valid, func(m *Manifest) { m.Install.Type = "brew" }, `install.type "brew" is unknown`},
		{"install type doesn't fit kind", valid, func(m *Manifest) {
			m.Install = Install{Type: InstallContainer, Image: containerManifest().Install.Image}
		}, "doesn't fit kind"},

		{"version latest", valid, func(m *Manifest) { m.Install.Version = "latest" }, "isn't one exact version"},
		{"version empty", valid, func(m *Manifest) { m.Install.Version = "" }, "isn't one exact version"},
		{"version caret", valid, func(m *Manifest) { m.Install.Version = "^2.0.1" }, "isn't one exact version"},
		{"version tilde", valid, func(m *Manifest) { m.Install.Version = "~2.0.1" }, "isn't one exact version"},
		{"version greater", valid, func(m *Manifest) { m.Install.Version = ">=2.0.1" }, "isn't one exact version"},
		{"version less", valid, func(m *Manifest) { m.Install.Version = "<3" }, "isn't one exact version"},
		{"version star", valid, func(m *Manifest) { m.Install.Version = "*" }, "isn't one exact version"},
		{"version x", valid, func(m *Manifest) { m.Install.Version = "2.0.x" }, "isn't one exact version"},
		{"version bare major", valid, func(m *Manifest) { m.Install.Version = "2" }, "isn't one exact version"},
		{"version tag", valid, func(m *Manifest) { m.Install.Version = "next" }, "isn't one exact version"},
		{"pip version latest", httpManifest, func(m *Manifest) { m.Install.Version = "latest" }, "isn't one exact version"},
		{"pip version range", httpManifest, func(m *Manifest) { m.Install.Version = ">=1.30" }, "isn't one exact version"},
		{"pip version wildcard", httpManifest, func(m *Manifest) { m.Install.Version = "1.30.*" }, "isn't one exact version"},
		{"binary version latest", binaryManifest, func(m *Manifest) { m.Install.Version = "latest" }, "isn't one exact version"},
		{"version in package name", valid, func(m *Manifest) { m.Install.Package = "notes-mcp@latest" }, "isn't an npm package name"},
		{"pip version in package name", httpManifest, func(m *Manifest) { m.Install.Package = "notes-mcp==1.0" }, "isn't a Python package name"},
		{"stray image on npm", valid, func(m *Manifest) { m.Install.Image = "x@sha256:" + strings.Repeat("a", 64) }, "install.image is for type container"},

		{"image without digest", containerManifest, func(m *Manifest) { m.Install.Image = "docker.io/acme/search:2026.1" }, "pinned by digest"},
		{"image latest", containerManifest, func(m *Manifest) { m.Install.Image = "docker.io/acme/search:latest" }, "pinned by digest"},
		{"image latest with digest", containerManifest, func(m *Manifest) {
			m.Install.Image = "docker.io/acme/search:latest@sha256:" + strings.Repeat("a", 64)
		}, "names the tag latest"},
		{"image short digest", containerManifest, func(m *Manifest) { m.Install.Image = "acme/search@sha256:abc" }, "pinned by digest"},
		{"registry port is no tag", containerManifest, func(m *Manifest) {
			m.Install.Image = "localhost:5000/search@sha256:" + strings.Repeat("a", 64)
		}, ""},
		{"container version", containerManifest, func(m *Manifest) { m.Install.Version = "1.0.0" }, "install.version is for"},

		{"binary without downloads", binaryManifest, func(m *Manifest) { m.Install.Binaries = nil }, "needs url_<os>_<arch>"},
		{"binary without sha256", binaryManifest, func(m *Manifest) {
			m.Install.Binaries["darwin_arm64"] = Binary{URL: "https://example.org/x.tar.gz"}
		}, "install.sha256_darwin_arm64"},
		{"binary without url", binaryManifest, func(m *Manifest) {
			m.Install.Binaries["linux_amd64"] = Binary{SHA256: strings.Repeat("c", 64)}
		}, "install.url_linux_amd64"},
		{"binary http url", binaryManifest, func(m *Manifest) {
			m.Install.Binaries["darwin_arm64"] = Binary{URL: "http://example.org/x", SHA256: strings.Repeat("b", 64)}
		}, "must be an https URL"},
		{"binary unknown platform", binaryManifest, func(m *Manifest) {
			m.Install.Binaries["plan9_386"] = Binary{URL: "https://example.org/x", SHA256: strings.Repeat("b", 64)}
		}, `platform "plan9_386" is unknown`},

		{"field without id", valid, func(m *Manifest) { m.Fields[0].ID = "" }, "field 1 has no id"},
		{"duplicate field id", valid, func(m *Manifest) { m.Fields[1].ID = "dir" }, `field id "dir" is used twice`},
		{"unknown field type", valid, func(m *Manifest) { m.Fields[0].Type = "date" }, `type "date" is unknown`},
		{"secret with default", valid, func(m *Manifest) { m.Fields[1].Default = "hunter2" }, "can't have a default"},
		{"bad pattern", valid, func(m *Manifest) { m.Fields[0].Pattern = "([a-z" }, "doesn't compile"},
		{"default misses pattern", valid, func(m *Manifest) {
			m.Fields[0].Pattern = "^/"
			m.Fields[0].Default = "notes"
		}, "doesn't match its pattern"},
		{"choice without choices", valid, func(m *Manifest) { m.Fields[0].Type = FieldChoice }, "needs choices"},
		{"field without label", valid, func(m *Manifest) { m.Fields[0].Label = "" }, "has no label"},

		{"stdio without command", valid, func(m *Manifest) { m.Launch.Command = "" }, "launch.command is missing"},
		{"stdio with url", valid, func(m *Manifest) { m.Launch.URL = "http://127.0.0.1:1/" }, `launch.url isn't used by kind "stdio"`},
		{"placeholder names unknown field", valid, func(m *Manifest) { m.Launch.Args = []string{"{field.vault}"} }, "names no field"},
		{"unknown placeholder", valid, func(m *Manifest) { m.Launch.Command = "{home}/bin/notes" }, "placeholder {home} is unknown"},
		{"npm command is a path", valid, func(m *Manifest) { m.Launch.Command = "{pkg}/node_modules/.bin/notes-mcp" }, "must be the name of a program"},
		{"pip command is a path", httpManifest, func(m *Manifest) { m.Launch.Command = "/usr/bin/notes" }, "must be the name of a program"},
		{"binary command outside pkg", binaryManifest, func(m *Manifest) { m.Launch.Command = "/usr/bin/notes" }, "must be a file under {pkg}/"},
		{"binary command climbs out", binaryManifest, func(m *Manifest) { m.Launch.Command = "{pkg}/../notes" }, "must be a file under {pkg}/"},
		{"secret on command line", valid, func(m *Manifest) { m.Launch.Args = []string{"--key", "{field.key}"} }, "can't go on the command line"},
		{"http url not loopback", httpManifest, func(m *Manifest) { m.Launch.URL = "http://example.org:8000/mcp" }, "launch.url"},
		{"http url missing", httpManifest, func(m *Manifest) { m.Launch.URL = "" }, "launch.url is missing"},
		{"http port mismatch", httpManifest, func(m *Manifest) { m.Launch.Port = 9000 }, "must use launch.port 9000"},
		{"http unknown auth", httpManifest, func(m *Manifest) { m.Launch.Auth = "token" }, `launch.auth "token" is unknown`},
		{"container url not loopback", containerManifest, func(m *Manifest) { m.Launch.URL = "http://10.0.0.5:8888" }, "launch.url"},
		{"container command", containerManifest, func(m *Manifest) { m.Launch.Command = "/bin/search" }, "launch.command isn't used"},

		{"stdio without health tool", valid, func(m *Manifest) { m.Health.Tool = "" }, "health.tool is missing"},
		{"container health path", containerManifest, func(m *Manifest) { m.Health.Path = "healthz" }, "must be a URL path"},
		{"unknown expect", valid, func(m *Manifest) { m.Health.Expect = "ok" }, `health.expect "ok" is unknown`},

		{"wildcard allow", valid, func(m *Manifest) { m.MCP.Allow = []string{"*"} }, "wildcards are refused"},
		{"confirm not allowed", valid, func(m *Manifest) { m.MCP.Confirm = []string{"delete"} }, `"delete" isn't in mcp.allow`},
		{"mcp on a container", containerManifest, func(m *Manifest) { m.MCP.Allow = []string{"search"} }, "[mcp] is for stdio and http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.base()
			tt.edit(&m)
			err := Validate(m)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("Validate: %v, want nil", err)
			case tt.want != "" && err == nil:
				t.Fatalf("Validate passed, want an error with %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("Validate: %v\nwant it to contain %q", err, tt.want)
			}
		})
	}
}

// TestParse checks the TOML side: the per-platform download keys land in
// Binaries, and a key the manifest doesn't have is an error.
func TestParse(t *testing.T) {
	sum := strings.Repeat("d", 64)
	good := `
id = "tool"
name = "Tool"
kind = "stdio"
[install]
type = "binary"
version = "1.2.3"
url_darwin_arm64 = "https://example.org/tool.tgz"
sha256_darwin_arm64 = "` + sum + `"
[launch]
command = "{pkg}/tool"
[health]
tool = "ping"
`
	m, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m.Install.Binaries["darwin_arm64"]; got.URL != "https://example.org/tool.tgz" || got.SHA256 != sum {
		t.Errorf("Binaries[darwin_arm64] = %+v", got)
	}
	if err := Validate(m); err != nil {
		t.Errorf("Validate: %v", err)
	}

	bad := []struct{ name, text, want string }{
		{"unknown key", "id = \"x\"\nversion = \"1.0.0\"\n", "unknown keys: version"},
		{"unknown install key", "[install]\ntype = \"npm\"\ntag = \"latest\"\n", "unknown keys: install.tag"},
		{"url not a string", "[install]\nurl_darwin_arm64 = 3\n", "must be a string"},
		{"bad toml", "id = \n", "parse manifest"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.text))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestLoadFS checks the rules that span files: the file name matches the
// ID, IDs are unique, and a broken manifest fails the whole load.
func TestLoadFS(t *testing.T) {
	ok := "id = \"a\"\nname = \"A\"\nkind = \"dependency\"\n[install]\ntype = \"none\"\n[health]\npath = \"/\"\n"
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"one good file", fstest.MapFS{"m/a.toml": {Data: []byte(ok)}}, ""},
		{"no files", fstest.MapFS{}, "no manifests"},
		{"name doesn't match id", fstest.MapFS{"m/b.toml": {Data: []byte(ok)}}, "must be called a.toml"},
		{"same id twice", fstest.MapFS{
			"m/a.toml": {Data: []byte(ok)},
			"m/c.toml": {Data: []byte(ok)},
		}, `the id "a" is used twice`},
		{"invalid manifest", fstest.MapFS{"m/a.toml": {Data: []byte(strings.Replace(ok, "none", "brew", 1))}}, "is unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFS(tt.files, "m")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("loadFS: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("loadFS = %v, want an error with %q", err, tt.want)
			}
		})
	}
}
