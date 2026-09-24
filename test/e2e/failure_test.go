//go:build e2e

// This file tests what happens when a turn goes wrong: the user presses
// Ctrl-C part way through an answer, Ollama returns an error, or Ollama goes
// away. In every case meru must exit with the right status and merud must
// stay up for the next question.

package e2e

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// TestCancelMidAnswer sends SIGINT to meru while the answer streams. meru
// must exit 130, merud must cancel its call to Ollama and write no answer
// line, and the next question must still work.
func TestCancelMidAnswer(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	// The first chunk comes at once; the second would take a minute.
	s.fake.enqueue(t, fastModel, directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Chunks: []string{"first ", "second"}, ChunkDelay: time.Minute})

	const question = "tell me a long story"
	p := startMeru(t, s.home, question)
	waitFor(t, callTimeout, "the first chunk on meru's stdout", func() bool {
		return strings.Contains(p.stdout.String(), "first")
	})
	p.signal(t, syscall.SIGINT)
	if code := p.wait(t, exitTimeout); code != 130 {
		t.Errorf("meru exited %d after SIGINT, want 130; stderr:\n%s", code, p.stderr.String())
	}

	// merud closes its HTTP request to Ollama when the client goes away. The
	// fake notices the closed connection and marks the request cancelled.
	waitFor(t, callTimeout, "the fake to record a cancelled answer call", func() bool {
		for _, r := range s.fake.chatRequests(t, mainModel) {
			if r.Stream && r.Cancelled {
				return true
			}
		}
		return false
	})

	s.fake.enqueue(t, fastModel, directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "still here"})
	res := runMeru(t, s.home, "are you there?")
	if res.code != 0 || res.stdout != "still here\n" {
		t.Fatalf("next question: exit %d, stdout %q, stderr %q; want 0 and \"still here\\n\"", res.code, res.stdout, res.stderr)
	}

	// Each meru run starts its own session. The cancelled one holds the
	// question and nothing else.
	found := false
	for _, path := range sessionFiles(t, s.home) {
		lines := readTranscript(t, path)
		if len(lines) == 0 || lines[0].Text != question {
			continue
		}
		found = true
		if len(lines) != 1 || lines[0].Type != transcript.TypeUser {
			t.Errorf("cancelled session %s has lines %+v, want only the user line", path, lines)
		}
	}
	if !found {
		t.Errorf("no transcript holds the cancelled question")
	}
}

// TestOllamaErrors makes the fake fail at different points in a turn. Each
// time meru must exit 1 and print the reason on stderr, and merud must keep
// running.
func TestOllamaErrors(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	tests := []struct {
		name       string
		script     func(t *testing.T) // sets up the failure on the fake
		wantStdout string
		wantErr    []string // pieces stderr must contain
	}{
		{
			name: "router call gets HTTP 500",
			script: func(t *testing.T) {
				s.fake.failNext(t, "/api/chat", 500, "model crashed")
			},
			wantErr: []string{"route", "500", "model crashed"},
		},
		{
			name: "answer call gets HTTP 503",
			script: func(t *testing.T) {
				s.fake.enqueue(t, fastModel, directRoute())
				s.fake.enqueue(t, mainModel, fakeollama.Reply{Status: 503, Error: "server busy"})
			},
			wantErr: []string{mainModel, "503", "server busy"},
		},
		{
			name: "answer stream breaks part way",
			script: func(t *testing.T) {
				s.fake.enqueue(t, fastModel, directRoute())
				s.fake.enqueue(t, mainModel, fakeollama.Reply{Chunks: []string{"partial ", "answer"}, StreamError: "out of memory", FailAfter: 1})
			},
			// meru ends the half-printed line before printing the error.
			wantStdout: "partial \n",
			wantErr:    []string{mainModel, "out of memory"},
		},
	}
	// The subtests share one merud, so they run one after another.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.script(t)
			res := runMeru(t, s.home, "question for "+tt.name)
			if res.code != 1 {
				t.Errorf("meru exited %d, want 1", res.code)
			}
			if res.stdout != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", res.stdout, tt.wantStdout)
			}
			if !strings.HasPrefix(res.stderr, "meru: ") {
				t.Errorf("stderr = %q, want it to start with \"meru: \"", res.stderr)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(res.stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", res.stderr, want)
				}
			}
			if s.merud.exited() {
				t.Fatalf("merud exited after the failure\nstderr:\n%s\nlog:\n%s", s.merud.stderr.String(), s.home.log())
			}
		})
	}

	// None of the failed turns wrote an answer line.
	for _, path := range sessionFiles(t, s.home) {
		for _, l := range readTranscript(t, path) {
			if l.Type == transcript.TypeAssistant {
				t.Errorf("%s holds an answer line from a failed turn: %+v", path, l)
			}
		}
	}

	// And merud still answers.
	s.fake.enqueue(t, fastModel, directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "recovered"})
	if res := runMeru(t, s.home, "one more"); res.code != 0 || res.stdout != "recovered\n" {
		t.Errorf("after the failures: exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
}

// TestOllamaDown stops the fake Ollama under a running merud. meru must exit
// 1 with a message that names the Ollama call, and merud must stay up.
func TestOllamaDown(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	s.fake.proc.stop(t)

	res := runMeru(t, s.home, "is anyone there?")
	if res.code != 1 {
		t.Errorf("meru exited %d, want 1; stderr %q", res.code, res.stderr)
	}
	for _, want := range []string{"meru: ", "/api/chat", "connection refused"} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", res.stderr, want)
		}
	}
	if r := runMeru(t, s.home, "ping"); r.code != 0 {
		t.Errorf("meru ping after Ollama went away: exit %d, stderr %q", r.code, r.stderr)
	}
}
