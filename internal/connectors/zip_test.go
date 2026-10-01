// This file tests the .zip side of the runtime installer: unpackZip's
// rules, which match unpackTarGz's, and EnsureRuntime on a zip pin, the
// way chrome-headless-shell ships.

package connectors

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// zipEntry is one entry for makeZip: a folder (name ending in "/"), a
// file with body, or a symbolic link to link.
type zipEntry struct {
	name string
	body string
	link string
	mode os.FileMode
}

// makeZip builds a zip file in memory from entries.
func makeZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.link != "":
			h.SetMode(os.ModeSymlink | 0o777)
		case e.name[len(e.name)-1] == '/':
			h.SetMode(os.ModeDir | 0o755)
		default:
			mode := e.mode
			if mode == 0 {
				mode = 0o644
			}
			h.SetMode(mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		body := e.body
		if e.link != "" {
			body = e.link
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestUnpackZip unpacks a good zip, keeping the program's execute bit and
// a link that stays inside, then refuses each unsafe entry.
func TestUnpackZip(t *testing.T) {
	good := makeZip(t, []zipEntry{
		{name: "chrome-headless-shell-test/"},
		{name: "chrome-headless-shell-test/chrome-headless-shell", body: "#!/bin/sh\n", mode: 0o755},
		{name: "chrome-headless-shell-test/resources/a.pak", body: "pak"},
		{name: "chrome-headless-shell-test/latest", link: "chrome-headless-shell"},
	})
	dest := t.TempDir()
	path := filepath.Join(t.TempDir(), "good.zip")
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unpack(path, dest); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "chrome-headless-shell"))
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("the program is missing or not executable: %v %v", info, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "resources", "a.pak")); string(got) != "pak" {
		t.Errorf("resources/a.pak = %q", got)
	}
	if target, err := os.Readlink(filepath.Join(dest, "latest")); err != nil || target != "chrome-headless-shell" {
		t.Errorf("latest -> %q (%v)", target, err)
	}

	bad := []struct {
		name    string
		entries []zipEntry
	}{
		{"climbs out", []zipEntry{{name: "top/../../evil", body: "x"}}},
		{"absolute", []zipEntry{{name: "/etc/evil", body: "x"}}},
		{"link out", []zipEntry{{name: "top/out", link: "../../etc/passwd"}}},
		{"absolute link", []zipEntry{{name: "top/out", link: "/etc/passwd"}}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.zip")
			if err := os.WriteFile(path, makeZip(t, tt.entries), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := t.TempDir()
			if err := unpackZip(path, dest); !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("unpackZip = %v, want ErrUnsafeArchive", err)
			}
		})
	}
}

// TestEnsureRuntimeZip installs a runtime pinned as a zip, as
// chrome-headless-shell is, and refuses one whose SHA-256 is wrong.
func TestEnsureRuntimeZip(t *testing.T) {
	archive := makeZip(t, []zipEntry{
		{name: "chrome-headless-shell-test/chrome-headless-shell", body: "#!/bin/sh\n", mode: 0o755},
	})
	srv := newFileServer(t, map[string][]byte{"/chs.zip": archive})
	pin := func(sha string) map[string]Runtime {
		return map[string]Runtime{RuntimeChrome: {Name: RuntimeChrome, Version: "1.2.3", Program: "chrome-headless-shell",
			Downloads: map[string]Binary{testPlatform: {URL: srv.URL + "/chs.zip", SHA256: sha}}}}
	}
	in := &Installer{MeruDir: filepath.Join(t.TempDir(), ".meru"), Platform: testPlatform, Runtimes: pin(sum(archive)), Client: srv.Client()}
	dir, err := in.EnsureRuntime(context.Background(), RuntimeChrome, nil)
	if err != nil {
		t.Fatalf("EnsureRuntime: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chrome-headless-shell")); err != nil {
		t.Fatalf("the program: %v", err)
	}
	if _, ok := in.RuntimeDir(RuntimeChrome); !ok {
		t.Error("RuntimeDir doesn't find the installed runtime")
	}

	wrong := &Installer{MeruDir: filepath.Join(t.TempDir(), ".meru"), Platform: testPlatform, Runtimes: pin(sum([]byte("other"))), Client: srv.Client()}
	if _, err := wrong.EnsureRuntime(context.Background(), RuntimeChrome, nil); !errors.Is(err, ErrChecksum) {
		t.Fatalf("EnsureRuntime with a wrong pin = %v, want ErrChecksum", err)
	}
}

// TestChromePins checks that the shipped pin covers the four Unix
// platforms with https URLs and full SHA-256s, and leaves Windows out.
func TestChromePins(t *testing.T) {
	rt, ok := Runtimes()[RuntimeChrome]
	if !ok || rt.Program != "chrome-headless-shell" {
		t.Fatalf("no chrome-headless-shell pin: %+v", rt)
	}
	for _, p := range []string{"darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64"} {
		b, ok := rt.Downloads[p]
		if !ok || len(b.SHA256) != 64 || filepath.Ext(b.URL) != ".zip" || b.URL[:8] != "https://" {
			t.Errorf("%s: %+v", p, b)
		}
	}
	if _, ok := rt.Downloads["windows_amd64"]; ok {
		t.Error("Windows has a pin, but StartPiped can't run there")
	}
}
