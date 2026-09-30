// This file holds the Supervisor: one small state machine per stdio
// connector, owned by merud. It installs the connector, checks it once,
// starts it when the first tool call needs it, stops it after its idle
// timeout, and restarts it after a crash with a growing wait, until five
// crashes in ten minutes stop it for good. The MCP pool asks it for a
// session through Spawn. See ARCHITECTURE.md, "The supervisor".
//
//	off, by_hand, needs_config ── config fixed, reload ──▶ installing ──ok──▶ ready
//	                                                          │fail           │first call
//	                                                          ▼               ▼
//	                                                        failed ◀── 5 ── starting ──▶ ok
//	                                                                crashes    ▲          │crash
//	                                                                           └─backoff◀─┘
//	ok ──idle_timeout──▶ ready

package connectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// startTimeout caps one start: the program starting, the MCP handshake,
// the tool listing and the health check. It matches the MCP pool's
// connectTimeout for a server added by hand.
const startTimeout = 30 * time.Second

// The crash rule: a crash waits firstBackoff before the restart, and each
// crash within crashWindow doubles the wait, up to maxBackoff. The
// crashLimit-th crash within crashWindow stops the connector for good.
// With a limit of five the waits run 1, 2, 4 and 8 seconds, so the cap
// only matters if the limit grows.
const (
	firstBackoff = time.Second
	maxBackoff   = time.Minute
	crashWindow  = 10 * time.Minute
	crashLimit   = 5
)

// Clock is the time source for the supervisor's waits: the idle timeout,
// the backoff and the crash window. merud uses the real clock; tests use
// a fake one they move by hand, so a test of a ten-minute window runs at
// once.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f after d, in its own goroutine, unless the Timer is
	// stopped first. time.AfterFunc is the real one.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a wait from Clock.AfterFunc that can be called off.
type Timer interface {
	Stop() bool
}

// realClock is the Clock merud uses: the standard library's.
type realClock struct{}

// Now returns the time now.
func (realClock) Now() time.Time { return time.Now() }

// AfterFunc waits in a timer the Go runtime runs, and then calls f.
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// phase is where a supervisor stands inside. Users see the coarser State
// each one folds into (see state). The constants count up from 0: iota
// is Go's counter inside a const block.
type phase int

const (
	phaseOff         phase = iota // turned off in config
	phaseByHand                   // an [[mcp.servers]] entry of the same name wins
	phaseNeedsConfig              // a field is missing or wrong
	phaseInstalling               // installing, then the first health check
	phaseReady                    // installed and checked, not running; tools come from the cache
	phaseStarting                 // the program is starting for a call, or after a backoff
	phaseOK                       // running
	phaseBackoff                  // crashed; waiting to start again
	phaseFailed                   // stopped for good until config changes or merud restarts
)

// String names the phase, for the log.
func (p phase) String() string {
	return [...]string{"off", "by_hand", "needs_config", "installing", "ready", "starting", "ok", "backoff", "failed"}[p]
}

// state folds the phase into the State a user sees.
func (p phase) state() string {
	switch p {
	case phaseOff:
		return StateOff
	case phaseByHand:
		return StateByHand
	case phaseNeedsConfig:
		return StateNeedsConfig
	case phaseInstalling, phaseStarting, phaseBackoff:
		return StateStarting
	case phaseReady, phaseOK:
		return StateOK
	}
	return StateFailed
}

