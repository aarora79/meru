// This file holds the Bridge: the methods the page calls to ask a
// question, stop it, answer an approval and manage the queue, and the
// goroutine that reads each turn's events from merud and hands them to the
// page as Updates. settings.go holds the methods behind the Library and
// Setup screens, and files.go the ones that save, pick and open files.

package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/aarora79/meru/internal/opener"
	"github.com/aarora79/meru/internal/rpc"
)

// maxQueue caps how many questions wait behind the running turn, as in
// `meru chat`. Each one costs a whole turn, and a longer line of them is
// more often a slip than a plan.
const maxQueue = 5

// EmitFunc sends one named event, with data, to the page. In the app it is
// Wails' app.Event.Emit; tests pass a function that records the updates.
// It must not call back into the Bridge, because the Bridge calls it while
// it holds its lock, which keeps the updates in order.
type EmitFunc func(name string, data any)

// Options configures a Bridge.
type Options struct {
	// Socket is merud's Unix socket, usually ~/.meru/merud.sock.
	Socket string
	// Model, Fast and Embed name the models config.toml sets for each
	// tier, for the status block and for Setup, which lists them even
	// while merud is down. Home is the user's home folder, for opening a
	// source shown as ~/...
	Model string
	Fast  string
	Embed string
	Home  string
	// Dir is Meru's home folder, ~/.meru, for the About section. ""
	// means the folder that holds Socket.
	Dir string
	// Emit sends each Update to the page.
	Emit EmitFunc
	// Open opens a URL in the browser or the file's app. nil means
	// opener.Open; tests pass a fake that opens nothing.
	Open func(url string) error
	// OutputDir is [skills] output_dir with "~" expanded, where merud
	// saves chats and notes, for Reveal. "" when config doesn't load.
	OutputDir string
	// PickFiles shows the system's file dialog and returns the files
	// picked, none when the user cancels. PickFolder shows its folder
	// dialog and returns the folder, or "" on a cancel. Quit closes the
	// app. The window supplies all three; nil leaves the feature off, as
	// in tests that don't need it.
	PickFiles  func() ([]string, error)
	PickFolder func() (string, error)
	Quit       func()
}

// Bridge is what the page calls. Build one with New. The window binds
// every exported method, so the page can call each one by name.
//
// One turn runs at a time, as in `meru chat`. A question sent while one
// runs waits in the queue, and the Bridge sends it, in the same session,
// when the running turn ends.
type Bridge struct {
	socket     string
	model      string
	fast       string
	embed      string
	home       string
	dir        string
	outputDir  string
	emit       EmitFunc
	open       func(url string) error
	pickFiles  func() ([]string, error)
	pickFolder func() (string, error)
	quit       func()

	// wg counts the turn goroutines and the ones that copy dropped files,
	// so ServiceShutdown can wait for them.
	wg sync.WaitGroup

	mu      sync.Mutex // guards the fields below
	turn    *turn      // the running turn, or nil
	last    int        // the number of the latest turn
	session string     // the session the running and queued questions go to
	scope   string     // where the running and queued questions may look
	queue   []outgoing // questions waiting behind the running turn, oldest first
	// attached holds the files the next question carries, at most
	// maxAttachments; files.go adds and removes them.
	attached []Attachment
	// approvals holds the channel each open approval card waits on, by the
	// card's ID, which the Bridge makes unique across turns and saves:
	// merud numbers approvals per connection, so two connections can each
	// have an approval "1".
	approvals map[string]chan rpc.Choice
	saves     int // numbers the saves, for their approval IDs
}

// outgoing is one question the Bridge sends or queues: its text, with a
// "Read this file" line per attached file, and the images it carries.
type outgoing struct {
	text   string
	images []Attachment
}

