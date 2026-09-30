// This file tests LaunchCommand: the npm program runs with the pinned
// Node, the pip program from its Python environment, placeholders fill
// in, and a program that leaves its package is refused.

package connectors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLaunchCommandNPM installs the fake npm package with each form of
// package.json's "bin", and checks the program, arguments and PATH.
func TestLaunchCommandNPM(t *testing.T) {
	tests := []struct {
		name    string
		bin     any
		wantErr string
	}{
		{"bin is one path", "dist/main.js", ""},
		{"bin is a map", map[string]string{"notes-mcp": "dist/main.js", "other": "x.js"}, ""},
		{"no such program", map[string]string{"other": "x.js"}, "has no program called notes-mcp"},
		{"bin climbs out", map[string]string{"notes-mcp": "../../../evil.js"}, "points outside the package"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := &fakeRuns{}
			in, _ := testInstaller(t, fakeRunner(runs, fakeNPM(tt.bin)))
			m := npmManifest("1.0.0")
			inst, err := in.Install(context.Background(), m, nil)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			fields := map[string]string{"vault_path": "/Users/you/Notes"}
			path, args, env, err := in.LaunchCommand(m, inst, fields)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LaunchCommand: %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LaunchCommand: %v", err)
			}
			nodeBin := filepath.Join(in.runtimeDir(), "node-99.0.0", "bin")
			if path != filepath.Join(nodeBin, "node") {
				t.Errorf("path = %s, want the pinned node", path)
			}
			// The vault name has no value, so it takes its default.
			wantArgs := []string{
				filepath.Join(inst.Dir, "node_modules", "notes-mcp", "dist", "main.js"),
				"serve", "--vault", "vault=/Users/you/Notes",
			}
			if !slices.Equal(args, wantArgs) {
				t.Errorf("args = %q, want %q", args, wantArgs)
			}
			if !strings.HasPrefix(env["PATH"], nodeBin+string(os.PathListSeparator)) {
				t.Errorf("PATH = %s, want the pinned node's bin first", env["PATH"])
			}
		})
	}
}

// TestLaunchCommandRealObsidian runs LaunchCommand on the real obsidian
// manifest, over a fake install, to prove the manifest's command and
// arguments fit it.
func TestLaunchCommandRealObsidian(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m Manifest) bool { return m.ID == "obsidian" })
	m := ms[i]
	in, _ := testInstaller(t, nil)
	in.Run = fakeRunner(&fakeRuns{}, func(c Cmd) (string, error) {
		prefix := c.Args[slices.Index(c.Args, "--prefix")+1]
		pkgDir := filepath.Join(prefix, "node_modules", "obsidian-mcp")
		if err := os.MkdirAll(filepath.Join(pkgDir, "dist"), 0o750); err != nil {
			return "", err
		}
		// obsidian-mcp 2.0.1's own package.json has this "bin".
		if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"bin":{"obsidian-mcp":"dist/main.js"}}`), 0o600); err != nil {
			return "", err
		}
		return "", os.WriteFile(filepath.Join(pkgDir, "dist", "main.js"), nil, 0o600)
	})
	inst, err := in.Install(context.Background(), m, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if inst.Dir != filepath.Join(in.runtimeDir(), "pkg", "obsidian-2.0.1") {
		t.Errorf("Dir = %s", inst.Dir)
	}
	_, args, _, err := in.LaunchCommand(m, inst, map[string]string{"vault_path": "/v", "vault_name": "notes"})
	if err != nil {
		t.Fatalf("LaunchCommand: %v", err)
	}
	if strings.Join(args[1:], " ") != "serve --vault notes=/v" {
		t.Errorf("args = %q", args)
	}

	// With no vault name and no default, the caller must make one.
	_, _, _, err = in.LaunchCommand(m, inst, map[string]string{"vault_path": "/v"})
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("LaunchCommand without vault_name: %v, want ErrMissingField", err)
	}
}

// TestLaunchCommandPip checks that a pip program runs from the package's
// Python environment and that env placeholders, a secret among them,
// fill in.
func TestLaunchCommandPip(t *testing.T) {
	in, _ := testInstaller(t, fakeRunner(&fakeRuns{}, nil))
	m := Manifest{
		ID: "mail", Name: "Mail", Kind: KindHTTP,
		Install: Install{Type: InstallPip, Package: "mail-mcp", Version: "1.30.0"},
		Launch: Launch{
			Command: "mail-mcp", Args: []string{"--transport", "streamable-http"},
			Env: map[string]string{"MAIL_SECRET": "{field.secret}", "OUT": "{meru_dir}/out"},
			URL: "http://127.0.0.1:8000/mcp", Port: 8000, Auth: AuthNone,
		},
		Fields: []Field{{ID: "secret", Type: FieldSecret, Label: "Secret"}},
		Health: Health{Tool: "list"},
	}
	inst, err := in.Install(context.Background(), m, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	venvBin := filepath.Join(inst.Dir, ".venv", "bin")
	_, _, _, err = in.LaunchCommand(m, inst, map[string]string{"secret": "s3"})
	if err == nil || !strings.Contains(err.Error(), "install it again") {
		t.Fatalf("LaunchCommand before the program exists: %v", err)
	}
	if err := os.MkdirAll(venvBin, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvBin, "mail-mcp"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path, args, env, err := in.LaunchCommand(m, inst, map[string]string{"secret": "s3"})
	if err != nil {
		t.Fatalf("LaunchCommand: %v", err)
	}
	if path != filepath.Join(venvBin, "mail-mcp") || !slices.Equal(args, []string{"--transport", "streamable-http"}) {
		t.Errorf("path %s, args %q", path, args)
	}
	if env["MAIL_SECRET"] != "s3" || env["OUT"] != filepath.Join(in.MeruDir, "out") {
		t.Errorf("env = %v", env)
	}
	if !strings.HasPrefix(env["PATH"], venvBin+string(os.PathListSeparator)) {
		t.Errorf("PATH = %s, want the environment's bin first", env["PATH"])
	}
}

// TestLaunchCommandContainer checks that a container has no command.
func TestLaunchCommandContainer(t *testing.T) {
	in, _ := testInstaller(t, nil)
	m := Manifest{ID: "search", Kind: KindContainer, Install: Install{Type: InstallContainer}}
	if _, _, _, err := in.LaunchCommand(m, Installed{}, nil); err == nil {
		t.Error("LaunchCommand passed for a container")
	}
}
