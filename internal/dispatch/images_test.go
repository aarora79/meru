// This file tests a result that carries pictures, as view_image's does:
// Dispatch hands the pictures back for the model and writes none of them
// to the transcript or the tool_calls row.

package dispatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatchImages(t *testing.T) {
	picture := bytes.Repeat([]byte("PNGDATA"), 2000) // long enough that base64 of it would be easy to spot
	encoded := base64.StdEncoding.EncodeToString(picture)
	for _, incognito := range []bool{false, true} {
		b := &fakeBackend{kind: KindBuiltin, tools: []string{"view_image"},
			call: func(context.Context) (Result, error) {
				return Result{Text: "[1] ~/r/1.png (14 KB) is attached to this result.", Images: [][]byte{picture}}, nil
			}}
		rec := &fakeRecorder{}
		s := &sink{}
		d := New([]Backend{b}, rec, Options{})
		c := newCall("view_image", s, nil)
		c.Incognito = incognito

		res, out := d.Dispatch(context.Background(), c)
		if out.Outcome != OutcomeOK {
			t.Fatalf("incognito %v: outcome %q", incognito, out.Outcome)
		}
		// The model gets the picture.
		if len(res.Images) != 1 || !bytes.Equal(res.Images[0], picture) {
			t.Errorf("incognito %v: result carries %d pictures; want the backend's one", incognito, len(res.Images))
		}
		// Nothing written holds it, in raw or base64 form.
		var written strings.Builder
		for _, l := range s.lines {
			j, _ := json.Marshal(l)
			written.Write(j)
		}
		for _, r := range rec.rows {
			j, _ := json.Marshal(r)
			written.Write(j)
		}
		if w := written.String(); strings.Contains(w, encoded[:200]) || strings.Contains(w, "PNGDATAPNGDATA") {
			t.Errorf("incognito %v: the transcript or the row holds the picture", incognito)
		}
	}
}