// Supervisor runs one stdio connector. Create it with New, hand it config
// with Configure, and stop it with Close. Its methods are safe to call
// from many goroutines at once.
//
// It owns every goroutine it starts: one worker at a time for an install
// or a start, and one watcher per running program, which waits for the
// program to end. Close stops them all and waits for them. The timers it
// sets run a short function that takes the lock, checks that nothing has
// moved on, and returns.
type Supervisor struct {
	m     Manifest
	log   *slog.Logger
	clock Clock
	// client is the MCP client the supervisor connects with.
	client *mcp.Client
	// home is the user's home folder, for a folder field written "~/…".
	home string
	// state is the file that keeps the tool list: runtime/state/<id>.json.
	state string
	// idle is how long a running program may sit unused; 0 means for ever.
	idle time.Duration

	// The steps that touch the machine. New fills them from an Installer;
	// tests put in fakes. install installs the pinned version, installed
	// reports a finished install, launch says how to start it, and dial
	// starts it and returns the MCP transport to its stdin and stdout.
	install   func(ctx context.Context, progress func(string)) (Installed, error)
	installed func() (Installed, bool)
	launch    func(inst Installed, values map[string]string) (Cmd, error)
	dial      func(procCtx context.Context, c Cmd, stderr *tailLog) (mcp.Transport, error)

	// wg counts the goroutines the supervisor started; Close waits on it.
	wg sync.WaitGroup

	// mu guards every field below it.
	mu         sync.Mutex
	closed     bool
	configured bool
	set        settings // config, as the last Configure checked it
	byHand     bool
	phase      phase
	reason     string // why it failed, or what the last crash said
	// gen counts restarts of the machine: each stop, crash and new config
	// adds one. A worker, a timer or a call started under an older gen
	// finds it changed and leaves the state alone.
	gen        int
	workCancel context.CancelFunc // ends the running install or start
	inst       Installed
	tools      []*mcp.Tool // from the running program, or the cache while ready
	session    *mcp.ClientSession
	stopProc   context.CancelFunc // kills the running program
	stderr     *tailLog           // the running program's error output
	inUse      int                // calls running now
	idleTimer  Timer
	idleSeq    int // which idle timer is current
	retryTimer Timer
	retryAt    time.Time
	crashes    []time.Time // within the last crashWindow
	// changed is closed and replaced at each change of phase, so a call
	// waiting in Spawn wakes up and looks again. A closed channel is
	// ready to read at once, for every reader: Go's way to tell many
	// goroutines "something happened".
	changed chan struct{}
}

// New returns the supervisor for the stdio connector m, installing into
// in's runtime folder. It starts nothing and stands in the off state
// until Configure hands it config. It fails for a connector of another
// kind: the supervisor runs only stdio connectors so far.
func New(m Manifest, in *Installer, log *slog.Logger) (*Supervisor, error) {
	if m.Kind != KindStdio {
		return nil, fmt.Errorf("connector %s: the supervisor runs only stdio connectors, not %s", m.ID, m.Kind)
	}
	s := newSupervisor(m, log, realClock{}, in.Home, statePath(in.MeruDir, m.ID))
	s.install = func(ctx context.Context, progress func(string)) (Installed, error) {
		return in.Install(ctx, m, progress)
	}
	s.installed = func() (Installed, bool) { return in.Installed(m) }
	s.launch = func(inst Installed, values map[string]string) (Cmd, error) {
		return childCmd(in, m, inst, values)
	}
	s.dial = func(procCtx context.Context, c Cmd, stderr *tailLog) (mcp.Transport, error) {
		return stdioTransport(procCtx, c, stderr)
	}
	return s, nil
}

// newSupervisor sets up the parts of a Supervisor that don't touch the
// machine, for New and for tests.
func newSupervisor(m Manifest, log *slog.Logger, clock Clock, home, state string) *Supervisor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Validate has checked that the timeout parses.
	idle, _ := time.ParseDuration(m.IdleTimeout)
	return &Supervisor{
		m: m, log: log.With("connector", m.ID), clock: clock, home: home, state: state, idle: idle,
		client: mcp.NewClient(&mcp.Implementation{Name: "meru"}, &mcp.ClientOptions{
			Logger: log,
			// Meru offers a connector no roots, sampling or elicitation,
			// as it offers none to a server added by hand.
			Capabilities: &mcp.ClientCapabilities{},
		}),
		changed: make(chan struct{}),
	}
}

// childCmd returns the program, arguments and environment that start the
// installed connector m with the user's values. The environment is short:
// LaunchCommand's PATH and variables, the user's real HOME, where a
// connector keeps its own settings, and TMPDIR when merud has one.
func childCmd(in *Installer, m Manifest, inst Installed, values map[string]string) (Cmd, error) {
	program, args, env, err := in.LaunchCommand(m, inst, values)
	if err != nil {
		return Cmd{}, err
	}
	list := []string{"HOME=" + in.Home}
	if v := os.Getenv("TMPDIR"); v != "" {
		list = append(list, "TMPDIR="+v)
	}
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	slices.Sort(list)
	return Cmd{Path: program, Args: args, Env: list}, nil
}

