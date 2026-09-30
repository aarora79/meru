// This file tests download and unpackTarGz, and holds the helpers the
// install tests share: building a .tar.gz in memory and serving files
// from a local https server. No test here touches the network.

package connectors

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// tarEntry is one entry of a test archive. Typeflag defaults to a
// regular file.
type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	link     string
}

// makeTarGz builds a .tar.gz holding entries, in order.
func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: e.typeflag, Linkname: e.link}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("tar body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sum returns the SHA-256 of data as hex.
func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// fileServer serves files by URL path over https on loopback, and counts
// the requests, so a test can check that a second call downloads
// nothing. The server closes when the test ends.
type fileServer struct {
	*httptest.Server
	files    map[string][]byte
	requests atomic.Int32
}

// newFileServer starts a fileServer for files.
func newFileServer(t *testing.T, files map[string][]byte) *fileServer {
	t.Helper()
	fs := &fileServer{files: files}
	fs.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.requests.Add(1)
		data, ok := fs.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(fs.Close)
	return fs
}

// TestDownload checks that a file whose SHA-256 matches lands at dest,
// and that a mismatch, a plain http URL or a missing file leaves nothing
// behind.
func TestDownload(t *testing.T) {
	body := []byte("the real file")
	srv := newFileServer(t, map[string][]byte{"/f": body})
	tests := []struct {
		name    string
		url     string
		sha     string
		wantErr error  // checked with errors.Is when set
		wantMsg string // checked with strings.Contains when set
	}{
		{"match", srv.URL + "/f", sum(body), nil, ""},
		{"mismatch", srv.URL + "/f", sum([]byte("another file")), ErrChecksum, ""},
		{"plain http", strings.Replace(srv.URL, "https://", "http://", 1) + "/f", sum(body), nil, "only over https"},
		{"not found", srv.URL + "/missing", sum(body), nil, "404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "out")
			err := download(context.Background(), srv.Client(), tt.url, tt.sha, dest)
			wantOK := tt.wantErr == nil && tt.wantMsg == ""
			switch {
			case wantOK && err != nil:
				t.Fatalf("download: %v", err)
			case !wantOK && err == nil:
				t.Fatal("download passed, want an error")
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("download: %v, want %v", err, tt.wantErr)
			case tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg):
				t.Fatalf("download: %v, want it to contain %q", err, tt.wantMsg)
			}
			entries, _ := os.ReadDir(dir)
			if wantOK {
				got, _ := os.ReadFile(dest)
				if !bytes.Equal(got, body) || len(entries) != 1 {
					t.Errorf("dest holds %q and the folder %d files; want the body alone", got, len(entries))
				}
			} else if len(entries) != 0 {
				t.Errorf("a failed download left %d files behind", len(entries))
			}
		})
	}
}

// TestUnpackTarGz unpacks a good archive, then refuses each unsafe
// entry, and checks that nothing lands outside the destination.
func TestUnpackTarGz(t *testing.T) {
	good := []tarEntry{
		{name: "node-v1/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "node-v1/bin/node", body: "#!/bin/sh\n", mode: 0o755},
		{name: "node-v1/lib/node_modules/npm/bin/npm-cli.js", body: "// npm"},
		{name: "node-v1/bin/npm", typeflag: tar.TypeSymlink, link: "../lib/node_modules/npm/bin/npm-cli.js"},
		{name: "node-v1/README.md", body: "read me"},
	}
	tests := []struct {
		name    string
		entries []tarEntry
		unsafe  bool
	}{
		{"good", good, false},
		{"dot-dot at the start", []tarEntry{{name: "../evil", body: "x"}}, true},
		{"dot-dot inside", []tarEntry{{name: "top/../../evil", body: "x"}}, true},
		{"absolute name", []tarEntry{{name: "/tmp/evil", body: "x"}}, true},
		{"absolute link", []tarEntry{{name: "top/passwd", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}, true},
		{"link climbs out", []tarEntry{{name: "top/a/b", typeflag: tar.TypeSymlink, link: "../../../evil"}}, true},
		{"hard link", []tarEntry{{name: "top/h", typeflag: tar.TypeLink, link: "top/bin/node"}}, true},
		{"device", []tarEntry{{name: "top/dev", typeflag: tar.TypeChar}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			archive := filepath.Join(parent, "a.tar.gz")
			if err := os.WriteFile(archive, makeTarGz(t, tt.entries), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(parent, "out")
			if err := os.Mkdir(dest, 0o750); err != nil {
				t.Fatal(err)
			}
			err := unpackTarGz(archive, dest)
			if tt.unsafe {
				if !errors.Is(err, ErrUnsafeArchive) {
					t.Fatalf("unpack: %v, want ErrUnsafeArchive", err)
				}
				if _, err := os.Lstat(filepath.Join(parent, "evil")); err == nil {
					t.Fatal("an entry landed outside the destination")
				}
				return
			}
			if err != nil {
				t.Fatalf("unpack: %v", err)
			}
			info, err := os.Stat(filepath.Join(dest, "bin", "node"))
			if err != nil || info.Mode()&0o100 == 0 {
				t.Fatalf("bin/node: %v, mode %v; want a file the owner can run", err, info)
			}
			if got, err := os.ReadFile(filepath.Join(dest, "bin", "npm")); err != nil || string(got) != "// npm" {
				t.Errorf("bin/npm reads %q, %v; want the link to reach npm-cli.js", got, err)
			}
			if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
				t.Errorf("README.md: %v", err)
			}
		})
	}
}
