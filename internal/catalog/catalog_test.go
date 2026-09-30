// This file tests the catalog entries, Block, Custom and AppendServer.

package catalog

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// TestEntries checks each catalog entry on its own: the fields a caller
// relies on are set, allow lists name tools with no wildcards, confirm sits
// inside allow, and every secret the env names has a Need that asks for it.
func TestEntries(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Entries() {
		t.Run(e.Name, func(t *testing.T) {
			if err := CheckName(e.Name); err != nil {
				t.Error(err)
			}
			if seen[e.Name] {
				t.Errorf("two entries named %q", e.Name)
			}
			seen[e.Name] = true
			if e.Title == "" || e.Description == "" || e.Docs == "" || e.Install == "" || e.Requires == "" {
				t.Error("Title, Description, Docs, Install and Requires must all be set")
			}
			switch e.Transport {
			case TransportStdio:
				if e.Command == "" || e.URL != "" || e.Start != "" {
					t.Errorf("stdio entry: command %q, url %q, start %q; want a command only", e.Command, e.URL, e.Start)
				}
			case TransportHTTP:
				// merud never starts an HTTP server, so the entry must say
				// how the user does, and carry no env merud couldn't set.
				if e.URL == "" || e.Command != "" || e.Start == "" || len(e.Env) > 0 || e.Remote {
					t.Errorf("http entry: url %q, command %q, start %q, env %v, remote %v; want a loopback url and a start command",
						e.URL, e.Command, e.Start, e.Env, e.Remote)
				}
			default:
				t.Errorf("transport %q is neither stdio nor http", e.Transport)
			}
			if len(e.Allow) == 0 {
				t.Error("allow is empty; the entry would give the model nothing")
			}
			for _, tool := range append(slices.Clone(e.Allow), e.Confirm...) {
				if tool == "" || strings.ContainsAny(tool, "*? ") {
					t.Errorf("tool %q: name each tool exactly", tool)
				}
			}
			for _, tool := range e.Confirm {
				if !slices.Contains(e.Allow, tool) {
					t.Errorf("confirm %q is not in allow", tool)
				}
			}
			asked := map[string]bool{}
			for _, n := range e.Needs {
				switch n.Kind {
				case NeedAPIKey:
					if n.SecretName == "" {
						t.Error("an api_key Need has no SecretName")
					}
					asked[n.SecretName] = true
				case NeedPath, NeedURL:
					if n.Env == "" {
						t.Errorf("a %s Need has no Env", n.Kind)
					}
				case NeedNote:
				default:
					t.Errorf("unknown Need kind %q", n.Kind)
				}
				if n.Prompt == "" {
					t.Errorf("a %s Need has no Prompt", n.Kind)
				}
			}
			for _, name := range e.SecretNames() {
				if !asked[name] {
					t.Errorf("env names secret %q, but no Need asks for it", name)
				}
			}
		})
	}
}

