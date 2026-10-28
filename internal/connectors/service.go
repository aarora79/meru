// This file holds the Service supervisor: the one for a connector of kind
// service, a program on loopback that merud sends plain HTTP requests,
// such as whisper.cpp's whisper-server for speech to text. It installs the
// program, and the files the user's choices name, as soon as config turns
// the connector on, so the first request doesn't wait for a download. It
// starts the program when a request needs it, behind a path made of a
// random token so that no web page in a browser can reach it, and stops it
// after the manifest's idle timeout. A program that exits on its own
// counts as a crash, and starts again at the next request; the fifth crash
// in ten minutes stops it until Fix or a change of config. See
// ARCHITECTURE.md, "Speech to text".
//
//	off ──enable──▶ installing ──▶ ready ──Use──▶ running ──idle──▶ ready
//	                    │ fails       ▲  ◀── exits on its own (a crash) ──┘
//	needs_config ◀── a field is wrong │  fifth crash in 10 min ──▶ failed
//	                 failed ◀─────────┘

package connectors

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// serviceStartWait caps one start of a service: the program loading its
// model and listening, then its health check. whisper-server loads a
// 574 MB model in about a second on Apple silicon; a slow disk gets room.
const serviceStartWait = time.Minute

// serviceCheckTimeout caps one health check request.
const serviceCheckTimeout = 5 * time.Second

// The internal states of a service. state folds them into the State
// constants a user sees.
type servicePhase int

// The phases, counted up from 0 by iota.
const (
	servicePhaseOff servicePhase = iota
	servicePhaseNeedsConfig
	servicePhaseInstalling
	servicePhaseReady
	servicePhaseRunning
	servicePhaseFailed
)

// state returns the State constant a user sees for p.
func (p servicePhase) state() string {
	switch p {
	case servicePhaseOff:
		return StateOff
	case servicePhaseNeedsConfig:
		return StateNeedsConfig
	case servicePhaseInstalling:
		return StateStarting
	case servicePhaseFailed:
		return StateFailed
	}
	return StateOK
}

// Service supervises one service connector. Create it with NewService,
// hand it config with Configure, ask for the program with Use, and stop
// it with Close. Its methods are safe to call from many goroutines at once.
//
// It owns at most two goroutines: the install that Configure starts, and
// the watch on a running program. Configure and Close stop both and wait
// for them.
type Service struct {
	m     Manifest
	in    *Installer
	log   *slog.Logger
	clock Clock
	// start starts the program and returns a channel that closes when it
	// exits; it is startListening, and tests replace it.
	start func(ctx, procCtx context.Context, c Cmd, stderr io.Writer, endpoint string) (<-chan struct{}, error)
	// client sends the health checks, and refuses a redirect off this
	// machine.
	client *http.Client
	// idle is how long the program may sit unused before Service stops
	// it, from the manifest's idle_timeout; 0 means never.
	idle time.Duration
	// startWait is serviceStartWait; tests may shorten it.
	startWait time.Duration

	// starting lets one start run at a time: a second Use waits for the
	// first one's program rather than starting another on the same port.
	starting sync.Mutex

	// mu guards every field below it.
	mu         sync.Mutex
	closed     bool
	configured bool
	phase      servicePhase
	sentence   string   // why the service is failed, installing or needs config
	set        settings // the last Configure's values and problem
	fields     []Field  // the manifest's fields, with the defaults Configure got
	table      config.Connector
	defaults   map[string]string
	crashes    []time.Time
	// The install that Configure starts: its cancel, and a channel that
	// closes when it is done.
	stopInstall context.CancelFunc
	installDone chan struct{}
	// The running program: the base URL requests go to, the cancel that
	// kills it, a channel that closes when it exits, the generation that
	// tells its exit from a later program's, and how many requests use it.
	base     string
	kill     context.CancelFunc
	exited   <-chan struct{}
	gen      int
	inUse    int
	stopping bool
	idleT    Timer
}

