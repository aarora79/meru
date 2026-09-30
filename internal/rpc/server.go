// This file is the server half of the protocol: the code merud uses to listen
// on the Unix socket, read each client's Request, hand it to a Handler, and
// write the Handler's Events back as newline-delimited JSON.
//
// Each question gets an rpc.request span, the root of its trace, and at
// debug level a log line when it arrives and one when it ends.

package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/obs"
)

// Handler answers one request whose op isn't OpPing: OpAsk, OpIndex,
// OpIndexStatus, OpTools, OpLog, OpUsage, OpMemoryList, OpMemoryAdd,
// OpMemoryForget, OpSkills, OpSkillShow, OpSkillReset, OpMCPProbe,
// OpMCPReload, OpMCPStatus, OpConnectors, OpSessions, OpSessionTurns, the ops that
// delete, move and tag a chat and manage the chat folders, or one of the
// desktop app's settings ops in settings.go. It calls emit
// once per event to send ("session", "route", "token" and so on) and
// returns when the reply is complete.
//
// The server writes the closing event itself: "done" when Handler returns
// nil, "error" with the error's text when it doesn't. A Handler that wants
// the "done" event to carry stats emits one: the server holds it back and
// sends it as the closing event if Handler returns nil, or drops it if
// Handler fails. So the client always sees exactly one closing event, and it
// comes last. ctx is cancelled when
// the client disconnects or merud shuts down; Handler should then stop and
// return ctx.Err().
//
// approve asks the client about one tool call: it sends an "approval" event
// and waits for the client's Reply. Handlers that run no tools ignore it.
//
// Handler is a function type, so any function with this signature, such as a
// method value like agent.Handle, can serve requests.
type Handler func(ctx context.Context, req Request, emit func(Event) error, approve ApproveFunc) error

// pingTimeout bounds how long Listen waits for an existing merud to answer.
const pingTimeout = time.Second

// Listen opens the Unix socket at path and returns a listener for Serve.
//
// A socket file left behind by a merud that crashed is removed first. If
// another merud still answers a ping on path, Listen refuses to start, so two
// daemons never fight over one socket. Listen also refuses when path exists
// and isn't a socket, rather than delete a file it didn't make.
//
// The socket gets mode 0600, so only the user can connect. The directory that
// holds it (~/.meru) is 0700 as well, which closes the short gap between
// creating the socket and changing its mode.
func Listen(ctx context.Context, path string) (net.Listener, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing there: the normal case.
	case err != nil:
		return nil, fmt.Errorf("check socket %s: %w", path, err)
	case info.Mode()&fs.ModeSocket == 0:
		return nil, fmt.Errorf("%s exists and isn't a socket; move it away and start merud again", path)
	default:
		if pingOK(ctx, path) {
			return nil, fmt.Errorf("another merud is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("set mode of socket %s: %w", path, err)
	}
	return ln, nil
}

// pingOK reports whether a merud answers a ping on the socket at path within
// pingTimeout.
func pingOK(ctx context.Context, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	for ev, err := range Do(ctx, path, Request{Op: OpPing}, nil) {
		return err == nil && ev.Type == EventDone
	}
	return false
}

// Serve accepts connections on ln until ctx is cancelled, and runs each one in
// its own goroutine. When ctx is cancelled, Serve closes ln, cancels every
// open request, waits for their goroutines to finish, and returns nil. It
// returns an error only if accepting fails for a reason other than shutdown.
//
// A goroutine is a function running at the same time as the rest of the
// program. Serve owns every goroutine it starts and waits for all of them
// before it returns, so none outlive it.
func Serve(ctx context.Context, ln net.Listener, h Handler, log *slog.Logger) error {
	// context.AfterFunc runs ln.Close in its own goroutine once ctx is
	// cancelled. Closing the listener makes the blocked Accept below return.
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()

	// A WaitGroup counts running goroutines. wg.Go starts one and counts it;
	// wg.Wait blocks until every one has returned.
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutting down
			}
			if errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("accept: listener closed")
			}
			// Other errors, such as running out of file descriptors, may pass.
			// Log, pause so we don't spin, and keep serving.
			log.Warn("accept failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		wg.Go(func() { serveConn(ctx, conn, h, log) })
	}
}

