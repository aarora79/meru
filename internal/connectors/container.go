// This file holds the Container supervisor: the one for a connector that
// runs as a container, SearXNG today. It checks the connector's URL when
// it starts and every minute after, and when nothing healthy answers and
// config asks Meru to run it, it pulls the pinned image, starts Meru's own
// container, and waits for the check to pass. A container that stops
// answering is started again after a growing wait, as a stdio connector
// is. A healthy server that isn't Meru's container counts as external:
// Meru reports on it and never starts, stops or pulls anything for it.
// See ARCHITECTURE.md, "SearXNG and Ollama".
//
//	off ──enable──▶ checking ──healthy──▶ ok (Meru's container, or external)
//	                   │ nothing answers         ▲            │ stops answering
//	                   ▼                         │            ▼
//	      needs_config ◀─ no Docker        start ┘      backoff 1, 2, 4, 8 s ──▶ start
//	                   └─ pull, run, wait ───────┘      fifth crash in 10 min ──▶ failed

package connectors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// Mode says what merud does for a container connector.
type Mode int

// The modes, counted up from 0 by iota.
const (
	// ModeOff: merud neither checks nor runs it.
	ModeOff Mode = iota
	// ModeWatch: merud checks the URL and reports what it finds, and
	// never starts anything. A config from before the connectors gets
	// this, so an upgrade starts no container nobody asked for.
	ModeWatch
	// ModeRun: merud checks the URL, and runs its own container when
	// nothing healthy answers there.
	ModeRun
)

// ErrNothingListens is what a container's check wraps when nothing
// accepts the connection. Any other failed check means something answers
// at the URL, and Meru leaves that server alone.
var ErrNothingListens = errors.New("nothing answers there")

// errForeign means a container that Meru didn't start holds the name
// Meru's container needs.
var errForeign = errors.New("a container Meru didn't start holds the name")

// The container's timings.
const (
	// containerCheckEvery is how often merud checks a container
	// connector while it is on: once a minute. The check sends nothing
	// off the machine, so it costs almost nothing.
	containerCheckEvery = time.Minute
	// containerStartWait is how long a started container gets to pass
	// its check. SearXNG's first start takes a few seconds; 90 leaves
	// room for a slow disk.
	containerStartWait = 90 * time.Second
	// containerPollEvery is how often merud checks while it waits.
	containerPollEvery = 2 * time.Second
	// labelKey is the Docker label that marks Meru's own container. Its
	// value is the connector's ID.
	labelKey = "meru.connector"
)

// ContainerMode picks the mode for a container connector m from config:
// table is its [connectors.<id>] table, url the address config says it
// answers at ([web] searxng_url for SearXNG), and toolListed whether
// [builtin] tools lists the tool that uses it (web_search).
//
// The rule, in order:
//
//   - no url: off, since the tool has nowhere to go;
//   - enabled = false: off;
//   - enabled = true and url is the manifest's own: run;
//   - enabled = true and another url: watch, since Meru's container
//     answers only at the manifest's address;
//   - no enabled key: watch when the manifest is on by default and the
//     tool is listed, as in a config from before the connectors; off
//     otherwise.
func ContainerMode(m Manifest, table config.Connector, url string, toolListed bool) Mode {
	on, set := table.Enabled()
	switch {
	case url == "":
		return ModeOff
	case set && !on:
		return ModeOff
	case set && sameURL(url, m.Launch.URL):
		return ModeRun
	case set:
		return ModeWatch
	case m.DefaultOn && toolListed:
		return ModeWatch
	}
	return ModeOff
}

