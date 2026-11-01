// This file opens Word (.docx) and Excel (.xlsx) files. Both are Office
// Open XML: a ZIP archive of XML parts. A file from someone else can be
// built to hurt the program that opens it, so every read here stops at a
// limit on the bytes it unpacks, and parts stay in memory: a ZIP entry's
// name never becomes a path on disk.

package index

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Limits on what one Office file may unpack into. They apply to the bytes
// read, never to the sizes the ZIP's own headers claim: a ZIP bomb lies in
// its headers.
const (
	maxZipEntries    = 10_000    // a real .xlsx holds dozens
	maxPartBytes     = 64 << 20  // one XML part, such as a sheet (64 MiB)
	maxUnpackedBytes = 256 << 20 // every part read for one file (256 MiB)
)

// oleSignature starts an OLE compound file. Office saves a
// password-protected .docx or .xlsx inside one, and the old .doc and .xls
// formats use it too.
var oleSignature = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// errEncrypted marks a password-protected Office file.
var errEncrypted = errors.New("the file is password-protected")

// errNotOOXML marks a .docx or .xlsx file that isn't a ZIP archive at all.
var errNotOOXML = errors.New("not a Word or Excel file: it isn't a ZIP archive")

// ooxml reads the parts of one Office Open XML file held in memory. It is
// a plain struct, not an interface: there is one way to read a part.
type ooxml struct {
	zr   *zip.Reader
	read int64 // bytes unpacked so far, across parts
	// The caps part applies: maxPartBytes and maxUnpackedBytes, set by
	// openOOXML. Tests lower them to check the caps without unpacking
	// hundreds of MiB.
	partCap, fileCap int64
}

// openOOXML checks data's signature and opens it as a ZIP. It fails with
// errEncrypted for an OLE file, errNotOOXML for anything else that isn't a
// ZIP, and a wrapped error for a broken ZIP or one with more than
// maxZipEntries entries.
func openOOXML(data []byte) (*ooxml, error) {
	if bytes.HasPrefix(data, oleSignature) {
		return nil, errEncrypted
	}
	// Every ZIP archive starts with "PK", the initials of its inventor.
	if !bytes.HasPrefix(data, []byte("PK")) {
		return nil, errNotOOXML
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	if len(zr.File) > maxZipEntries {
		return nil, fmt.Errorf("the archive has %d entries; Meru reads up to %d", len(zr.File), maxZipEntries)
	}
	return &ooxml{zr: zr, partCap: maxPartBytes, fileCap: maxUnpackedBytes}, nil
}

// has reports whether the archive holds an entry named name.
func (o *ooxml) has(name string) bool {
	for _, f := range o.zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

// part returns the bytes of the entry named name, such as
// "xl/worksheets/sheet1.xml". It looks the name up in the archive's list
// and never uses it as a file path. It reads through io.LimitReader, one
// byte past the cap, so a part that unpacks past maxPartBytes, or a file
// past maxUnpackedBytes, fails instead of filling memory. The zip package
// checks each entry's checksum once it reaches the end, so a corrupt part
// fails too. It fails with errMissingPart wrapped when name isn't there.
func (o *ooxml) part(name string) ([]byte, error) {
	for _, f := range o.zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		// defer runs rc.Close() when part returns. Closing a reader of
		// memory can't fail in a way that matters here.
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, o.partCap+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if int64(len(data)) > o.partCap {
			return nil, fmt.Errorf("%s unpacks to more than %s; Meru reads up to %s", name, mib(o.partCap), mib(o.partCap))
		}
		o.read += int64(len(data))
		if o.read > o.fileCap {
			return nil, fmt.Errorf("the file unpacks to more than %s; Meru reads up to %s", mib(o.fileCap), mib(o.fileCap))
		}
		return data, nil
	}
	return nil, fmt.Errorf("%s: %w", name, errMissingPart)
}

// mib writes n bytes as whole MiB, such as "64 MiB", or as bytes below
// one MiB.
func mib(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%d MiB", n>>20)
}

// errMissingPart marks a part the archive doesn't hold.
var errMissingPart = errors.New("part not in the file")