// serveConn reads one Request from conn, answers it and closes conn.
func serveConn(ctx context.Context, conn net.Conn, h Handler, log *slog.Logger) {
	// serverCtx is cancelled only when merud stops. The ctx below is also
	// cancelled when the client hangs up; comparing the two says which.
	serverCtx := ctx
	defer conn.Close()
	obs.ActiveStreams(ctx, 1)
	defer obs.ActiveStreams(ctx, -1)

	// This ctx is cancelled when serveConn returns, when the client hangs up
	// (see watchHangup), or when the server's ctx is cancelled.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Once ctx is cancelled, set a deadline in the past so any blocked read or
	// write on conn fails at once. Without it, a client that stops reading
	// could hold a write, and so this goroutine, forever.
	stopDeadline := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopDeadline()

	var mu sync.Mutex // guards enc: emit and the closing write never overlap
	enc := json.NewEncoder(conn)
	write := func(ev Event) error {
		mu.Lock()
		defer mu.Unlock()
		return enc.Encode(ev)
	}

	br := bufio.NewReaderSize(conn, 4096)
	req, err := readRequest(br)
	if err != nil {
		log.Debug("bad request", "err", err)
		// A failed write means the client already hung up, so there is no one left
		// to tell. The `_ =` marks each dropped error below as deliberate.
		_ = write(Event{Type: EventError, Error: err.Error()})
		return
	}

	switch req.Op {
	case OpPing:
		log.DebugContext(ctx, "rpc ping")
		_ = write(Event{Type: EventDone})
		return
	case OpAsk, OpIndex, OpIndexStatus, OpTools, OpLog, OpUsage, OpMemoryList, OpMemoryAdd, OpMemoryForget,
		OpSkills, OpSkillShow, OpSkillReset, OpMCPProbe, OpMCPReload, OpMCPStatus, OpConnectors, OpSessions, OpSessionTurns,
		OpConnections, OpToolPolicy, OpMCPAdd, OpMCPRemove, OpSecretSet, OpFolders, OpFolderAdd,
		OpFolderRemove, OpSaveFile, OpAttachFile, OpSkillEnable, OpSkillDisable, OpModels,
		OpModelSet, OpModelUse, OpModelSave, OpSessionDelete, OpSessionMove, OpSessionTag, OpChatFolders,
		OpChatFolderAdd, OpChatFolderRename, OpChatFolderRemove:
		// Handled below.
	default:
		_ = write(Event{Type: EventError, Error: fmt.Sprintf("unknown op %q", req.Op)})
		return
	}

	// The span below is the root of this question's trace: the agent's
	// meru.turn span and everything under it nest inside it, and the
	// logger's trace_id comes from it. The question's text never goes on
	// it, only its length in characters.
	start := time.Now()
	chars := utf8.RuneCountInString(req.Text)
	ctx, span := obs.Tracer().Start(ctx, "rpc.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("meru.rpc.op", string(req.Op)),
			attribute.String("meru.source", string(req.Source)),
			attribute.Int("meru.question.chars", chars),
		))
	defer span.End()
	log.DebugContext(ctx, "rpc request", "op", req.Op, "source", req.Source,
		"session", req.Session, "question_chars", chars)

	// After its Request the client sends only Replies to approval events.
	// readReplies hands each one to the approval waiting for it, and
	// cancels the turn when the client hangs up.
	replies := newReplyBox()
	var wg sync.WaitGroup
	wg.Go(func() { readReplies(br, replies, cancel, log) })
	// Deferred calls run last-in, first-out: this Wait runs after cancel
	// (registered below) has unblocked the read, and before conn.Close.
	defer wg.Wait()
	defer cancel()

	// done is the closing event for a successful turn. The Handler may
	// replace it with its own, which carries the turn's stats. Only this
	// goroutine touches it: the Handler calls emit from the goroutine that
	// called it.
	done := Event{Type: EventDone}
	emit := func(ev Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ev.Type == EventDone {
			done = ev // hold it back until the Handler returns
			return nil
		}
		if err := write(ev); err != nil {
			return fmt.Errorf("send event: %w", err)
		}
		return nil
	}

	approve := func(ctx context.Context, a Approval) (Choice, error) {
		return replies.ask(ctx, a, emit)
	}
	herr := h(ctx, req, emit, approve)
	ms := time.Since(start).Milliseconds()
	switch {
	case ctx.Err() != nil:
		// The client left or merud is stopping. Nobody is reading a final
		// event, so don't send one.
		reason := "client hung up"
		if serverCtx.Err() != nil {
			reason = "merud stopping"
		}
		span.AddEvent("cancelled", trace.WithAttributes(attribute.String("meru.cancel.reason", reason)))
		log.DebugContext(ctx, "rpc cancelled", "reason", reason, "ms", ms)
	case herr != nil:
		obs.EndSpanErr(ctx, span, herr)
		_ = write(Event{Type: EventError, Error: herr.Error()})
		log.DebugContext(ctx, "rpc error sent", "ms", ms, "err", herr)
	default:
		_ = write(done)
		log.DebugContext(ctx, "rpc done sent", "ms", ms)
	}
}