// sameURL reports whether two URLs are the same once a trailing "/" goes.
func sameURL(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// Container supervises one container connector. Create it with
// NewContainer, hand it config with Configure, and stop it with Close.
// Its methods are safe to call from many goroutines at once.
//
// It owns one goroutine while it is on: the loop that checks the URL and
// acts. Close and each new Configure stop the loop and wait for it. Close
// leaves Meru's container running, so a restart of merud doesn't restart
// the search engine too.
type Container struct {
	m     Manifest
	in    *Installer
	log   *slog.Logger
	clock Clock
	// check tells whether the server at a URL works; merud passes
	// catalog.CheckSearXNG, wrapped so a refused connection reads as
	// ErrNothingListens. prepare runs before a new container starts;
	// merud's writes SearXNG's settings.yml when it is missing.
	check   func(ctx context.Context, url string) error
	prepare func() error
	// name is the container's name, "meru-" and the ID.
	name string
	// startWait and pollEvery are containerStartWait and
	// containerPollEvery; tests may shorten them.
	startWait, pollEvery time.Duration

	// configuring lets one Configure or Close run at a time, since each
	// stops the old loop before it starts the next.
	configuring sync.Mutex

	// mu guards every field below it.
	mu         sync.Mutex
	closed     bool
	configured bool
	mode       Mode
	url        string
	problem    string // a key the manifest doesn't know, as a sentence
	state      string
	sentence   string
	ours       bool        // the healthy server is Meru's own container
	reason     string      // what the last crash said, during a backoff
	retryAt    time.Time   // when the backoff ends
	crashes    []time.Time // within the last crashWindow
	stopLoop   context.CancelFunc
	loopDone   chan struct{}
}

// NewContainer returns the supervisor for the container connector m,
// which runs docker through in. check and prepare are as the Container
// type says. It starts nothing, and stands off until Configure. It fails
// for a connector of another kind.
func NewContainer(m Manifest, in *Installer, check func(ctx context.Context, url string) error, prepare func() error, log *slog.Logger) (*Container, error) {
	if m.Kind != KindContainer {
		return nil, fmt.Errorf("connector %s: a Container runs only container connectors, not %s", m.ID, m.Kind)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if prepare == nil {
		prepare = func() error { return nil }
	}
	return &Container{
		m: m, in: in, log: log.With("connector", m.ID), clock: realClock{},
		check: check, prepare: prepare, name: "meru-" + m.ID,
		startWait: containerStartWait, pollEvery: containerPollEvery,
		state: StateOff, sentence: m.Name + " is off.",
	}, nil
}

// Manifest returns the connector's manifest.
func (c *Container) Manifest() Manifest { return c.m }

// Configure hands the container its part of config: table, the
// [connectors.<id>] table (nil when config has none); url, where the
// server answers; and toolListed, whether [builtin] tools lists the tool
// that uses it. merud calls it at startup and on each reload.
//
// When nothing changed and the connector hasn't failed, the loop keeps
// going. Otherwise the old loop stops, the crashes are forgotten, and a
// new loop starts from a fresh check. Leaving run mode stops Meru's own
// container, if it runs; a server that isn't Meru's is never touched.
func (c *Container) Configure(table config.Connector, url string, toolListed bool) {
	mode := ContainerMode(c.m, table, url, toolListed)
	// checkSettings finds a key the manifest doesn't know. A container
	// connector has no fields, so only "enabled" belongs in its table.
	problem := checkSettings(c.m, table, nil, "").problem

	c.configuring.Lock()
	defer c.configuring.Unlock()
	c.mu.Lock()
	if c.closed || (c.configured && mode == c.mode && url == c.url && problem == c.problem && c.state != StateFailed) {
		c.mu.Unlock()
		return
	}
	stopOurs := c.configured && c.mode == ModeRun && mode != ModeRun
	cancel, done := c.stopLoop, c.loopDone
	c.configured, c.mode, c.url, c.problem = true, mode, url, problem
	c.crashes, c.ours = nil, false
	switch {
	case mode == ModeOff:
		c.setLocked(StateOff, c.m.Name+" is off.")
	case problem != "":
		c.setLocked(StateNeedsConfig, problem)
	default:
		c.setLocked(StateStarting, fmt.Sprintf("Meru is checking %s at %s.", c.m.Name, url))
	}
	c.mu.Unlock()

	// Stop the old loop and wait for it outside the lock: it may be in
	// the middle of a docker command, which the cancel ends.
	if cancel != nil {
		cancel()
		<-done
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	// The loop's context starts from Background: it lives until the next
	// Configure or Close, not for one request.
	ctx, cancel := context.WithCancel(context.Background())
	c.stopLoop, c.loopDone = cancel, make(chan struct{})
	on := mode != ModeOff && problem == ""
	// go starts the loop in its own goroutine, a function that runs at
	// the same time as the rest of merud.
	go c.loop(ctx, c.loopDone, mode, url, on, stopOurs)
}

// loop is the container's one goroutine. It stops Meru's container first
// when config left run mode, then, while the connector is on, runs one
// pass after another, each followed by the wait the pass asks for. It
// closes done when it returns, which it does when ctx ends.
func (c *Container) loop(ctx context.Context, done chan struct{}, mode Mode, url string, on, stopOurs bool) {
	defer close(done)
	if stopOurs {
		c.stopOurs(ctx)
	}
	if !on {
		return
	}
	for {
		wait := c.pass(ctx, mode, url)
		if !c.sleep(ctx, wait) {
			return
		}
	}
}

// sleep waits d on the supervisor's clock, and reports false when ctx
// ends first.
func (c *Container) sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	// A channel carries a signal between goroutines. The timer's function
	// closes it, and a closed channel is ready to read at once.
	fired := make(chan struct{})
	t := c.clock.AfterFunc(d, func() { close(fired) })
	// select waits for whichever case is ready first.
	select {
	case <-fired:
		return true
	case <-ctx.Done():
		t.Stop()
		return false
	}
}

// pass checks the URL once and acts on what it finds. It returns how long
// the loop waits before the next pass: a minute after a check that
// settles the state, or the backoff after a crash.
//
//   - Healthy: ok. In run mode, it asks docker whether the server is
//     Meru's own container, for the sentence.
//   - Not healthy, watch mode: needs_config, with the reason.
//   - Not healthy, run mode: a crash if Meru's container was running;
//     nothing more once failed; needs_config when something that isn't
//     Meru's container answers there, when Docker is missing or stopped,
//     or when a container without Meru's label holds its name.
//     Otherwise it installs the pinned image, starts the container and
//     waits for the check to pass.
func (c *Container) pass(ctx context.Context, mode Mode, url string) time.Duration {
	err := c.check(ctx, url)
	if ctx.Err() != nil {
		return 0
	}
	if err == nil {
		ours := mode == ModeRun && c.isOurs(ctx)
		c.healthy(ours, url)
		return containerCheckEvery
	}
	if mode == ModeWatch {
		c.set(StateNeedsConfig, c.watchSentence(url, err))
		return containerCheckEvery
	}

	c.mu.Lock()
	wasOurs, failed := c.state == StateOK && c.ours, c.state == StateFailed
	c.mu.Unlock()
	if wasOurs {
		c.log.Warn("connector stopped answering", "err", err)
		return c.crash(shortReason(err))
	}
	if failed {
		return containerCheckEvery
	}
	if !errors.Is(err, ErrNothingListens) && !c.isOurs(ctx) {
		// Something that isn't Meru's container answers there. Starting
		// another would only fight it for the port.
		c.set(StateNeedsConfig, fmt.Sprintf("%s can't use SearXNG at %s: %s.", c.m.Name, url, shortReason(err)))
		return containerCheckEvery
	}

	docker, err := c.dockerReady(ctx)
	switch {
	case ctx.Err() != nil:
		return 0
	case errors.Is(err, ErrDockerMissing):
		c.set(StateNeedsConfig, c.m.Name+" needs Docker, which isn't installed.")
		return containerCheckEvery
	case err != nil:
		c.set(StateNeedsConfig, c.m.Name+" can't start: Docker isn't running.")
		return containerCheckEvery
	}

	// A container of Meru's name that Meru didn't start, such as one an
	// older installer made, stays as it is: nothing is pulled or run.
	if info, err := c.inspect(ctx, docker); err == nil && info.exists && info.label != c.m.ID {
		c.set(StateNeedsConfig, c.foreignSentence())
		return containerCheckEvery
	}
	if _, ok := c.in.Installed(c.m); !ok {
		c.set(StateStarting, fmt.Sprintf("Meru is downloading %s (image %s) and starting it.", c.m.Name, installVersion(c.m)))
	} else {
		c.set(StateStarting, c.m.Name+" is starting.")
	}
	if _, err := c.in.Install(ctx, c.m, func(line string) { c.log.Debug("connector install", "line", line) }); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		c.set(StateFailed, fmt.Sprintf("%s couldn't install: %s.", c.m.Name, c.shortErr(err)))
		return containerCheckEvery
	}
	c.set(StateStarting, c.m.Name+" is starting.")
	if err := c.start(ctx, docker); err != nil {
		switch {
		case ctx.Err() != nil:
			return 0
		case errors.Is(err, errForeign):
			c.set(StateNeedsConfig, c.foreignSentence())
			return containerCheckEvery
		}
		return c.crash("couldn't start: " + c.shortErr(err))
	}
	if err := c.waitHealthy(ctx, url); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		return c.crash(fmt.Sprintf("didn't pass its check within %s: %s", c.startWait, shortReason(err)))
	}
	c.healthy(true, url)
	return containerCheckEvery
}