// turn is one question on its way through merud.
type turn struct {
	n      int
	cancel context.CancelFunc
	// steps are the turn's tool calls so far, for the privacy line.
	steps []Step
	// answer collects the answer's text and sources holds the latest
	// "sources" event's list, so the turn's end can say which sources
	// the answer cites.
	answer  strings.Builder
	sources []rpc.Citation
}

// cited returns the sources t's answer cites so far.
func (t *turn) cited() []rpc.Citation {
	return rpc.Cited(t.answer.String(), t.sources)
}

// New returns a Bridge for the merud at o.Socket.
func New(o Options) *Bridge {
	open := o.Open
	if open == nil {
		open = opener.Open
	}
	return &Bridge{
		socket: o.Socket, model: o.Model, fast: o.Fast, embed: o.Embed, home: o.Home, dir: o.Dir, outputDir: o.OutputDir, emit: o.Emit, open: open,
		pickFiles: o.PickFiles, pickFolder: o.PickFolder, quit: o.Quit,
		approvals: map[string]chan rpc.Choice{},
	}
}

// Send asks question in session: "" starts a new session, and an ID
// continues that one. The files attached so far go with it, one "Read
// this file" line each, and so do the images, in the request's Images;
// all of them come off the composer. scope says where Meru may look, one of the
// rpc.Scope constants, from the composer's "Where Meru looks" switch; ""
// means auto. While a turn runs, the question waits in the queue instead
// and goes to the running turn's session with its scope, so session and
// scope are only read when the Bridge is idle. Send returns at once; the
// answer arrives as Updates.
//
// Send takes no context, unlike the methods that only ask merud for data:
// the turn must outlive the call that starts it. Stop ends it.
//
// It fails when question is blank, scope is unknown, or the queue is full.
func (b *Bridge) Send(session, question, scope string) error {
	return b.send(session, question, scope, nil)
}

// Retry is Send for Try again: it asks question once more, with the
// images the first asking carried. images are the paths the page got on
// that turn's images, "~/meru-output/uploads/garden-bed.png"; each must
// sit in the uploads folder, and merud checks them again. Without Retry,
// Try again on a question about an image would ask it blind. It fails as
// Send does, and when a path isn't in the uploads folder.
func (b *Bridge) Retry(session, question, scope string, images []string) error {
	var extra []Attachment
	for _, p := range images {
		full := expandTilde(p, b.home)
		if b.outputDir == "" || !inside(filepath.Join(b.outputDir, "uploads"), full) {
			return fmt.Errorf("%s isn't an image you attached", p)
		}
		extra = append(extra, Attachment{
			Path: rpc.ShortPath(b.home, full), Name: filepath.Base(full), Kind: rpc.AttachImage,
			Thumb: b.thumbnail(full), full: full,
		})
	}
	return b.send(session, question, scope, extra)
}

// send does the work of Send and Retry. extra holds images to send beside
// the ones attached in the composer.
func (b *Bridge) send(session, question, scope string, extra []Attachment) error {
	q := strings.TrimSpace(question)
	if q == "" {
		return errors.New("type a question first")
	}
	if scope != "" && !slices.Contains(rpc.Scopes(), scope) {
		return fmt.Errorf("%q isn't a place Meru can look", scope)
	}
	b.mu.Lock()
	// defer runs b.mu.Unlock() when Send returns, on every path.
	defer b.mu.Unlock()
	if b.turn != nil && len(b.queue) >= maxQueue {
		return fmt.Errorf("%d questions already wait; send this one when the next starts", maxQueue)
	}
	next := outgoing{text: b.withAttachments(q), images: append(extra, imagesOf(b.attached)...)}
	if len(b.attached) > 0 {
		b.attached = nil
		b.emitAttachments("")
	}
	if b.turn != nil {
		b.queue = append(b.queue, next)
		b.emitQueue("")
		return nil
	}
	b.session, b.scope = session, scope
	b.start(next)
	return nil
}

