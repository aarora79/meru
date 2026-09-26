// This file holds the Bridge: the methods the page calls to ask a
// question, stop it, answer an approval and manage the queue, and the
// goroutine that reads each turn's events from merud and hands them to the
// page as Updates.

package desktop

import (
	"context"
	"errors"
	"fmt"
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
	// Model is the main model's name from config.toml, for the status
	// block, and Home the user's home folder, for opening a source shown
	// as ~/...
	Model string
	Home  string
	// Emit sends each Update to the page.
	Emit EmitFunc
	// Open opens a URL in the browser or the file's app. nil means
	// opener.Open; tests pass a fake that opens nothing.
	Open func(url string) error
}

// Bridge is what the page calls. Build one with New. The window binds
// every exported method, so the page can call each one by name.
//
// One turn runs at a time, as in `meru chat`. A question sent while one
// runs waits in the queue, and the Bridge sends it, in the same session,
// when the running turn ends.
type Bridge struct {
	socket string
	model  string
	home   string
	emit   EmitFunc
	open   func(url string) error

	// wg counts the turn goroutines, so ServiceShutdown can wait for them.
	wg sync.WaitGroup

	mu      sync.Mutex // guards the fields below
	turn    *turn      // the running turn, or nil
	last    int        // the number of the latest turn
	session string     // the session the running and queued questions go to
	queue   []string   // questions waiting behind the running turn, oldest first
}

// turn is one question on its way through merud.
type turn struct {
	n      int
	cancel context.CancelFunc
	// pending holds the channel each open approval waits on, by merud's
	// approval ID.
	pending map[string]chan rpc.Choice
	// steps are the turn's tool calls so far, for the privacy line.
	steps []Step
}

// New returns a Bridge for the merud at o.Socket.
func New(o Options) *Bridge {
	open := o.Open
	if open == nil {
		open = opener.Open
	}
	return &Bridge{socket: o.Socket, model: o.Model, home: o.Home, emit: o.Emit, open: open}
}

// Send asks question in session: "" starts a new session, and an ID
// continues that one. While a turn runs, the question waits in the queue
// instead and goes to the running turn's session, so session is only read
// when the Bridge is idle. Send returns at once; the answer arrives as
// Updates.
//
// Send takes no context, unlike the methods that only ask merud for data:
// the turn must outlive the call that starts it. Stop ends it.
//
// It fails when question is blank or the queue is full.
func (b *Bridge) Send(session, question string) error {
	q := strings.TrimSpace(question)
	if q == "" {
		return errors.New("type a question first")
	}
	b.mu.Lock()
	// defer runs b.mu.Unlock() when Send returns, on every path.
	defer b.mu.Unlock()
	if b.turn != nil {
		if len(b.queue) >= maxQueue {
			return fmt.Errorf("%d questions already wait; send this one when the next starts", maxQueue)
		}
		b.queue = append(b.queue, q)
		b.emitQueue("")
		return nil
	}
	b.session = session
	b.start(q)
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
		b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindEnd, Stopped: true, Contacted: contacted(t.steps)})
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

// Approve answers the approval id of turn n with choice: "once",
// "session" or "deny". It fails when the turn has ended, the approval
// isn't open, or choice isn't one of the three. merud checks the choice
// again against the ones it offered, and treats any other as a deny.
func (b *Bridge) Approve(n int, id, choice string) error {
	c := rpc.Choice(choice)
	if c != rpc.ChoiceOnce && c != rpc.ChoiceSession && c != rpc.ChoiceDeny {
		return fmt.Errorf("%q isn't an answer to an approval", choice)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.turn
	if t == nil || t.n != n {
		return errors.New("that question has already ended")
	}
	ch, ok := t.pending[id]
	if !ok {
		return errors.New("that approval is no longer open")
	}
	delete(t.pending, id)
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

// start sends q to merud as a new turn in b.session. The caller holds b.mu.
func (b *Bridge) start(q string) {
	b.last++
	// Each turn gets its own context, so Stop can cancel it without
	// touching anything else. The Bridge keeps the cancel function.
	ctx, cancel := context.WithCancel(context.Background())
	t := &turn{n: b.last, cancel: cancel, pending: map[string]chan rpc.Choice{}}
	b.turn = t
	req := rpc.Request{Op: rpc.OpAsk, Session: b.session, Text: q, Source: rpc.SourceDesktop}
	b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindStart, Question: q, Session: b.session})
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
	for ev, err := range rpc.Do(ctx, b.socket, req, b.approver(t)) {
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
	b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindEnd, Error: failure, Contacted: contacted(t.steps)})
	if len(b.queue) == 0 {
		return
	}
	next := b.queue[0]
	b.queue = b.queue[1:]
	b.emitQueue("")
	b.start(next)
}

// approver returns the ApproveFunc for t. rpc.Do calls it from t's
// goroutine each time merud asks about a tool call. It shows the page an
// approval card, then waits until Approve answers or the turn ends. While
// it waits, the turn waits too: merud holds the tool call until the reply
// arrives.
func (b *Bridge) approver(t *turn) rpc.ApproveFunc {
	return func(ctx context.Context, a rpc.Approval) (rpc.Choice, error) {
		// A buffer of one lets Approve deliver without waiting, even if
		// this function has already given up.
		ch := make(chan rpc.Choice, 1)
		b.mu.Lock()
		if b.turn != t {
			b.mu.Unlock()
			return rpc.ChoiceDeny, nil // nobody can see the card
		}
		t.pending[a.ID] = ch
		view := approvalView(a)
		b.emit(UpdateEvent, Update{Turn: t.n, Kind: KindApproval, Approval: &view})
		b.mu.Unlock()
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

// emitQueue sends the page the queue as it stands, with notice. The
// caller holds b.mu.
func (b *Bridge) emitQueue(notice string) {
	b.emit(UpdateEvent, Update{Kind: KindQueue, Queue: slices.Clone(b.queue), Notice: notice})
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