// healthy moves to ok. ours says whether the server is Meru's container,
// which changes the sentence. A server that isn't Meru's clears the
// crash count: whatever failed before, the user's own server works now.
func (c *Container) healthy(ours bool, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ours = ours
	if ours {
		c.setLocked(StateOK, fmt.Sprintf("%s is running in the container %s.", c.m.Name, c.name))
		return
	}
	c.crashes = nil
	c.setLocked(StateOK, fmt.Sprintf("%s uses the SearXNG already running at %s.", c.m.Name, url))
}

// foreignSentence says that a container Meru didn't start holds the name
// Meru's container needs, and what the user can do.
func (c *Container) foreignSentence() string {
	return fmt.Sprintf("%s can't start: a container named %s that Meru didn't start holds the name. "+
		"Start it yourself, or remove it with docker rm %s.", c.m.Name, c.name, c.name)
}

// watchSentence says why watch mode found no working server at url. When
// url is the manifest's own, Meru could run one there, and the sentence
// says how to ask it to.
func (c *Container) watchSentence(url string, err error) string {
	s := fmt.Sprintf("%s can't use SearXNG at %s: %s.", c.m.Name, url, shortReason(err))
	if sameURL(url, c.m.Launch.URL) {
		s += fmt.Sprintf(" To have Meru run it, set enabled = true under [connectors.%s] in config.toml, then restart merud.", c.m.ID)
	}
	return s
}