// NewService returns the supervisor for the service connector m, which
// installs and starts its program through in. It starts nothing, and
// stands off until Configure. It fails for a connector of another kind.
func NewService(m Manifest, in *Installer, log *slog.Logger) (*Service, error) {
	if m.Kind != KindService {
		return nil, fmt.Errorf("connector %s: a Service runs only service connectors, not %s", m.ID, m.Kind)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Validate has checked idle_timeout, so a parse error can't happen.
	idle, _ := time.ParseDuration(m.IdleTimeout)
	client := loopbackClient()
	client.Timeout = serviceCheckTimeout
	return &Service{
		m: m, in: in, log: log.With("connector", m.ID), clock: realClock{},
		start: startListening, client: client, idle: idle, startWait: serviceStartWait,
		phase: servicePhaseOff, fields: m.Fields,
	}, nil
}

// Manifest returns the connector's manifest.
func (s *Service) Manifest() Manifest { return s.m }

// Configure hands the service its part of config: table, the
// [connectors.<id>] table (nil when config has none), and defaults, a
// value by field ID that takes the place of the manifest's default, such
// as the speech model a profile picks. merud calls it at startup and on
// each reload.
//
// When nothing changed and the service hasn't failed, it keeps going.
// Otherwise it stops the program and the install, forgets the crashes,
// and, when the connector is on and its fields are fine, installs what is
// missing in the background.
func (s *Service) Configure(table config.Connector, defaults map[string]string) {
	s.configure(table, defaults, false)
}

// Recheck is Fix for a service: like Configure with the config it last
// got, but it starts over even when nothing changed. It does nothing
// before the first Configure.
func (s *Service) Recheck() {
	s.mu.Lock()
	configured, table, defaults := s.configured, s.table, s.defaults
	s.mu.Unlock()
	if configured {
		s.configure(table, defaults, true)
	}
}

// configure does the work of Configure and, with force, of Recheck.
func (s *Service) configure(table config.Connector, defaults map[string]string, force bool) {
	// The manifest with the caller's defaults in place of its own, so the
	// check, the launch and Status all see them.
	m := s.m
	m.Fields = slices.Clone(s.m.Fields)
	for i, f := range m.Fields {
		if v, ok := defaults[f.ID]; ok && v != "" {
			m.Fields[i].Default = v
		}
	}
	set := checkSettings(m, table, nil, s.in.Home)

	s.mu.Lock()
	if s.closed || (!force && s.configured && set.same(s.set) && s.phase != servicePhaseFailed) {
		s.table, s.defaults = table, defaults
		s.mu.Unlock()
		return
	}
	stopInstall, installDone := s.stopInstall, s.installDone
	s.stopInstall, s.installDone = nil, nil
	s.configured, s.set, s.fields, s.table, s.defaults = true, set, m.Fields, table, defaults
	s.crashes = nil
	waitExit := s.stopLocked()
	// A platform the manifest has no download for, such as Linux before
	// its build exists, stays off with a sentence that says why, rather
	// than failing an install that can't work. It says so even while the
	// connector is switched off, so Meru never asks the user to turn on
	// what can't run.
	_, built := s.m.Install.Binaries[s.in.Platform]
	switch {
	case !built:
		s.setLocked(servicePhaseOff, fmt.Sprintf("%s isn't available on this computer yet: Meru has no %s build of it.", s.m.Name, s.in.Platform))
	case !set.enabled:
		s.setLocked(servicePhaseOff, "")
	case set.problem != "":
		s.setLocked(servicePhaseNeedsConfig, set.problem)
	default:
		s.setLocked(servicePhaseInstalling, "")
	}
	s.mu.Unlock()

	// Stop the old install and the old program outside the lock: each may
	// take a moment, and the install's goroutine takes the lock to finish.
	if stopInstall != nil {
		stopInstall()
		<-installDone
	}
	waitExit()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.phase != servicePhaseInstalling {
		return
	}
	// The install's context starts from Background: it lives until the
	// next Configure or Close, not for one request.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.stopInstall, s.installDone = cancel, done
	// go runs the install in its own goroutine, at the same time as the
	// rest of merud.
	go s.install(ctx, done, m, set.values)
}

// install installs the program and the files values name, then moves to
// ready, or to failed with the reason. It closes done when it returns.
func (s *Service) install(ctx context.Context, done chan struct{}, m Manifest, values map[string]string) {
	defer close(done)
	progress := func(line string) { s.log.Debug("connector install", "line", line) }
	err := func() error {
		files, err := launchFiles(m, values)
		if err != nil {
			return err
		}
		inst, err := s.in.Install(ctx, m, progress)
		if err != nil {
			return err
		}
		return s.in.EnsureFiles(ctx, inst, files, progress)
	}()
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.log.Warn("connector install failed", "err", err)
		s.setLocked(servicePhaseFailed, fmt.Sprintf("%s couldn't install: %s.", s.m.Name, s.shortErr(err)))
		return
	}
	s.setLocked(servicePhaseReady, "")
}

