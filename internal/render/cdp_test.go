// This file tests the DevTools client against a fake Chrome: a goroutine
// on the far side of two in-memory pipes that answers by the script a
// test gives it. No browser runs.

package render

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeChrome answers each command it reads with answer(method, params),
// which returns the JSON to send back as the result, or an error message
// to send as Chrome's error. An answer of "" means no reply at all.
type fakeChrome struct {
	conn     *conn
	toFake   *io.PipeWriter // closing it ends the fake's read loop
	fromFake *io.PipeWriter // closing it is Chrome exiting
	methods  chan string
}

// newFakeChrome starts a fake that answers with answer, and a conn to it.
func newFakeChrome(t *testing.T, answer func(method string, params json.RawMessage) (result, errMsg string)) *fakeChrome {
	t.Helper()
	cmdR, cmdW := io.Pipe() // commands: conn writes, fake reads
	repR, repW := io.Pipe() // replies: fake writes, conn reads
	f := &fakeChrome{toFake: cmdW, fromFake: repW, methods: make(chan string, 64)}
	go func() {
		br := bufio.NewReader(cmdR)
		for {
			msg, err := br.ReadBytes(0)
			if err != nil {
				return
			}
			var in struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(msg[:len(msg)-1], &in)
			f.methods <- in.Method
			// An event first, which the client must skip.
			_, _ = repW.Write([]byte(`{"method":"Target.targetCreated","params":{}}` + "\x00"))
			res, errMsg := answer(in.Method, in.Params)
			switch {
			case errMsg != "":
				b, _ := json.Marshal(map[string]any{"id": in.ID, "error": map[string]any{"code": -32000, "message": errMsg}})
				_, _ = repW.Write(append(b, 0))
			case res != "":
				_, _ = repW.Write([]byte(`{"id":` + itoa(in.ID) + `,"result":` + res + "}\x00"))
			}
		}
	}()
	f.conn = newConn(cmdW, repR)
	t.Cleanup(func() { _ = repW.Close(); _ = cmdW.Close() })
	return f
}

// itoa writes n in decimal.
func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestCDPCall(t *testing.T) {
	f := newFakeChrome(t, func(method string, _ json.RawMessage) (string, string) {
		switch method {
		case "Browser.getVersion":
			return `{"product":"HeadlessChrome/154"}`, ""
		case "Target.bad":
			return "", "no such target"
		}
		return "", "" // no reply: the call must give up on its ctx
	})
	ctx := context.Background()

	var v struct {
		Product string `json:"product"`
	}
	if err := f.conn.call(ctx, "", "Browser.getVersion", nil, &v); err != nil || v.Product != "HeadlessChrome/154" {
		t.Fatalf("getVersion: %+v, %v", v, err)
	}
	if err := f.conn.call(ctx, "", "Target.bad", nil, nil); err == nil || !strings.Contains(err.Error(), "no such target") {
		t.Fatalf("an error reply: %v", err)
	}

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := f.conn.call(short, "", "Page.silent", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call with no reply: %v, want DeadlineExceeded", err)
	}
	f.conn.mu.Lock()
	left := len(f.conn.pending)
	f.conn.mu.Unlock()
	if left != 0 {
		t.Errorf("%d calls left pending after giving up", left)
	}
}

func TestCDPPipeClosed(t *testing.T) {
	f := newFakeChrome(t, func(string, json.RawMessage) (string, string) { return "", "" })
	done := make(chan error, 1)
	go func() { done <- f.conn.call(context.Background(), "", "Page.waiting", nil, nil) }()
	<-f.methods // the call is out
	_ = f.fromFake.Close()
	select {
	case err := <-done:
		if !errors.Is(err, errPipeClosed) {
			t.Fatalf("waiting call: %v, want errPipeClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting call never failed")
	}
	<-f.conn.done // the reader ended
	if err := f.conn.call(context.Background(), "", "Page.after", nil, nil); !errors.Is(err, errPipeClosed) {
		t.Errorf("a call after the close: %v", err)
	}
}

func TestReadMessageCap(t *testing.T) {
	big := strings.Repeat("x", maxMessage+10) + "\x00"
	if _, err := readMessage(bufio.NewReaderSize(strings.NewReader(big), 64<<10)); err == nil {
		t.Fatal("readMessage took a message over the cap")
	}
	msg, err := readMessage(bufio.NewReaderSize(strings.NewReader("one\x00two\x00"), 16))
	if err != nil || string(msg) != "one" {
		t.Fatalf("readMessage = %q, %v", msg, err)
	}
}