// Stop ends the running turn, if one runs, and drops the queued
// questions: a user who stops an answer wants the screen back, not the
// next answer starting on its own. Closing the connection tells merud to
// stop the turn.
func (b *Bridge) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.turn; t != nil {
		t.cancel()
		b.turn = nil
		b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindEnd, Stopped: true, Contacted: contacted(t.steps), Cited: t.cited()})
	}
	dropped := len(b.queue)
	b.queue = nil
	b.emitQueue(dropNotice(dropped))
}

// Unqueue removes the queued question at index i, counting from 0 for the
// oldest. It fails when no question waits there.
func (b *Bridge) Unqueue(i int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < 0 || i >= len(b.queue) {
		return fmt.Errorf("no queued question %d", i+1)
	}
	b.queue = slices.Delete(b.queue, i, i+1)
	b.emitQueue("")
	return nil
}

// Approve answers the approval card id, from a turn or a save, with
// choice: "once", "session" or "deny". It fails when the card isn't open
// any more, because its turn or save ended or it was answered, or when
// choice isn't one of the three. merud checks the choice again against the
// ones it offered, and treats any other as a deny.
func (b *Bridge) Approve(id, choice string) error {
	c := rpc.Choice(choice)
	if c != rpc.ChoiceOnce && c != rpc.ChoiceSession && c != rpc.ChoiceDeny {
		return fmt.Errorf("%q isn't an answer to an approval", choice)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.approvals[id]
	if !ok {
		return errors.New("that approval is no longer open")
	}
	delete(b.approvals, id)
	// The channel has room for one value and gets only this one, so the
	// send never waits.
	ch <- c
	return nil
}

// ServiceShutdown stops the running turn and waits for its goroutine to
// finish. Wails calls a method by this name when the app quits, and keeps
// it off the list of methods the page may call.
func (b *Bridge) ServiceShutdown() error {
	b.mu.Lock()
	if b.turn != nil {
		b.turn.cancel()
		b.turn = nil
	}
	b.queue = nil
	b.mu.Unlock()
	b.wg.Wait()
	return nil
}

// start sends q to merud as a new turn in b.session, with the full paths
// of its images. The page gets the images, previews included, to show in
// the question's bubble. The caller holds b.mu.
func (b *Bridge) start(q outgoing) {
	b.last++
	// Each turn gets its own context, so Stop can cancel it without
	// touching anything else. The Bridge keeps the cancel function.
	ctx, cancel := context.WithCancel(context.Background())
	t := &turn{n: b.last, cancel: cancel}
	b.turn = t
	req := rpc.Request{Op: rpc.OpAsk, Session: b.session, Text: q.text, Source: rpc.SourceDesktop, Scope: b.scope}
	if len(q.images) > 0 {
		req.Images = &rpc.Images{}
		for _, a := range q.images {
			req.Images.Paths = append(req.Images.Paths, a.full)
		}
	}
	b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindStart, Question: q.text, Session: b.session, Images: q.images})
	// wg.Go starts the function in a new goroutine and counts it, so
	// ServiceShutdown can wait for it.
	b.wg.Go(func() { b.run(ctx, t, req) })
}

// run reads the turn's events from merud and hands each to the page, then
// ends the turn. It runs in its own goroutine, one per turn. A turn Stop
// already ended sends nothing more.
func (b *Bridge) run(ctx context.Context, t *turn, req rpc.Request) {
	failure := ""
	// range over rpc.Do runs the loop body once per event; see
	// docs/coding-notes/go-basics/iterators.md.
	for ev, err := range rpc.Do(ctx, b.socket, req, b.approver(t.n, "", func() bool { return b.turn == t })) {
		if err != nil {
			failure = err.Error()
			break
		}
		if ev.Type == rpc.EventError {
			failure = ev.Error
		}
		b.event(t, ev)
	}
	b.finish(t, failure)
}

