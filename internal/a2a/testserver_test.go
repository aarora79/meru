// This file holds the test agent: a real A2A server, built with the SDK's
// server package and run on 127.0.0.1 by httptest, whose behaviour each
// test scripts.

package a2a

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// executeFunc is one scripted agent run: it sends updates with yield.
type executeFunc func(ctx context.Context, ec *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool)

// scriptedAgent is an a2asrv.AgentExecutor that runs execute for every
// message and reports cancel requests on cancelled.
type scriptedAgent struct {
	execute   executeFunc
	cancelled chan sdk.TaskID
}

// Execute runs the scripted function as the SDK's iterator.
func (s *scriptedAgent) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] {
	return func(yield func(sdk.Event, error) bool) { s.execute(ctx, ec, yield) }
}

// Cancel reports the task and marks it canceled.
func (s *scriptedAgent) Cancel(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] {
	return func(yield func(sdk.Event, error) bool) {
		select {
		case s.cancelled <- ec.TaskID:
		default:
		}
		yield(sdk.NewStatusUpdateEvent(ec, sdk.TaskStateCanceled, nil), nil)
	}
}

// testAgent is a running test server plus what it saw.
type testAgent struct {
	srv       *httptest.Server
	cancelled chan sdk.TaskID

	mu        sync.Mutex
	cardHits  int
	calls     int
	streams   int         // calls that asked for an event stream
	headers   http.Header // headers of the last request
	cardFails bool        // when true, the card URL answers 503
}

// agentOptions sets up a test agent.
type agentOptions struct {
	streaming bool
	skills    []sdk.AgentSkill
	execute   executeFunc
	// interfaceURL is the agent URL the card lists; empty means the test
	// server itself.
	interfaceURL string
}

// defaultSkills is what the test card lists unless a test says otherwise.
var defaultSkills = []sdk.AgentSkill{
	{ID: "summarize", Name: "Summarize", Description: "Summarize a document."},
	{ID: "translate", Name: "Translate", Description: "Translate text."},
	{ID: "delete_all", Name: "Delete", Description: "Delete everything."},
}

// startAgent starts a test agent and stops it when the test ends.
func startAgent(t *testing.T, opts agentOptions) *testAgent {
	t.Helper()
	if opts.skills == nil {
		opts.skills = defaultSkills
	}
	if opts.execute == nil {
		opts.execute = completeWith("done")
	}
	ta := &testAgent{cancelled: make(chan sdk.TaskID, 1)}
	exec := &scriptedAgent{execute: opts.execute, cancelled: ta.cancelled}
	rpcHandler := a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec))

	var card http.Handler
	mux := http.NewServeMux()
	mux.HandleFunc(a2asrv.WellKnownAgentCardPath, func(w http.ResponseWriter, r *http.Request) {
		ta.mu.Lock()
		ta.cardHits++
		ta.headers = r.Header.Clone()
		fail := ta.cardFails
		ta.mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		card.ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ta.mu.Lock()
		ta.calls++
		if r.Header.Get("Accept") == "text/event-stream" {
			ta.streams++
		}
		ta.headers = r.Header.Clone()
		ta.mu.Unlock()
		rpcHandler.ServeHTTP(w, r)
	})
	ta.srv = httptest.NewServer(mux)
	t.Cleanup(ta.srv.Close)

	if opts.interfaceURL == "" {
		opts.interfaceURL = ta.srv.URL
	}
	card = a2asrv.NewStaticAgentCardHandler(&sdk.AgentCard{
		Name:                "Research Agent",
		SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface(opts.interfaceURL, sdk.TransportProtocolJSONRPC)},
		Capabilities:        sdk.AgentCapabilities{Streaming: opts.streaming},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
		Skills:              opts.skills,
	})
	return ta
}

// snapshot returns the counters and the last request's headers.
func (ta *testAgent) snapshot() (cardHits, calls, streams int, headers http.Header) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	return ta.cardHits, ta.calls, ta.streams, ta.headers
}

// setCardFails makes the card URL fail or work.
func (ta *testAgent) setCardFails(fail bool) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	ta.cardFails = fail
}

// completeWith returns a run that starts a task, sends one artifact with
// text, and completes.
func completeWith(text string) executeFunc {
	return func(_ context.Context, ec *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool) {
		if !yield(sdk.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		if !yield(sdk.NewArtifactEvent(ec, sdk.NewTextPart(text)), nil) {
			return
		}
		yield(sdk.NewStatusUpdateEvent(ec, sdk.TaskStateCompleted, nil), nil)
	}
}

// endWith returns a run that starts a task and ends it in state, with a
// status message holding reason.
func endWith(state sdk.TaskState, reason string) executeFunc {
	return func(_ context.Context, ec *a2asrv.ExecutorContext, yield func(sdk.Event, error) bool) {
		if !yield(sdk.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		msg := sdk.NewMessageForTask(sdk.MessageRoleAgent, ec, sdk.NewTextPart(reason))
		yield(sdk.NewStatusUpdateEvent(ec, state, msg), nil)
	}
}