// crash records a crash with detail saying why, and returns how long to
// wait before the next start: a backoff that doubles from firstBackoff,
// or, at the crashLimit-th crash within crashWindow, failed and a
// minute's wait, after which the loop only checks.
func (c *Container) crash(detail string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	c.crashes = slices.DeleteFunc(c.crashes, func(t time.Time) bool { return now.Sub(t) >= crashWindow })
	c.crashes = append(c.crashes, now)
	c.ours = false
	detail = strings.TrimSuffix(detail, ".")
	if len(c.crashes) >= crashLimit {
		c.setLocked(StateFailed, fmt.Sprintf("%s keeps stopping: %s.", c.m.Name, detail))
		return containerCheckEvery
	}
	delay := min(firstBackoff<<(len(c.crashes)-1), maxBackoff)
	c.reason, c.retryAt = detail, now.Add(delay)
	c.setLocked(StateStarting, "") // Status writes the backoff's sentence
	return delay
}

// waitHealthy checks url every pollEvery until the check passes, or
// startWait has gone by, and returns the last check's error.
func (c *Container) waitHealthy(ctx context.Context, url string) error {
	deadline := c.clock.Now().Add(c.startWait)
	for {
		err := c.check(ctx, url)
		if err == nil || !c.clock.Now().Before(deadline) {
			return err
		}
		if !c.sleep(ctx, c.pollEvery) {
			return ctx.Err()
		}
	}
}