// Manifest returns the connector's manifest.
func (s *Supervisor) Manifest() Manifest { return s.m }

// Configure hands the supervisor its part of config: table, the
// [connectors.<id>] table (nil when config has none), the secrets, and
// byHand, true when [[mcp.servers]] has an entry with the connector's ID,
// which then wins. merud calls it at startup and on each reload.
//
// When nothing changed, and the connector hasn't failed, a running
// program keeps running. Otherwise the supervisor stops what runs,
// forgets its crashes, and starts over from the state config asks for.
// So a reload is also how a user retries a failed connector.
func (s *Supervisor) Configure(table config.Connector, sec *secrets.Secrets, byHand bool) {
	st := checkSettings(s.m, table, sec, s.home)
	s.mu.Lock()
	if s.configured && s.byHand == byHand && st.same(s.set) && s.phase != phaseFailed {
		s.set = st // the shown values may differ, say "~/x" for the same folder
		s.mu.Unlock()
		return
	}
	s.set, s.byHand, s.configured = st, byHand, true
	release := s.resetLocked()
	s.mu.Unlock()
	release()
}

// resetLocked stops what runs and moves to the state config asks for:
// by_hand, off, needs_config, ready when the install and its tool list
// are on disk, or installing. The caller holds s.mu, and must call the
// returned function after unlocking it, to stop the old program.
func (s *Supervisor) resetLocked() func() {
	release := s.stopLocked()
	s.crashes = nil
	switch {
	case s.byHand:
		s.setPhaseLocked(phaseByHand, "")
	case !s.set.enabled:
		s.setPhaseLocked(phaseOff, "")
	case s.set.problem != "":
		s.setPhaseLocked(phaseNeedsConfig, "")
	default:
		if inst, ok := s.installed(); ok {
			if tools, ok := readToolCache(s.state, s.m.ID, inst.Version); ok {
				s.inst, s.tools = inst, tools
				s.setPhaseLocked(phaseReady, "")
				return release
			}
		}
		s.setPhaseLocked(phaseInstalling, "")
		values := s.set.values
		s.workLocked(func(ctx context.Context, gen int) { s.installAndCheck(ctx, gen, values) })
	}
	return release
}

// stopLocked ends the running worker and program and calls off the
// timers. It adds one to gen, so anything they were doing counts for
// nothing, and returns the function that stops the program, which the
// caller runs after unlocking s.mu: stopping takes up to a few seconds.
func (s *Supervisor) stopLocked() func() {
	s.gen++
	if s.workCancel != nil {
		s.workCancel()
		s.workCancel = nil
	}
	stopTimer(&s.idleTimer)
	stopTimer(&s.retryTimer)
	cs, stop := s.session, s.stopProc
	s.session, s.stopProc, s.inUse = nil, nil, 0
	return func() {
		if cs != nil {
			_ = cs.Close()
		}
		if stop != nil {
			stop()
		}
	}
}

// stopTimer stops *t, if set, and forgets it. t is a pointer to the
// field, so stopTimer can clear the field itself.
func stopTimer(t *Timer) {
	if *t != nil {
		(*t).Stop()
		*t = nil
	}
}

