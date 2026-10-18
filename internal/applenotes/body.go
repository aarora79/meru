// This file holds noteText, which turns the bytes the Notes app stores for
// a note into the note's plain text.

package applenotes

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protowire"
)

// maxBodyBytes caps how much a note's data may grow to once gunzipped. A
// note of plain text is a few kilobytes; the cap stops a damaged or hostile
// blob from filling memory.
const maxBodyBytes = 32 << 20 // 32 MiB; << shifts left, so this is 32 * 2^20

// The protobuf field numbers on the path to a note's text. Apple publishes
// no schema; these come from the open-source readers of the format, such
// as apple_cloud_notes_parser, and match every macOS release since 10.11:
//
//	NoteStoreProto { Document document = 2; }
//	Document       { Note note = 3; }
//	Note           { string note_text = 2; }
const (
	fieldDocument = 2
	fieldNote     = 3
	fieldText     = 2
)

// errNoText is returned when the data decodes but holds no text field.
var errNoText = errors.New("no note text in the data")

// noteText returns the plain text of a note from data, the ZDATA column of
// ZICNOTEDATA: gzip around a protobuf. It fails when data isn't gzip, when
// the protobuf is malformed, or when the text field is missing.
func noteText(data []byte) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("note data: %w", err)
	}
	// defer runs Close when noteText returns, on every path.
	defer zr.Close()
	// LimitReader stops reading one byte past the cap, so a longer body
	// shows up as too long instead of being cut silently.
	raw, err := io.ReadAll(io.LimitReader(zr, maxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("note data: %w", err)
	}
	if len(raw) > maxBodyBytes {
		return "", fmt.Errorf("note data is over %d bytes", maxBodyBytes)
	}
	doc, err := field(raw, fieldDocument)
	if err != nil {
		return "", err
	}
	note, err := field(doc, fieldNote)
	if err != nil {
		return "", err
	}
	text, err := field(note, fieldText)
	if err != nil {
		return "", err
	}
	return string(text), nil
}

// field returns the bytes of the first length-delimited field numbered num
// in msg, a protobuf message. A nested message and a string are both
// length-delimited, so the same walk finds either. Fields of other types
// are skipped. It fails when msg is malformed or has no such field.
func field(msg []byte, num protowire.Number) ([]byte, error) {
	for len(msg) > 0 {
		// ConsumeTag reads a field's number and wire type, and returns
		// how many bytes it took; a negative count means malformed data.
		n, typ, size := protowire.ConsumeTag(msg)
		if size < 0 {
			return nil, fmt.Errorf("note data: %w", protowire.ParseError(size))
		}
		msg = msg[size:]
		if n == num && typ == protowire.BytesType {
			v, size := protowire.ConsumeBytes(msg)
			if size < 0 {
				return nil, fmt.Errorf("note data: %w", protowire.ParseError(size))
			}
			return v, nil
		}
		// ConsumeFieldValue skips a value of any wire type.
		size = protowire.ConsumeFieldValue(n, typ, msg)
		if size < 0 {
			return nil, fmt.Errorf("note data: %w", protowire.ParseError(size))
		}
		msg = msg[size:]
	}
	return nil, errNoText
}