// dockerReady finds docker and checks that its engine answers. It fails
// with ErrDockerMissing or ErrDockerNotRunning.
func (c *Container) dockerReady(ctx context.Context) (string, error) {
	docker, err := c.in.findDocker()
	if err != nil {
		return "", err
	}
	if _, err := c.docker(ctx, docker, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDockerNotRunning, err)
	}
	return docker, nil
}

// docker runs one docker command through the Installer's Runner and
// returns its output. The error ends with docker's last line of output.
func (c *Container) docker(ctx context.Context, docker string, args ...string) (string, error) {
	out, err := c.in.Run(ctx, Cmd{Path: docker, Args: args, Env: c.in.dockerEnv(docker)}, nil)
	if err != nil {
		if line := lastLine(out); line != "" {
			return out, fmt.Errorf("%w (%s)", err, line)
		}
		return out, err
	}
	return out, nil
}

// containerInfo is what docker inspect says about the container.
type containerInfo struct {
	exists  bool
	running bool
	label   string // the value of labelKey; "" when the label is missing
	image   string // the image as docker run named it
}

// inspect asks docker about the container named c.name. A container that
// doesn't exist is no error: exists is false.
func (c *Container) inspect(ctx context.Context, docker string) (containerInfo, error) {
	format := `{{.State.Running}}|{{index .Config.Labels "` + labelKey + `"}}|{{.Config.Image}}`
	out, err := c.docker(ctx, docker, "inspect", "--format", format, c.name)
	if err != nil {
		if strings.Contains(strings.ToLower(out+err.Error()), "no such object") {
			return containerInfo{}, nil
		}
		return containerInfo{}, err
	}
	parts := strings.SplitN(lastLine(out), "|", 3)
	if len(parts) != 3 {
		return containerInfo{}, fmt.Errorf("docker inspect answered %q", lastLine(out))
	}
	return containerInfo{exists: true, running: parts[0] == "true", label: parts[1], image: parts[2]}, nil
}

// isOurs reports whether Meru's container runs now: docker is there, and
// a running container of c.name carries Meru's label.
func (c *Container) isOurs(ctx context.Context) bool {
	docker, err := c.in.findDocker()
	if err != nil {
		return false
	}
	info, err := c.inspect(ctx, docker)
	return err == nil && info.running && info.label == c.m.ID
}

// start gets Meru's container running: a new one when none exists, a
// fresh one when the old one runs another image (a new pin), docker
// start for a stopped one, and docker restart for one that runs but
// fails its check. It fails with errForeign when a container without
// Meru's label holds the name, and touches nothing then.
func (c *Container) start(ctx context.Context, docker string) error {
	info, err := c.inspect(ctx, docker)
	switch {
	case err != nil:
		return err
	case !info.exists:
		return c.runNew(ctx, docker)
	case info.label != c.m.ID:
		return errForeign
	case info.image != c.m.Install.Image:
		c.log.Info("connector image changed; replacing the container", "old", info.image)
		if _, err := c.docker(ctx, docker, "rm", "--force", c.name); err != nil {
			return err
		}
		return c.runNew(ctx, docker)
	case info.running:
		_, err := c.docker(ctx, docker, "restart", c.name)
		return err
	}
	_, err = c.docker(ctx, docker, "start", c.name)
	return err
}

// runNew runs prepare, then starts a new container from the pinned image.
func (c *Container) runNew(ctx context.Context, docker string) error {
	if err := c.prepare(); err != nil {
		return err
	}
	args, err := c.runArgs()
	if err != nil {
		return err
	}
	_, err = c.docker(ctx, docker, args...)
	return err
}