// Use returns the base URL to send requests to, with the token path in
// it, and a done function the caller must call when its request ends. It
// starts the program when it isn't running, which may take a few
// seconds, and waits for a start another Use began.
//
// It fails with the status sentence when the connector is off, needs
// config, is still installing or has failed, and with the reason when
// the program won't start or fails its check; that counts as a crash.
func (s *Service) Use(ctx context.Context) (base string, done func(), err error) {
	if base, done, ok := s.take(); ok {
		return base, done, nil
	}
	s.starting.Lock()
	defer s.starting.Unlock()
	// A start that finished while this one waited for the lock serves
	// this request too.
	if base, done, ok := s.take(); ok {
		return base, done, nil
	}
	s.mu.Lock()
	phase, sentence, values, fields := s.phase, s.sentenceLocked(), s.set.values, s.fields
	s.mu.Unlock()
	if phase != servicePhaseReady {
		return "", nil, errors.New(sentence)
	}
	m := s.m
	m.Fields = fields
	if err := s.run(ctx, m, values); err != nil {
		return "", nil, err
	}
	if base, done, ok := s.take(); ok {
		return base, done, nil
	}
	// The program exited between its start and this line.
	s.mu.Lock()
	defer s.mu.Unlock()
	return "", nil, errors.New(s.sentenceLocked())
}

// take counts one more request on the running program and returns its
// base URL and the done function, or false when no program runs.
func (s *Service) take() (string, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != servicePhaseRunning {
		return "", nil, false
	}
	s.inUse++
	stopTimer(&s.idleT)
	gen := s.gen
	var once sync.Once
	return s.base, func() { once.Do(func() { s.release(gen) }) }, true
}

// release ends one request on the program of generation gen, and starts
// the idle wait when it was the last.
func (s *Service) release(gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || s.phase != servicePhaseRunning {
		return
	}
	s.inUse--
	if s.inUse == 0 && s.idle > 0 {
		s.idleT = s.clock.AfterFunc(s.idle, func() { s.idleStop(gen) })
	}
}

// idleStop stops the program of generation gen when nothing has used it
// for the idle time.
func (s *Service) idleStop(gen int) {
	s.mu.Lock()
	if gen != s.gen || s.phase != servicePhaseRunning || s.inUse > 0 {
		s.mu.Unlock()
		return
	}
	s.log.Info("connector idle; stopping it")
	wait := s.stopLocked()
	s.setLocked(servicePhaseReady, "")
	s.mu.Unlock()
	wait()
}