// workLocked runs f in a new goroutine as the supervisor's one worker,
// with a context that stopLocked ends and the gen it started under. It
// does nothing after Close. The caller holds s.mu.
func (s *Supervisor) workLocked(f func(ctx context.Context, gen int)) {
	if s.closed {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.workCancel = cancel
	gen := s.gen
	s.goLocked(func() {
		defer cancel()
		f(ctx, gen)
	})
}

// goLocked starts f in a goroutine that Close waits for. A goroutine is a
// function running at the same time as the rest of merud; `go` starts
// one. It does nothing after Close. The caller holds s.mu.
func (s *Supervisor) goLocked(f func()) {
	if s.closed {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

// setPhaseLocked moves to phase p, keeps reason, logs the change, and
// wakes every call waiting in Spawn. The caller holds s.mu.
func (s *Supervisor) setPhaseLocked(p phase, reason string) {
	if p != s.phase || reason != s.reason {
		s.log.Info("connector state", "phase", p.String(), "state", p.state(), "reason", reason)
	}
	s.phase, s.reason = p, reason
	close(s.changed)
	s.changed = make(chan struct{})
}

// installAndCheck is the worker for the installing phase: it installs the
// pinned version, unless it is there already, then starts the program
// once to run the health check, keeps the tool list, and stops it. The
// connector is then ready. A failed install or check sets failed with
// the reason.
func (s *Supervisor) installAndCheck(ctx context.Context, gen int, values map[string]string) {
	inst, err := s.install(ctx, func(line string) { s.log.Debug("connector install", "line", line) })
	if err != nil {
		s.failIfCurrent(gen, "couldn't install: "+s.shortErr(err))
		return
	}
	cs, stop, _, tools, err := s.open(ctx, inst, values)
	if err != nil {
		s.failIfCurrent(gen, "failed its check: "+s.shortErr(err))
		return
	}
	_ = cs.Close()
	stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || s.closed {
		return
	}
	s.workCancel = nil
	s.inst, s.tools = inst, tools
	s.saveToolsLocked()
	s.setPhaseLocked(phaseReady, "")
}

// failIfCurrent sets failed with reason, unless the machine moved on
// since the worker started under gen.
func (s *Supervisor) failIfCurrent(gen int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || s.closed {
		return
	}
	s.workCancel = nil
	s.setPhaseLocked(phaseFailed, reason)
}

// shortErr turns err into one line for a sentence: its first line,
// without the "connector <id>: " the installer puts in front.
func (s *Supervisor) shortErr(err error) string {
	msg := firstLine(err.Error())
	msg = strings.TrimPrefix(msg, "connector "+s.m.ID+": ")
	return strings.TrimSuffix(msg, ".")
}

// open starts the installed program, runs the MCP handshake, lists its
// tools and runs the health check, all within startTimeout or until ctx
// ends. It returns the live session, the function that kills the
// program, its error output and its tools. On failure nothing is left
// running, and the error ends with the program's last line of error
// output, which most often says why.
func (s *Supervisor) open(ctx context.Context, inst Installed, values map[string]string) (*mcp.ClientSession, context.CancelFunc, *tailLog, []*mcp.Tool, error) {
	c, err := s.launch(inst, values)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// The program must outlive ctx, which ends with this start, so its
	// context starts from Background; the returned stop ends it.
	procCtx, stop := context.WithCancel(context.Background())
	tail := &tailLog{log: s.log}
	t, err := s.dial(procCtx, c, tail)
	if err != nil {
		stop()
		return nil, nil, nil, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	cs, err := s.client.Connect(cctx, t, nil)
	if err != nil {
		stop()
		return nil, nil, nil, nil, tail.wrap(fmt.Errorf("connect: %w", err))
	}
	// fail closes the session and stops the program before returning err.
	fail := func(err error) (*mcp.ClientSession, context.CancelFunc, *tailLog, []*mcp.Tool, error) {
		_ = cs.Close()
		stop()
		return nil, nil, nil, nil, tail.wrap(err)
	}
	var tools []*mcp.Tool
	// cs.Tools is an iterator: the loop fetches each page as it needs it.
	for tool, err := range cs.Tools(cctx, nil) {
		if err != nil {
			return fail(fmt.Errorf("list tools: %w", err))
		}
		tools = append(tools, tool)
	}
	if err := runHealthCheck(cctx, cs, s.m.Health); err != nil {
		return fail(err)
	}
	return cs, stop, tail, tools, nil
}

// saveToolsLocked writes the tool list to the state file, so the next
// merud offers the tools before the program runs. A failed write costs
// only that, so it is logged. The caller holds s.mu.
func (s *Supervisor) saveToolsLocked() {
	if err := writeToolCache(s.state, s.m.ID, s.inst.Version, s.tools); err != nil {
		s.log.Warn("connector tool list not saved", "err", err)
	}
}

// Spawn returns a live session to the connector for one tool call,
// starting the program when it is ready but not running, and waiting at
// most limit for it. The caller must call done when the call ends, so
// the idle timeout counts from the last call. Spawn is the MCP pool's
// hook: the pool never starts, restarts or stops a connector itself.
//
// It fails at once with the status sentence when the connector is off,
// set up by hand, needs config or has failed, and when a start it waited
// for failed. The call that fails is never sent again: the supervisor
// restarts the program, and the model can ask again.
func (s *Supervisor) Spawn(ctx context.Context, limit time.Duration) (cs *mcp.ClientSession, done func(), err error) {
	wait, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	sawStart := false
	for {
		s.mu.Lock()
		switch s.phase {
		case phaseOK:
			s.inUse++
			stopTimer(&s.idleTimer)
			cs, gen := s.session, s.gen
			s.mu.Unlock()
			return cs, s.doneFunc(gen), nil
		case phaseReady:
			s.setPhaseLocked(phaseStarting, "")
			inst, values := s.inst, s.set.values
			s.workLocked(func(ctx context.Context, gen int) { s.start(ctx, gen, inst, values) })
			sawStart = true
		case phaseStarting:
			sawStart = true
		case phaseInstalling:
			// Wait: the install may finish within limit.
		case phaseBackoff:
			if sawStart {
				// The start this call waited for failed.
				err := errors.New(s.sentenceLocked())
				s.mu.Unlock()
				return nil, nil, err
			}
		default:
			err := errors.New(s.sentenceLocked())
			s.mu.Unlock()
			return nil, nil, err
		}
		changed, sentence := s.changed, s.sentenceLocked()
		s.mu.Unlock()

		// select waits for whichever case is ready first: a change of
		// phase, or the end of the wait.
		select {
		case <-changed:
		case <-wait.Done():
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			return nil, nil, fmt.Errorf("%s didn't start within %s: %s", s.m.Name, limit, sentence)
		}
	}
}

// doneFunc returns the function that ends one call begun under gen. When
// the last call ends, the idle timer starts. A call that outlived its
// program, which crashed or was stopped, changes nothing. sync.Once
// makes a second call of done do nothing.
func (s *Supervisor) doneFunc(gen int) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if gen != s.gen {
				return
			}
			s.inUse--
			if s.inUse == 0 && s.phase == phaseOK {
				s.armIdleLocked()
			}
		})
	}
}