// runArgs returns docker's arguments that start Meru's container. Each is
// a separate string, and docker gets them with no shell.
//
//   - --restart no: the supervisor alone decides when the container
//     starts again, so it counts every crash, and a container nobody
//     uses doesn't come back after a restart of the computer. merud
//     starts it again at its own next start.
//   - --publish binds the manifest URL's loopback address and port to
//     launch.port inside the container, so no other machine can reach
//     it.
//   - --label marks it as Meru's, so Meru never mistakes another
//     container for its own.
func (c *Container) runArgs() ([]string, error) {
	u, err := url.Parse(c.m.Launch.URL)
	if err != nil {
		return nil, fmt.Errorf("connector %s: launch.url: %w", c.m.ID, err)
	}
	args := []string{
		"run", "--detach",
		"--name", c.name,
		"--label", labelKey + "=" + c.m.ID,
		"--restart", "no",
		"--publish", fmt.Sprintf("%s:%s:%d", u.Hostname(), u.Port(), c.m.Launch.Port),
	}
	for _, v := range c.m.Launch.Volumes {
		filled, err := fillPlaceholders(v, c.m, "", c.in.MeruDir, nil)
		if err != nil {
			return nil, err
		}
		args = append(args, "--volume", filled)
	}
	// Ranging over a map visits keys in a random order; sorting them keeps
	// the command the same from one start to the next.
	keys := make([]string, 0, len(c.m.Launch.Env))
	for k := range c.m.Launch.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+c.m.Launch.Env[k])
	}
	return append(args, c.m.Install.Image), nil
}

// stopOurs stops Meru's container when it runs, after config turned the
// connector off. It leaves any other container alone, and only logs a
// failure: the connector is off either way.
func (c *Container) stopOurs(ctx context.Context) {
	docker, err := c.in.findDocker()
	if err != nil {
		return
	}
	info, err := c.inspect(ctx, docker)
	if err != nil || !info.running || info.label != c.m.ID {
		return
	}
	if _, err := c.docker(ctx, docker, "stop", c.name); err != nil {
		c.log.Warn("couldn't stop the connector's container", "err", err)
		return
	}
	c.log.Info("stopped the connector's container", "container", c.name)
}

// set moves to state with sentence.
func (c *Container) set(state, sentence string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(state, sentence)
}

// setLocked moves to state with sentence, and logs a change. The caller
// holds c.mu.
func (c *Container) setLocked(state, sentence string) {
	if state != c.state || sentence != c.sentence {
		c.log.Info("connector state", "state", state, "sentence", sentence)
	}
	c.state, c.sentence = state, sentence
}

// OK reports whether the connector works now, and says its state in one
// line either way. merud offers web_search only while it is ok.
func (c *Container) OK() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == StateOK, c.sentenceLocked()
}

// Status reports the connector for the connectors op.
func (c *Container) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		ID: c.m.ID, Name: c.m.Name, Kind: c.m.Kind, Required: c.m.Required,
		State: c.state, Sentence: c.sentenceLocked(),
	}
}

// sentenceLocked returns the state's sentence; during a backoff it writes
// one with the seconds left. The caller holds c.mu.
func (c *Container) sentenceLocked() string {
	if c.state == StateStarting && c.sentence == "" {
		// math.Ceil rounds up, so a wait of 0.4 s reads "1 s".
		secs := max(int(math.Ceil(c.retryAt.Sub(c.clock.Now()).Seconds())), 0)
		return fmt.Sprintf("%s stopped (%s) and starts again in %d s.", c.m.Name, c.reason, secs)
	}
	return c.sentence
}

// shortErr turns err into one line for a sentence, without the
// "connector <id>: " the installer puts in front.
func (c *Container) shortErr(err error) string {
	msg := firstLine(err.Error())
	msg = strings.TrimPrefix(msg, "connector "+c.m.ID+": ")
	return strings.TrimSuffix(msg, ".")
}

// shortReason is err's first line without a closing full stop, for the
// middle of a sentence.
func shortReason(err error) string {
	return strings.TrimSuffix(firstLine(err.Error()), ".")
}

// Close stops the loop and waits for it. It leaves Meru's container
// running: the next merud finds it healthy and uses it at once.
func (c *Container) Close() {
	c.configuring.Lock()
	defer c.configuring.Unlock()
	c.mu.Lock()
	c.closed = true
	cancel, done := c.stopLoop, c.loopDone
	c.stopLoop = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