// run starts the program with values, waits for it to listen and pass
// its health check, and moves to running. A failure counts as a crash.
func (s *Service) run(ctx context.Context, m Manifest, values map[string]string) error {
	inst, ok := s.in.Installed(m)
	if !ok {
		return s.startFailed(errors.New("its install is missing; press Fix to install it again"))
	}
	token, err := newToken()
	if err != nil {
		return s.startFailed(err)
	}
	cmd, err := serviceCmd(s.in, m, inst, values, token)
	if err != nil {
		return s.startFailed(err)
	}
	basePath, err := fillPlaceholders(m.Launch.BasePath, m, inst.Dir, s.in.MeruDir, values, token)
	if err != nil {
		return s.startFailed(err)
	}
	base := strings.TrimSuffix(m.Launch.URL, "/") + basePath

	// The program's context starts from Background: it lives until idle,
	// Configure or Close stops it, not for the request that started it.
	procCtx, kill := context.WithCancel(context.Background())
	tail := &tailLog{log: s.log}
	startCtx, cancel := context.WithTimeout(ctx, s.startWait)
	defer cancel()
	exited, err := s.start(startCtx, procCtx, cmd, tail, m.Launch.URL)
	if err == nil {
		err = s.check(startCtx, base+m.Health.Path)
		if err != nil {
			kill()
			<-exited
		}
	}
	if err != nil {
		kill()
		return s.startFailed(tail.wrap(err))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.phase != servicePhaseReady {
		// Configure or Close came while the program started.
		kill()
		return errors.New(s.sentenceLocked())
	}
	s.gen++
	s.base, s.kill, s.exited, s.stopping, s.inUse = base, kill, exited, false, 0
	s.setLocked(servicePhaseRunning, "")
	gen := s.gen
	go s.watch(gen, exited, tail)
	return nil
}

// watch waits for the program of generation gen to exit. An exit nobody
// asked for is a crash.
func (s *Service) watch(gen int, exited <-chan struct{}, tail *tailLog) {
	<-exited
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || s.stopping || s.phase != servicePhaseRunning {
		return
	}
	s.kill()
	s.base, s.kill, s.exited = "", nil, nil
	stopTimer(&s.idleT)
	reason := "it exited"
	if line := tail.last(); line != "" {
		reason += ": " + line
	}
	s.log.Warn("connector exited on its own", "reason", reason)
	s.crashLocked(reason)
}

// startFailed records a start that failed with err as a crash, and
// returns the error to give the caller.
func (s *Service) startFailed(err error) error {
	s.log.Warn("connector didn't start", "err", err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.crashLocked(s.shortErr(err))
	return fmt.Errorf("%s didn't start: %s", s.m.Name, s.shortErr(err))
}

// crashLocked counts a crash, with reason saying why. The crashLimit-th
// crash within crashWindow moves to failed; before that the service is
// ready again, and the next request starts the program. The caller holds
// s.mu.
func (s *Service) crashLocked(reason string) {
	now := s.clock.Now()
	s.crashes = slices.DeleteFunc(s.crashes, func(t time.Time) bool { return now.Sub(t) >= crashWindow })
	s.crashes = append(s.crashes, now)
	if len(s.crashes) >= crashLimit {
		s.setLocked(servicePhaseFailed, fmt.Sprintf("%s keeps stopping: %s.", s.m.Name, strings.TrimSuffix(reason, ".")))
		return
	}
	if s.phase == servicePhaseReady || s.phase == servicePhaseRunning {
		s.setLocked(servicePhaseReady, "")
	}
}

// check sends the health check, a GET of url, and fails unless the answer
// is 2xx and holds what the manifest's expect asks for.
func (s *Service) check(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	// defer runs resp.Body.Close() when check returns.
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("its health check answered %s", resp.Status)
	}
	return expectBody(s.m.Health.Expect, body)
}

// expectBody checks body against a health check's expect: "" passes
// anything, "nonempty" needs some text, "contains:<text>" that text, and
// "json_key:<key>" a JSON object with that key.
func expectBody(expect string, body []byte) error {
	switch {
	case expect == "":
		return nil
	case expect == "nonempty":
		if len(bytes.TrimSpace(body)) == 0 {
			return errors.New("its health check answered with nothing")
		}
	case strings.HasPrefix(expect, "contains:"):
		if !bytes.Contains(body, []byte(strings.TrimPrefix(expect, "contains:"))) {
			return fmt.Errorf("its health check answer doesn't hold %q", strings.TrimPrefix(expect, "contains:"))
		}
	case strings.HasPrefix(expect, "json_key:"):
		var obj map[string]any
		key := strings.TrimPrefix(expect, "json_key:")
		if err := json.Unmarshal(body, &obj); err != nil {
			return errors.New("its health check didn't answer JSON")
		}
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("its health check answer has no %q", key)
		}
	}
	return nil
}