// readRequest reads and decodes the first line from r. It fails on a line
// longer than maxLine, bad JSON, or a connection that closes first.
func readRequest(r *bufio.Reader) (Request, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return Request{}, fmt.Errorf("read request: %w", err)
		}
		line = append(line, chunk...)
		if len(line) > maxLine {
			return Request{}, errors.New("read request: line too long")
		}
		if !isPrefix {
			break
		}
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Request{}, fmt.Errorf("bad request: %w", err)
	}
	return req, nil
}

// replyBox pairs each approval event with the Reply that answers it. The
// turn's goroutine registers a channel under the approval's ID and waits on
// it; readReplies, in another goroutine, delivers each Reply to its channel.
type replyBox struct {
	mu      sync.Mutex             // guards next and pending
	next    int                    // the last approval ID handed out
	pending map[string]chan Choice // open approvals, by ID
}

// newReplyBox returns an empty replyBox.
func newReplyBox() *replyBox {
	return &replyBox{pending: map[string]chan Choice{}}
}

// ask sends a as an "approval" event with a fresh ID, then waits for the
// client's answer. An answer the approval didn't offer counts as "deny",
// so a confused client can't widen what the user agreed to. ask fails when
// the event can't be sent or ctx ends first, which includes the client
// hanging up.
func (b *replyBox) ask(ctx context.Context, a Approval, emit func(Event) error) (Choice, error) {
	// A buffer of one lets readReplies deliver without waiting, even if
	// this function has already given up.
	ch := make(chan Choice, 1)
	b.mu.Lock()
	b.next++
	a.ID = strconv.Itoa(b.next)
	b.pending[a.ID] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, a.ID)
		b.mu.Unlock()
	}()

	if err := emit(Event{Type: EventApproval, Approval: &a}); err != nil {
		return "", err
	}
	// select waits for whichever happens first: the answer or the end of
	// the turn.
	select {
	case c := <-ch:
		if !slices.Contains(a.Choices, c) {
			return ChoiceDeny, nil
		}
		return c, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// deliver hands r to the approval waiting for it. A Reply for no open
// approval is dropped: the approval may have timed out already.
func (b *replyBox) deliver(r Reply) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.pending[r.ApprovalID]
	if !ok {
		return false
	}
	delete(b.pending, r.ApprovalID)
	ch <- r.Choice
	return true
}

// readReplies reads Reply lines from the client until the connection
// closes, then calls cancel, because a client that hangs up has given up
// on the turn. A line that isn't a Reply is logged and skipped.
func readReplies(br *bufio.Reader, box *replyBox, cancel context.CancelFunc, log *slog.Logger) {
	defer cancel()
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > maxLine {
			log.Debug("reply line too long; closing")
			return
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var r Reply
			if jerr := json.Unmarshal(line, &r); jerr != nil || r.ApprovalID == "" {
				log.Debug("bad reply line", "err", jerr)
			} else if !box.deliver(r) {
				log.Debug("reply for no open approval", "id", r.ApprovalID)
			}
		}
		if err != nil {
			return // the client hung up, or the connection broke
		}
	}
}