// start is the worker for the starting phase: it starts the program and,
// on success, keeps the session and moves to ok, with a watcher on the
// program. A failed start counts as a crash.
func (s *Supervisor) start(ctx context.Context, gen int, inst Installed, values map[string]string) {
	cs, stop, tail, tools, err := s.open(ctx, inst, values)

	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || s.closed {
		if err == nil {
			// Nothing wants this program any more. Stop it without
			// holding the lock, and have Close wait for that.
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				_ = cs.Close()
				stop()
			}()
		}
		return
	}
	s.workCancel = nil
	if err != nil {
		s.crashLocked("couldn't start: " + s.shortErr(err))
		return
	}
	s.session, s.stopProc, s.stderr = cs, stop, tail
	if !sameTools(s.tools, tools) {
		s.tools = tools
		s.saveToolsLocked()
	}
	s.setPhaseLocked(phaseOK, "")
	if s.inUse == 0 {
		s.armIdleLocked()
	}
	s.goLocked(func() { s.watch(cs) })
}

// sameTools reports whether two tool lists name the same tools in the
// same order.
func sameTools(a, b []*mcp.Tool) bool {
	return slices.EqualFunc(a, b, func(x, y *mcp.Tool) bool { return x.Name == y.Name })
}

// watch waits for the running program's session to end. If it is still
// the current session, the program crashed: watch records the crash,
// which leads to a backoff and a restart, or to failed. A session that
// was stopped on purpose (idle, reload, Close) is no longer current, so
// watch leaves the state alone. It never sends a call again.
func (s *Supervisor) watch(cs *mcp.ClientSession) {
	err := cs.Wait()

	s.mu.Lock()
	if s.session != cs {
		s.mu.Unlock()
		return
	}
	stop, tail := s.stopProc, s.stderr
	s.session, s.stopProc, s.inUse = nil, nil, 0
	// The calls that were running have failed with the session; their
	// done must not count against the next program.
	s.gen++
	stopTimer(&s.idleTimer)
	detail := tail.last()
	if detail == "" {
		detail = "it exited"
		if err != nil {
			detail = err.Error()
		}
	}
	s.log.Warn("connector stopped on its own", "err", err, "stderr", detail)
	s.crashLocked(detail)
	s.mu.Unlock()

	// Close reaps the program so no zombie process stays behind.
	_ = cs.Close()
	stop()
}

