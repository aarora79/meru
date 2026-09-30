// This file downloads a file, checks its SHA-256 before anything else
// reads it, and unpacks a .tar.gz archive into a folder that no entry can
// climb out of. The runtimes (runtimes.go) and binary installs
// (install.go) use it.

package connectors

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrChecksum means a download's SHA-256 differs from the pin. The file
// may be corrupt, or someone may have changed it; either way Meru deletes
// it and runs nothing from it.
var ErrChecksum = errors.New("the download's SHA-256 doesn't match the pin")

// ErrUnsafeArchive means an archive entry would land outside the folder
// it unpacks into, or is a kind of file Meru doesn't unpack.
var ErrUnsafeArchive = errors.New("the archive holds an unsafe entry")

// maxDownload caps one download at 512 MiB. Node's archive is about
// 50 MiB; the cap stops a broken server from filling the disk.
const maxDownload = 512 << 20

// maxUnpacked caps what one archive may unpack to, 2 GiB, for the same
// reason: a small archive can inflate to a huge one.
const maxUnpacked = 2 << 30

// download fetches rawURL into the file dest, and fails unless the
// file's SHA-256 is wantSHA. It writes to a temporary file beside dest
// and renames it into place only after the check passes, so dest never
// holds an unchecked file. It accepts only https URLs.
func download(ctx context.Context, client *http.Client, rawURL, wantSHA, dest string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("download %s: Meru downloads only over https", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	// defer runs resp.Body.Close() when this function returns, even on
	// an error.
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: the server answered %s", rawURL, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	// Remove the temporary file on every path that doesn't rename it.
	// After a rename the name no longer exists, and Remove fails quietly.
	defer os.Remove(tmp.Name())

	// io.MultiWriter sends each byte both to the file and to the hash,
	// so the file is read once. LimitReader stops at the cap plus one
	// byte, so an oversized file shows up as too long.
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxDownload+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	if n > maxDownload {
		return fmt.Errorf("download %s: the file is larger than %d MiB", rawURL, maxDownload>>20)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("download %s: got %s, want %s: %w", rawURL, got, wantSHA, ErrChecksum)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("download %s: %w", rawURL, err)
	}
	return nil
}

// unpackTarGz unpacks the .tar.gz file archive into the folder dest,
// which must exist and be empty. It drops the first part of every entry's
// name, since the Node and uv archives each hold one top folder, such as
// node-v24.21.0-darwin-arm64/.
//
// It fails with ErrUnsafeArchive on an entry whose name is absolute or
// climbs with "..", a link that points outside dest or is absolute, a
// hard link, a device, or anything but a folder, a file or a symbolic
// link. It writes through os.Root, which refuses any path that leaves
// dest even through a link, so a check this function missed still can't
// write outside.
func unpackTarGz(archive, dest string) error {
	f, err := os.Open(archive) // #nosec G304 -- archive is a file download just wrote and checked
	if err != nil {
		return fmt.Errorf("unpack %s: %w", archive, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", archive, err)
	}
	defer gz.Close()

	root, err := os.OpenRoot(dest)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", archive, err)
	}
	defer root.Close()

	tr := tar.NewReader(gz)
	var written int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("unpack %s: %w", archive, err)
		}
		name, err := entryName(hdr.Name)
		if err != nil {
			return fmt.Errorf("unpack %s: %w", archive, err)
		}
		if name == "" {
			// The top folder itself.
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o750); err != nil {
				return fmt.Errorf("unpack %s: %w", archive, err)
			}
		case tar.TypeReg:
			if hdr.Size > maxUnpacked-written {
				return fmt.Errorf("unpack %s: it unpacks to more than %d MiB", archive, maxUnpacked>>20)
			}
			if err := writeEntry(root, name, hdr, tr); err != nil {
				return fmt.Errorf("unpack %s: %w", archive, err)
			}
			written += hdr.Size
		case tar.TypeSymlink:
			if err := linkInside(name, hdr.Linkname); err != nil {
				return fmt.Errorf("unpack %s: %w", archive, err)
			}
			if err := root.MkdirAll(path.Dir(name), 0o750); err != nil {
				return fmt.Errorf("unpack %s: %w", archive, err)
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return fmt.Errorf("unpack %s: %w", archive, err)
			}
		case tar.TypeXGlobalHeader:
			// A pax header that describes the archive, not a file.
		default:
			return fmt.Errorf("unpack %s: %q has type %q: %w", archive, hdr.Name, hdr.Typeflag, ErrUnsafeArchive)
		}
	}
}

// entryName turns an archive entry's name into a path inside the
// destination, with the first part dropped. It returns "" for anything at
// the top level, the top folder itself or a stray file beside it, which
// the caller skips. It fails on an absolute name, a backslash, or a ".."
// part anywhere in the name.
func entryName(raw string) (string, error) {
	// Archives use "/" on every system, so the path package, not
	// filepath, reads them.
	if path.IsAbs(raw) || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%q: %w", raw, ErrUnsafeArchive)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", fmt.Errorf("%q: %w", raw, ErrUnsafeArchive)
		}
	}
	_, rest, found := strings.Cut(path.Clean(raw), "/")
	if !found {
		return "", nil
	}
	return rest, nil
}

// linkInside fails unless the symbolic link at name, pointing at target,
// stays inside the destination. Node's archive holds relative links such
// as bin/npm -> ../lib/node_modules/npm/bin/npm-cli.js; an absolute link
// or one that climbs out could make a later program read or write
// anywhere.
func linkInside(name, target string) error {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") {
		return fmt.Errorf("link %q -> %q: %w", name, target, ErrUnsafeArchive)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("link %q -> %q: %w", name, target, ErrUnsafeArchive)
	}
	return nil
}

// writeEntry writes one regular file from the archive. It keeps the
// entry's execute bit, which Node's bin/node needs, and drops every other
// permission bit but the owner's and group's read.
func writeEntry(root *os.Root, name string, hdr *tar.Header, r io.Reader) error {
	if err := root.MkdirAll(path.Dir(name), 0o750); err != nil {
		return err
	}
	perm := os.FileMode(0o640)
	if hdr.FileInfo().Mode()&0o111 != 0 {
		perm = 0o750
	}
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	// CopyN copies exactly the size the header gave; a short entry is an
	// error rather than a truncated program.
	_, err = io.CopyN(out, r, hdr.Size)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
