// This file holds merud's watch on Ollama, the one connector Meru only
// reports on, and the gate in front of merud's handler. merud opens its
// socket before it checks Ollama, so while Ollama is down, too old or
// missing a model, merud still answers: the connectors op reports Ollama
// failed with a sentence, and every other request gets "Ollama isn't
// running, so Meru can't answer yet." merud checks again every 30
// seconds, and at once when a request comes, and warms the models once
// Ollama answers. After that it keeps checking, so the status stays
// true. See ARCHITECTURE.md, "SearXNG and Ollama".

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// ollamaEvery is how often merud checks Ollama. A check is one small
// request to Ollama's version endpoint on this machine.
const ollamaEvery = 30 * time.Second

// sentenceError is an error whose text is a whole sentence the user reads,
// capital letter and full stop included, which Go's usual error style
// leaves out. A client prints it as it is.
type sentenceError string

// Error returns the sentence.
func (e sentenceError) Error() string { return string(e) }

// errOllamaDown is what a request gets while Ollama doesn't answer.
const errOllamaDown = sentenceError("Ollama isn't running, so Meru can't answer yet.")

// ollamaWatch holds what merud knows about Ollama, for the connectors op
// and for the requests that come before merud is ready.
type ollamaWatch struct {
	m      connectors.Manifest
	eng    engine.Engine
	models config.Models
	url    string
	log    *slog.Logger
	every  time.Duration
	// nudge asks the waiting loop to check now. It holds one signal at
	// most: a buffered channel of size 1, so a request never blocks on
	// it, and many requests at once make one check.
	nudge chan struct{}

	mu       sync.Mutex // guards the fields below
	state    string
	sentence string
	down     bool // the last check found nothing answering
	// changed is closed and replaced at each change of state, so a
	// request waiting in the gate wakes up and looks again.
	changed chan struct{}
}

// newOllamaWatch returns the watch on Ollama at cfg's [ollama] base_url,
// through eng. It stands in the starting state until the first check. It
// fails when the manifests don't load, which only a broken build can
// cause.
func newOllamaWatch(cfg config.Config, eng engine.Engine, log *slog.Logger) (*ollamaWatch, error) {
	all, err := connectors.Load()
	if err != nil {
		return nil, fmt.Errorf("connectors: %w", err)
	}
	o := &ollamaWatch{
		eng: eng, models: cfg.Models, url: cfg.Ollama.BaseURL, log: log, every: ollamaEvery,
		nudge: make(chan struct{}, 1), changed: make(chan struct{}),
		state: connectors.StateStarting, sentence: "Meru is checking Ollama at " + cfg.Ollama.BaseURL + ".",
	}
	for _, m := range all {
		if m.ID == "ollama" {
			o.m = m
		}
	}
	if o.m.ID == "" {
		return nil, errors.New("connectors: no ollama manifest")
	}
	return o, nil
}

// waitReady returns true once Ollama answers, is new enough, and has
// loaded the fast and embedding models. It checks now, then every
// o.every, or sooner when poke asks. It returns false when ctx ends
// first.
func (o *ollamaWatch) waitReady(ctx context.Context) bool {
	for {
		if o.tryReady(ctx) {
			return true
		}
		if !o.wait(ctx) {
			return false
		}
	}
}

// wait sleeps o.every, or until a poke, and reports false when ctx ends
// first.
func (o *ollamaWatch) wait(ctx context.Context) bool {
	timer := time.NewTimer(o.every)
	// defer runs timer.Stop when wait returns, which frees the timer.
	defer timer.Stop()
	// select waits for whichever case is ready first.
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	case <-o.nudge:
	}
	return true
}

// poke asks the waiting loop to check Ollama now. It never blocks: when a
// signal already waits, this one adds nothing.
func (o *ollamaWatch) poke() {
	select {
	case o.nudge <- struct{}{}:
	default:
	}
}

// tryReady runs one check and, when Ollama passes it, the warm-up. It
// reports whether merud can take questions now.
func (o *ollamaWatch) tryReady(ctx context.Context) bool {
	version, problem := checkOllama(ctx, o.eng, o.url)
	if problem != "" {
		o.set(connectors.StateFailed, problem, version == "")
		return false
	}
	o.set(connectors.StateStarting, fmt.Sprintf("Ollama %s answers at %s; Meru is loading %s and %s.", version, o.url, o.models.Fast, o.models.Embed), false)
	if err := warm(ctx, o.eng, o.models, o.log); err != nil {
		if ctx.Err() != nil {
			return false
		}
		o.set(connectors.StateFailed, fmt.Sprintf("Ollama %s couldn't load a model: %v.", version, err), false)
		return false
	}
	o.set(connectors.StateOK, fmt.Sprintf("Ollama %s is running at %s.", version, o.url), false)
	o.log.Info("ollama ok", "version", version)
	return true
}