// TestBlocksLoad appends every catalog block to one config file and checks
// that config.Load accepts it and reads each entry back as written.
func TestBlocksLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, e := range Entries() {
		if err := AppendServer(path, Block(e)); err != nil {
			t.Fatalf("AppendServer(%s): %v", e.Name, err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := Entries()
	if len(cfg.MCP.Servers) != len(entries) {
		t.Fatalf("config has %d servers, want %d", len(cfg.MCP.Servers), len(entries))
	}
	for i, s := range cfg.MCP.Servers {
		e := entries[i]
		if s.Name != e.Name || s.Command != e.Command || s.URL != e.URL || !slices.Equal(s.Args, e.Args) ||
			!slices.Equal(s.Allow, e.Allow) || len(s.Env) != len(e.Env) {
			t.Errorf("server %d read back as %+v, want the %s entry", i, s, e.Name)
		}
		if !slices.Equal(s.Confirm, e.Confirm) && (len(s.Confirm) != 0 || len(e.Confirm) != 0) {
			t.Errorf("%s confirm = %v, want %v", s.Name, s.Confirm, e.Confirm)
		}
	}
}

func TestCustom(t *testing.T) {
	tests := []struct {
		name, target string
		args         []string
		wantURL      bool
		wantRemote   bool
	}{
		{"notes", "/usr/local/bin/notes-mcp", []string{"--vault", "a b"}, false, false},
		{"local", "http://127.0.0.1:8123/mcp", nil, true, false},
		{"remote", "https://mcp.example.com/mcp", nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Custom(tt.name, tt.target, tt.args)
			if (e.URL != "") != tt.wantURL || e.Remote != tt.wantRemote {
				t.Errorf("Custom = %+v, want url %v remote %v", e, tt.wantURL, tt.wantRemote)
			}
			if len(e.Allow) != 0 {
				t.Errorf("allow = %v, want empty", e.Allow)
			}
			block := Block(e)
			if !strings.Contains(block, "meru tools") {
				t.Errorf("block doesn't say to run meru tools:\n%s", block)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := AppendServer(path, block); err != nil {
				t.Fatalf("AppendServer: %v\n%s", err, block)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			s := cfg.MCP.Servers[0]
			if s.Command != e.Command || s.URL != e.URL || !slices.Equal(s.Args, e.Args) || s.Remote != e.Remote {
				t.Errorf("read back %+v, want %+v", s, e)
			}
		})
	}
}

// TestQuote checks that awkward strings survive a trip through Block and
// the TOML parser unchanged.
func TestQuote(t *testing.T) {
	odd := []string{`back\slash`, `"quoted"`, "tab\there", "new\nline", "bell\a", "ünïcode"}
	e := Custom("odd", "prog", odd)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := AppendServer(path, Block(e)); err != nil {
		t.Fatalf("AppendServer: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MCP.Servers[0].Args; !slices.Equal(got, odd) {
		t.Errorf("args = %q, want %q", got, odd)
	}
}

// TestAppendServerKeepsFile checks that the text already in config.toml,
// comments included, comes through unchanged, and that the file ends up
// with mode 0600.
func TestAppendServerKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := "# my settings\nprofile = \"lite\" # the small one\n\n[index]\nfolders = [\"~/notes\"]"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	e, _ := Find("obsidian")
	if err := AppendServer(path, Block(e)); err != nil {
		t.Fatalf("AppendServer: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := orig + "\n\n" + Block(e); string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Index.Folders) != 1 || len(cfg.MCP.Servers) != 1 {
		t.Errorf("config lost a setting: %+v", cfg)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %#o, want 0600", perm)
		}
	}
}

func TestAppendServerRefuses(t *testing.T) {
	obsidian, _ := Find("obsidian")
	tests := []struct {
		name     string
		existing string // config.toml before the append; "" means no file
		block    string
		wantErr  string
	}{
		{"duplicate name", Block(obsidian), Block(obsidian), "already has"},
		{"broken config", "profile = \"huge\"\n", Block(obsidian), "fix config.toml"},
		{"not toml", "", "[[mcp.servers]\nname = ", "server block"},
		{"no server", "", "profile = \"lite\"\n", "holds 0 servers"},
		{"wildcard", "", "[[mcp.servers]]\nname = \"w\"\ncommand = \"x\"\nallow = [\"*\"]\n", "wildcards"},
		{"confirm outside allow", "", "[[mcp.servers]]\nname = \"c\"\ncommand = \"x\"\nallow = [\"a\"]\nconfirm = [\"b\"]\n", "not in allow"},
		{"bad name", "", "[[mcp.servers]]\nname = \"a.b\"\ncommand = \"x\"\n", "letters, digits"},
		{"command and url", "", "[[mcp.servers]]\nname = \"u\"\ncommand = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\n", "exactly one"},
		{"unknown key", "", "[[mcp.servers]]\nname = \"k\"\ncommand = \"x\"\nallows = [\"a\"]\n", "unknown keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if tt.existing != "" {
				if err := os.WriteFile(path, []byte(tt.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := AppendServer(path, tt.block)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("AppendServer error = %v, want one containing %q", err, tt.wantErr)
			}
			// The file must be as it was, and no temporary file left over.
			got, _ := os.ReadFile(path)
			if string(got) != tt.existing {
				t.Errorf("file changed to:\n%s", got)
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if want := min(len(tt.existing), 1); len(entries) != want {
				t.Errorf("folder holds %d files, want %d", len(entries), want)
			}
		})
	}
}

func TestSecretNames(t *testing.T) {
	obsidian, _ := Find("obsidian")
	want := []string{"obsidian_api_key"}
	if got := obsidian.SecretNames(); !slices.Equal(got, want) {
		t.Errorf("SecretNames = %v, want %v", got, want)
	}
	// google's OAuth client goes to the server the user starts, never
	// through Meru.
	google, _ := Find("google")
	if got := google.SecretNames(); len(got) != 0 {
		t.Errorf("google SecretNames = %v, want none", got)
	}
	if _, ok := Find("nope"); ok {
		t.Error("Find found an entry that doesn't exist")
	}
}

// TestCatalogIsTwoServers pins the catalog to the two servers the docs
// draw their examples from, in the order setup offers them. Web search is
// a built-in, so no search server belongs here.
func TestCatalogIsTwoServers(t *testing.T) {
	if got, want := Names(), []string{"google", "obsidian"}; !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	google, _ := Find("google")
	if google.URL != "http://127.0.0.1:8000/mcp" || !strings.Contains(google.Start, "uvx workspace-mcp==1.30.0 --transport streamable-http") {
		t.Errorf("google = url %q, start %q", google.URL, google.Start)
	}
	for _, tool := range []string{"send_gmail_message", "manage_event"} {
		if !slices.Contains(google.Confirm, tool) {
			t.Errorf("google confirm lacks %s, which sends or changes something", tool)
		}
	}
}

// TestGoogleAttachments checks the google entry's side of reading a mail
// attachment: the model may call get_gmail_attachment_content without a
// question, since it only reads, and the start command makes the server
// save attachments where read_file reads.
func TestGoogleAttachments(t *testing.T) {
	google, _ := Find("google")
	const tool = "get_gmail_attachment_content"
	if !slices.Contains(google.Allow, tool) {
		t.Errorf("google allow = %v, want it to hold %s", google.Allow, tool)
	}
	if slices.Contains(google.Confirm, tool) {
		t.Errorf("google confirm = %v, want it without %s, which only reads", google.Confirm, tool)
	}
	const env = "WORKSPACE_ATTACHMENT_DIR=~/meru-output/attachments "
	if !strings.Contains(google.Start, env) {
		t.Errorf("google start = %q, want it to set %q", google.Start, env)
	}
	// The variable must come before the program, so the shell passes it
	// to the server.
	if strings.Index(google.Start, env) > strings.Index(google.Start, "uvx ") {
		t.Errorf("google start = %q, want %q before uvx", google.Start, env)
	}
}

// TestGoogleToolTier checks that the start command loads workspace-mcp's
// extended tier. get_gmail_attachment_content and get_gmail_thread_content,
// both in Allow, sit in that tier (core/tool_tiers.yaml), so a server on
// the core tier would offer neither.
func TestGoogleToolTier(t *testing.T) {
	google, _ := Find("google")
	if !strings.HasSuffix(google.Start, " --tool-tier extended") {
		t.Errorf("google start = %q, want it to end with --tool-tier extended", google.Start)
	}
	for _, tool := range []string{"get_gmail_attachment_content", "get_gmail_thread_content"} {
		if !slices.Contains(google.Allow, tool) {
			t.Errorf("google allow lacks %s; drop the tier to core if no allowed tool needs extended", tool)
		}
	}
}

// TestTemplateHoldsCatalog checks that the config template holds each
// catalog entry exactly as Block renders it, commented out line by line.
// The config package can't call Block itself, because this package
// imports config, so the template holds a hand-written copy and this test
// keeps the two from drifting. When it fails, paste Block's output,
// commented, over the old block in internal/config/template.toml, then
// copy the template over config.example.toml.
func TestTemplateHoldsCatalog(t *testing.T) {
	for _, e := range Entries() {
		var want strings.Builder
		for _, line := range strings.Split(strings.TrimSuffix(Block(e), "\n"), "\n") {
			want.WriteString("# " + line + "\n")
		}
		if !strings.Contains(config.Template(), want.String()) {
			t.Errorf("the config template doesn't hold the %s entry as Block renders it; want:\n%s", e.Name, want.String())
		}
	}
}