// newToken returns 32 random hex digits, 128 bits, for a start's {token}.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make a token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// serviceCmd returns the program, arguments and environment that start
// the installed service m with values, and token for its {token}
// placeholders. Its environment is childCmd's.
func serviceCmd(in *Installer, m Manifest, inst Installed, values map[string]string, token string) (Cmd, error) {
	program, args, env, err := in.launchCommand(m, inst, values, token)
	if err != nil {
		return Cmd{}, err
	}
	return Cmd{Path: program, Args: args, Env: childEnv(in, env), Dir: inst.Dir}, nil
}

// stopLocked asks the running program to stop, if one runs, and returns
// a function that waits for it to exit, to call after the lock is
// released. The caller holds s.mu, and sets the next phase.
func (s *Service) stopLocked() func() {
	stopTimer(&s.idleT)
	if s.kill == nil {
		return func() {}
	}
	kill, exited := s.kill, s.exited
	s.stopping = true
	s.base, s.kill, s.exited = "", nil, nil
	kill()
	return func() { <-exited }
}

// setLocked moves to phase p with sentence, and logs a change. The
// sentence is for failed and needs_config; the other phases write their
// own. The caller holds s.mu.
func (s *Service) setLocked(p servicePhase, sentence string) {
	if p != s.phase || sentence != s.sentence {
		s.log.Info("connector state", "state", p.state(), "sentence", sentence)
	}
	s.phase, s.sentence = p, sentence
}

// sentenceLocked returns the state's sentence. The caller holds s.mu.
func (s *Service) sentenceLocked() string {
	name := s.m.Name
	switch s.phase {
	case servicePhaseOff:
		if s.sentence != "" {
			return s.sentence
		}
		return name + " is off. Turn it on in Settings, Connections."
	case servicePhaseInstalling:
		return fmt.Sprintf("Meru is installing %s %s.", name, installVersion(s.m))
	case servicePhaseReady:
		return name + " is ready. It starts when you use it."
	case servicePhaseRunning:
		return name + " is running."
	}
	return s.sentence
}

// Status reports the connector for the connectors op: its state and
// sentence, its fields with the values config holds, and the fields to
// fix.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		ID: s.m.ID, Name: s.m.Name, Kind: s.m.Kind, Required: s.m.Required,
		State: s.phase.state(), Sentence: s.sentenceLocked(),
	}
	for _, f := range s.fields {
		st.Fields = append(st.Fields, FieldStatus{Field: f, Value: s.set.shown[f.ID], Saved: s.set.saved[f.ID]})
	}
	if s.phase == servicePhaseNeedsConfig {
		st.Fix = slices.Clone(s.set.fix)
	}
	return st
}

// Value returns the value field id has now, the user's or the default,
// such as the speech model in use, for a metric attribute. It is "" for a
// field the manifest doesn't have, or before the first Configure.
func (s *Service) Value(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.values[id]
}

// shortErr turns err into one line for a sentence, without the
// "connector <id>: " the installer puts in front.
func (s *Service) shortErr(err error) string {
	msg := firstLine(err.Error())
	msg = strings.TrimPrefix(msg, "connector "+s.m.ID+": ")
	return strings.TrimSuffix(msg, ".")
}

// Close stops the install and the program, and waits for both.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	stopInstall, installDone := s.stopInstall, s.installDone
	s.stopInstall, s.installDone = nil, nil
	wait := s.stopLocked()
	s.mu.Unlock()
	if stopInstall != nil {
		stopInstall()
		<-installDone
	}
	wait()
}