// event hands one of t's events to the page, with the friendly Step for a
// tool call, and keeps the session a new turn started, so the queued
// questions continue it.
func (b *Bridge) event(t *turn, ev rpc.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turn != t {
		return // stopped
	}
	u := Update{Turn: t.n, Kind: KindEvent, Event: &ev}
	switch ev.Type {
	case rpc.EventSession:
		b.session = ev.Session
		u.Session = ev.Session
	case rpc.EventToken:
		t.answer.WriteString(ev.Text)
	case rpc.EventSources:
		// Each "sources" event replaces the one before it.
		t.sources = ev.Sources
	case rpc.EventToolCall:
		if ev.Tool != nil {
			s := stepOf(ev.Tool.ID, ev.Tool.Kind, ev.Tool.Name, ev.Tool.Args)
			t.steps = append(t.steps, s)
			u.Step = &s
		}
	case rpc.EventToolResult:
		if ev.Tool == nil {
			break
		}
		for i := range t.steps {
			if t.steps[i].ID == ev.Tool.ID {
				t.steps[i].Outcome = ev.Tool.Outcome
				t.steps[i].DurationMillis = ev.Tool.DurationMillis
				s := t.steps[i]
				u.Step = &s
				break
			}
		}
	}
	b.emit(UpdateEvent, u)
}

// finish ends t, unless Stop already did, and starts the oldest queued
// question. The next question goes even after an error, so each queued
// question gets its own answer or its own error, as in `meru chat`.
func (b *Bridge) finish(t *turn, failure string) {
	t.cancel() // frees the context; the connection is already closed
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turn != t {
		return
	}
	b.turn = nil
	b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindEnd, Error: failure, Contacted: contacted(t.steps), Cited: t.cited()})
	if len(b.queue) == 0 {
		return
	}
	next := b.queue[0]
	b.queue = b.queue[1:]
	b.emitQueue("")
	b.start(next)
}

// approver returns the ApproveFunc for turn n, or, when task is "save",
// for a save. rpc.Do calls it from the turn's or the save's goroutine each
// time merud asks about a tool call. It shows the page an approval card,
// then waits until Approve answers or ctx ends. While it waits, merud
// holds the tool call. live, called with b.mu held, says whether the turn
// or save is still the page's; a card nobody can see is a deny.
func (b *Bridge) approver(n int, task string, live func() bool) rpc.ApproveFunc {
	return func(ctx context.Context, a rpc.Approval) (rpc.Choice, error) {
		// A buffer of one lets Approve deliver without waiting, even if
		// this function has already given up.
		ch := make(chan rpc.Choice, 1)
		b.mu.Lock()
		if !live() {
			b.mu.Unlock()
			return rpc.ChoiceDeny, nil
		}
		prefix := fmt.Sprintf("t%d-", n)
		if task != "" {
			prefix = fmt.Sprintf("%s%d-", task, n)
		}
		view := approvalView(a)
		view.ID, view.Task = prefix+a.ID, task
		b.approvals[view.ID] = ch
		b.emit(UpdateEvent, Update{Turn: n, Kind: KindApproval, Approval: &view})
		b.mu.Unlock()
		// However this ends, the card closes with it.
		defer func() {
			b.mu.Lock()
			delete(b.approvals, view.ID)
			b.mu.Unlock()
		}()
		// select waits for whichever comes first: the answer or the end
		// of the turn.
		select {
		case c := <-ch:
			return c, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// emitQueue sends the page the queue as it stands, the text of each
// question, with notice. The caller holds b.mu.
func (b *Bridge) emitQueue(notice string) {
	texts := make([]string, len(b.queue))
	for i, q := range b.queue {
		texts[i] = q.text
	}
	b.emit(UpdateEvent, Update{Kind: KindQueue, Queue: texts, Notice: notice})
}

// dropNotice says how many queued questions Stop dropped, or "" for none.
func dropNotice(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "Dropped 1 queued question."
	}
	return fmt.Sprintf("Dropped %d queued questions.", n)
}
