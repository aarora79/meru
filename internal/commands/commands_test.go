// This file tests New's startup checks, one case per rejection, and the
// placeholder syntax and interpreter rule they rely on.

package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// testHome makes a temporary home directory with a "repos" folder in it,
// points HOME (USERPROFILE on Windows) at it for this test, and returns
// its path with symbolic links resolved.
func testHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "repos"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// gitLog returns a valid entry that tests change one field of.
func gitLog() config.Command {
	return config.Command{
		Name:        "git-log",
		Description: "Recent commits",
		Argv:        []string{"git", "-C", "{repo}", "log", "--oneline", "-n", "{count}"},
		Params: map[string]config.CommandParam{
			"repo":  {Type: TypePath, Under: "~/repos"},
			"count": {Type: TypeInt, Min: i64(1), Max: i64(50)},
		},
	}
}

// i64 returns a pointer to v, for the optional min and max.
func i64(v int64) *int64 { return &v }

func TestNewAccepts(t *testing.T) {
	home := testHome(t)
	d := gitLog()
	d.Argv = append(d.Argv, "~/notes", "{{literal}}")
	d.Cwd = "~/repos"
	s, err := New([]config.Command{d}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c, ok := s.lookup("cmd.git-log")
	if !ok {
		t.Fatal("cmd.git-log missing")
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want the 30s default", c.Timeout)
	}
	if c.Cwd != filepath.Join(home, "repos") {
		t.Errorf("cwd = %q, want ~/repos expanded", c.Cwd)
	}
	if got := c.Argv[len(c.Argv)-2]; got != filepath.Join(home, "notes") {
		t.Errorf("argv \"~/notes\" became %q", got)
	}
	if c.Params["repo"].Under != filepath.Join(home, "repos") {
		t.Errorf("under = %q", c.Params["repo"].Under)
	}

	// With no cwd, a command starts in the home directory.
	s, err = New([]config.Command{gitLog()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := s.lookup("cmd.git-log"); c.Cwd != home {
		t.Errorf("default cwd = %q, want %q", c.Cwd, home)
	}
}

// TestNewRejects has one case per rejection. Each error must name the
// command, so the user knows which entry to fix.
func TestNewRejects(t *testing.T) {
	home := testHome(t)
	if err := os.WriteFile(filepath.Join(home, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(d *config.Command)
		want   string
	}{
		{"empty name", func(d *config.Command) { d.Name = "" }, "name must be"},
		{"bad name", func(d *config.Command) { d.Name = "git log" }, "letters, digits"},
		{"empty argv", func(d *config.Command) { d.Argv = nil; d.Params = nil }, "argv is empty"},
		{"shell", func(d *config.Command) { d.Argv[0] = "bash" }, "runs code given as text"},
		{"python with a version", func(d *config.Command) { d.Argv[0] = "/usr/bin/python3.12" }, "runs code given as text"},
		{"env", func(d *config.Command) { d.Argv[0] = "env" }, "runs code given as text"},
		{"cmd.exe", func(d *config.Command) { d.Argv[0] = `C:\Windows\System32\CMD.EXE` }, "runs code given as text"},
		{"placeholder in argv[0]", func(d *config.Command) { d.Argv[0] = "{repo}" }, "program must be fixed"},
		{"placeholder with no param", func(d *config.Command) { d.Argv = append(d.Argv, "{branch}") }, "declares no \"branch\""},
		{"param no argv uses", func(d *config.Command) {
			d.Params["since"] = config.CommandParam{Type: TypeString}
		}, "params.since appears in no argv element"},
		{"path with no under", func(d *config.Command) { d.Params["repo"] = config.CommandParam{Type: TypePath} }, "needs under"},
		{"under missing", func(d *config.Command) {
			d.Params["repo"] = config.CommandParam{Type: TypePath, Under: "~/nowhere"}
		}, "nowhere"},
		{"under is a file", func(d *config.Command) {
			d.Params["repo"] = config.CommandParam{Type: TypePath, Under: "~/file"}
		}, "not a folder"},
		{"timeout over the cap", func(d *config.Command) { d.Timeout = "301s" }, "over the 5m0s cap"},
		{"timeout doesn't parse", func(d *config.Command) { d.Timeout = "soon" }, "positive duration"},
		{"unknown type", func(d *config.Command) { d.Params["count"] = config.CommandParam{Type: "float"} }, "type \"float\" is unknown"},
		{"enum with no values", func(d *config.Command) { d.Params["count"] = config.CommandParam{Type: TypeEnum} }, "needs values"},
		{"min above max", func(d *config.Command) {
			d.Params["count"] = config.CommandParam{Type: TypeInt, Min: i64(9), Max: i64(1)}
		}, "min 9 is above max 1"},
		{"key on the wrong type", func(d *config.Command) {
			d.Params["count"] = config.CommandParam{Type: TypeString, Under: "~/repos"}
		}, "under applies only"},
		{"brace with no close", func(d *config.Command) { d.Argv = append(d.Argv, "{repo") }, "no closing"},
		{"brace that closes nothing", func(d *config.Command) { d.Argv = append(d.Argv, "a}b") }, "closes nothing"},
		{"brace around a non-name", func(d *config.Command) { d.Argv = append(d.Argv, "{}") }, "not a placeholder"},
		{"bad env name", func(d *config.Command) { d.EnvAllowlist = []string{"A=B"} }, "not a variable name"},
		{"cwd missing", func(d *config.Command) { d.Cwd = "~/nowhere" }, "cwd"},
		{"relative cwd", func(d *config.Command) { d.Cwd = "repos" }, "absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := gitLog()
			tt.change(&d)
			_, err := New([]config.Command{d}, nil)
			if err == nil {
				t.Fatal("New accepted a bad entry")
			}
			if !strings.Contains(err.Error(), "command \""+d.Name+"\"") {
				t.Errorf("error doesn't name the command: %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to say %q", err, tt.want)
			}
		})
	}

	t.Run("duplicate name", func(t *testing.T) {
		_, err := New([]config.Command{gitLog(), gitLog()}, nil)
		if err == nil || !strings.Contains(err.Error(), `command "git-log": the name is used by an earlier`) {
			t.Errorf("error = %v", err)
		}
	})
}

func TestIsInterpreter(t *testing.T) {
	for _, p := range []string{"sh", "/bin/sh", "bash", "zsh", "fish", "python", "python3", "python3.12", "perl5",
		"ruby", "node", "env", "/usr/bin/env", "pwsh", "powershell.exe", "PowerShell", "cmd", "cmd.exe", "osascript"} {
		if !isInterpreter(p) {
			t.Errorf("isInterpreter(%q) = false", p)
		}
	}
	for _, p := range []string{"git", "ssh", "rg", "df", "/usr/bin/git", "./talk.sh", "shasum", "envsubst"} {
		if isInterpreter(p) {
			t.Errorf("isInterpreter(%q) = true", p)
		}
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		elem string
		want []segment
	}{
		{"log", []segment{{text: "log"}}},
		{"", []segment{{text: ""}}},
		{"{repo}", []segment{{param: "repo"}}},
		{"--repo={repo}", []segment{{text: "--repo="}, {param: "repo"}}},
		{"{a}-{b}", []segment{{param: "a"}, {text: "-"}, {param: "b"}}},
		{"{{literal}}", []segment{{text: "{literal}"}}},
		{"x{{{name}}}", []segment{{text: "x{"}, {param: "name"}, {text: "}"}}},
	}
	for _, tt := range tests {
		got, err := parse(tt.elem)
		if err != nil {
			t.Errorf("parse(%q): %v", tt.elem, err)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("parse(%q) = %+v, want %+v", tt.elem, got, tt.want)
		}
	}
}