// crashLocked records a crash or a failed start, with detail saying why,
// and either waits out a backoff before the next start or, at the
// crashLimit-th crash within crashWindow, sets failed. The caller holds
// s.mu.
func (s *Supervisor) crashLocked(detail string) {
	now := s.clock.Now()
	// Keep only the crashes inside the window, then add this one.
	// slices.DeleteFunc drops the entries its function says yes to.
	s.crashes = slices.DeleteFunc(s.crashes, func(t time.Time) bool { return now.Sub(t) >= crashWindow })
	s.crashes = append(s.crashes, now)
	detail = strings.TrimSuffix(detail, ".")
	if len(s.crashes) >= crashLimit {
		s.setPhaseLocked(phaseFailed, "keeps stopping: "+detail)
		return
	}
	// 1 << n is 2 to the power n, so the wait doubles with each crash.
	delay := min(firstBackoff<<(len(s.crashes)-1), maxBackoff)
	s.retryAt = now.Add(delay)
	s.setPhaseLocked(phaseBackoff, detail)
	gen := s.gen
	s.retryTimer = s.clock.AfterFunc(delay, func() { s.retry(gen) })
}

// retry ends a backoff: it starts the program again, unless the machine
// moved on since the crash under gen.
func (s *Supervisor) retry(gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen || s.phase != phaseBackoff {
		return
	}
	s.retryTimer = nil
	s.setPhaseLocked(phaseStarting, "")
	inst, values := s.inst, s.set.values
	s.workLocked(func(ctx context.Context, gen int) { s.start(ctx, gen, inst, values) })
}

// armIdleLocked starts the idle timer, which stops the program if no call
// comes within the manifest's idle_timeout. The caller holds s.mu.
func (s *Supervisor) armIdleLocked() {
	if s.idle <= 0 {
		return
	}
	stopTimer(&s.idleTimer)
	s.idleSeq++
	gen, seq := s.gen, s.idleSeq
	s.idleTimer = s.clock.AfterFunc(s.idle, func() { s.idleStop(gen, seq) })
}

// idleStop stops a program that sat unused for the idle timeout and moves
// back to ready, unless a call came in or the machine moved on since the
// timer started.
func (s *Supervisor) idleStop(gen, seq int) {
	s.mu.Lock()
	if s.closed || gen != s.gen || seq != s.idleSeq || s.phase != phaseOK || s.inUse > 0 {
		s.mu.Unlock()
		return
	}
	cs, stop := s.session, s.stopProc
	s.session, s.stopProc, s.idleTimer = nil, nil, nil
	s.setPhaseLocked(phaseReady, "")
	s.mu.Unlock()
	s.log.Info("connector idle; stopped it", "idle_timeout", s.idle)
	_ = cs.Close()
	stop()
}

// Tools returns the tools the connector offers while it is ok: from the
// running program, or from the list kept at its last health check while
// it is ready. In every other state it returns nil, so the model sees
// none of its tools.
func (s *Supervisor) Tools() []*mcp.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != phaseOK && s.phase != phaseReady {
		return nil
	}
	return slices.Clone(s.tools)
}

// State returns the state a user sees and its sentence.
func (s *Supervisor) State() (state, sentence string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase.state(), s.sentenceLocked()
}