// watch keeps checking Ollama every o.every after merud is ready, so the
// status says so when Ollama stops or comes back. It changes nothing
// else: a question asked while Ollama is down fails with Ollama's own
// error, and the models load again on the next call. It returns when
// ctx ends.
func (o *ollamaWatch) watch(ctx context.Context) {
	for o.wait(ctx) {
		version, problem := checkOllama(ctx, o.eng, o.url)
		if ctx.Err() != nil {
			return
		}
		if problem != "" {
			o.set(connectors.StateFailed, problem, version == "")
			continue
		}
		o.set(connectors.StateOK, fmt.Sprintf("Ollama %s is running at %s.", version, o.url), false)
	}
}

// checkOllama asks Ollama at url, through eng, for its version. It
// returns the version, or "" when Ollama didn't answer, and a sentence
// that says what is wrong, or "" when nothing is: Ollama isn't running,
// or it is older than minOllama, which the router needs for log
// probabilities (ARCHITECTURE.md, "Model tiers").
func checkOllama(ctx context.Context, eng engine.Engine, url string) (version, problem string) {
	info, err := eng.Info(ctx)
	if err != nil {
		return "", "Ollama isn't running at " + url + "."
	}
	ok, err := versionAtLeast(info.RuntimeVersion, minOllama)
	switch {
	case err != nil:
		return info.RuntimeVersion, fmt.Sprintf("Ollama answered with a version Meru can't read, %q; Meru needs %s or later.", info.RuntimeVersion, minOllama)
	case !ok:
		return info.RuntimeVersion, fmt.Sprintf("Ollama %s is too old; Meru needs %s or later.", info.RuntimeVersion, minOllama)
	}
	return info.RuntimeVersion, ""
}

// set records Ollama's state and sentence, and logs a change. down says
// whether nothing answered at all.
func (o *ollamaWatch) set(state, sentence string, down bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if state == o.state && sentence == o.sentence {
		return
	}
	o.log.Info("connector state", "connector", o.m.ID, "state", state, "sentence", sentence)
	o.state, o.sentence, o.down = state, sentence, down
	close(o.changed)
	o.changed = make(chan struct{})
}

// status reports Ollama as a connector, for the connectors op.
func (o *ollamaWatch) status() connectors.Status {
	o.mu.Lock()
	defer o.mu.Unlock()
	return connectors.Status{
		ID: o.m.ID, Name: o.m.Name, Kind: o.m.Kind, Required: o.m.Required,
		State: o.state, Sentence: o.sentence,
	}
}

// notReady is the error a request gets before merud is ready: the
// sentence for a down Ollama, or what is wrong otherwise.
func (o *ollamaWatch) notReady() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.down {
		return errOllamaDown
	}
	return sentenceError("Meru can't answer yet: " + o.sentence)
}

// failing reports whether the last check found Ollama down, too old or
// short of a model, and returns the channel that closes at the next
// change of state.
func (o *ollamaWatch) failing() (bool, <-chan struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state == connectors.StateFailed, o.changed
}

// answer answers req while Ollama fails, before merud is ready. It
// answers the connectors op with Ollama's row, the only connector merud
// has set up by then, and mcp_status with no servers, so `meru mcp
// status` prints the connectors; every other request gets the error
// notReady gives. The rpc server answers ping itself.
func (o *ollamaWatch) answer(req rpc.Request, emit func(rpc.Event) error) error {
	switch req.Op {
	case rpc.OpConnectors:
		return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: []rpc.ConnectorStatus{connectorRow(o.status())}})
	case rpc.OpMCPStatus:
		return emit(rpc.Event{Type: rpc.EventMCPStatus, MCP: []rpc.MCPStatus{}})
	}
	return o.notReady()
}

// gate is the handler rpc.Serve runs. It lets merud open its socket
// before Ollama answers: until merud is ready, a request waits while
// Ollama is being checked or the models load, as it waited in the
// socket's queue before, and gets Ollama's answer while Ollama fails.
// Once open runs, every request goes to the full handler.
type gate struct {
	ollama *ollamaWatch
	// ready closes when open sets full. A closed channel is ready to
	// read at once, for every reader; full is written before the close
	// and read only after it, so it needs no lock.
	ready chan struct{}
	full  rpc.Handler
}

// newGate returns a closed gate that answers from o.
func newGate(o *ollamaWatch) *gate {
	return &gate{ollama: o, ready: make(chan struct{})}
}

// open hands every request from now on to h. merud calls it once.
func (g *gate) open(h rpc.Handler) {
	g.full = h
	close(g.ready)
}

// handle answers one request: with the full handler once the gate is
// open; with Ollama's answer while Ollama fails, after asking for a check
// at once, so a user who has just started Ollama doesn't wait 30
// seconds; and otherwise it waits for the gate to open or Ollama's state
// to change, whichever comes first.
func (g *gate) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
	for {
		select {
		case <-g.ready:
			return g.full(ctx, req, emit, approve)
		default:
		}
		failed, changed := g.ollama.failing()
		if failed {
			g.ollama.poke()
			return g.ollama.answer(req, emit)
		}
		select {
		case <-g.ready:
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