// Status reports the connector for the connectors op: its state and
// sentence, its fields with the values config holds, and the fields to
// fix.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		ID: s.m.ID, Name: s.m.Name, Kind: s.m.Kind, Required: s.m.Required,
		State: s.phase.state(), Sentence: s.sentenceLocked(),
	}
	for _, f := range s.m.Fields {
		st.Fields = append(st.Fields, FieldStatus{Field: f, Value: s.set.shown[f.ID], Saved: s.set.saved[f.ID]})
	}
	if s.phase == phaseNeedsConfig {
		st.Fix = slices.Clone(s.set.fix)
	}
	return st
}

// sentenceLocked says the state in one line, such as "Obsidian needs your
// vault folder." The caller holds s.mu.
func (s *Supervisor) sentenceLocked() string {
	name := s.m.Name
	switch s.phase {
	case phaseOff:
		return name + " is off."
	case phaseByHand:
		return fmt.Sprintf("%s is set up by hand, as the %s entry in [[mcp.servers]].", name, s.m.ID)
	case phaseNeedsConfig:
		return s.set.problem
	case phaseInstalling:
		return fmt.Sprintf("Meru is installing %s %s and checking it.", name, installVersion(s.m))
	case phaseReady:
		return name + " is ready. It starts when a question needs it."
	case phaseStarting:
		return name + " is starting."
	case phaseOK:
		return name + " is running."
	case phaseBackoff:
		// math.Ceil rounds up, so a wait of 0.4 s reads "1 s", never "0 s".
		secs := max(int(math.Ceil(s.retryAt.Sub(s.clock.Now()).Seconds())), 0)
		return fmt.Sprintf("%s stopped (%s) and starts again in %d s.", name, s.reason, secs)
	}
	return fmt.Sprintf("%s %s.", name, s.reason)
}

// Close stops the running program and every goroutine the supervisor
// started, and waits for them. Spawn fails after it.
func (s *Supervisor) Close() {
	s.mu.Lock()
	s.closed = true
	release := s.stopLocked()
	s.setPhaseLocked(phaseFailed, "stopped with merud")
	s.mu.Unlock()
	release()
	s.wg.Wait()
}

// tailLog is the io.Writer a running connector's stderr goes to. It logs
// each line at debug level and keeps the last one that isn't blank, for
// "keeps stopping: <line>". A line longer than tailLineCap is cut, and
// after tailTotalCap bytes the log gets no more, so a chatty program
// can't fill the disk; the last line still updates.
type tailLog struct {
	log *slog.Logger

	mu      sync.Mutex
	partial []byte
	line    string
	total   int
}

const (
	tailLineCap  = 300      // bytes kept of one line
	tailTotalCap = 64 << 10 // bytes logged per program: 64 KiB
)

// Write takes the next bytes of error output. It always reports all of p
// written: an error would make the program's pipe stop draining, and a
// full pipe blocks the program.
func (w *tailLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.partial = appendUpTo(w.partial, p, tailLineCap)
			break
		}
		line := strings.TrimSpace(string(appendUpTo(w.partial, p[:i], tailLineCap)))
		w.partial = nil
		p = p[i+1:]
		if line == "" {
			continue
		}
		w.line = line
		w.total += len(line)
		if w.total <= tailTotalCap {
			w.log.Debug("connector stderr", "line", line)
		}
	}
	return n, nil
}

// appendUpTo appends add to b, keeping b at most limit bytes long.
func appendUpTo(b, add []byte, limit int) []byte {
	room := limit - len(b)
	if room <= 0 {
		return b
	}
	if len(add) > room {
		add = add[:room]
	}
	return append(b, add...)
}

// last returns the last line of error output that wasn't blank, or the
// unfinished line when no full one came. It is safe on a nil tailLog.
func (w *tailLog) last() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := strings.TrimSpace(string(w.partial)); p != "" {
		return p
	}
	return w.line
}

// wrap adds the program's last line of error output to err, when it
// printed one.
func (w *tailLog) wrap(err error) error {
	if line := w.last(); line != "" {
		return fmt.Errorf("%w (%s)", err, line)
	}
	return err
}
