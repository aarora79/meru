// This file tests the Container supervisor with a fake docker and a fake
// health check: the mode config picks, pulling and starting Meru's
// container, a start that never passes its check, a container that stops
// answering, Docker missing or stopped, and the external-server rule,
// under which Meru runs no docker command that changes anything.

package connectors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// searxngManifest returns the embedded searxng manifest.
func searxngManifest(t *testing.T) Manifest {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == "searxng" {
			return m
		}
	}
	t.Fatal("no searxng manifest")
	return Manifest{}
}

// TestContainerMode checks which mode each config gives.
func TestContainerMode(t *testing.T) {
	m := searxngManifest(t)
	const own, other = "http://127.0.0.1:8888", "http://127.0.0.1:9999"
	tests := []struct {
		name       string
		table      config.Connector
		url        string
		toolListed bool
		want       Mode
	}{
		{"no table, tool listed: watch, as before connectors", nil, own, true, ModeWatch},
		{"no table, tool left out", nil, own, false, ModeOff},
		{"no URL", config.Connector{"enabled": true}, "", true, ModeOff},
		{"turned off", config.Connector{"enabled": false}, own, true, ModeOff},
		{"turned on at Meru's address", config.Connector{"enabled": true}, own, false, ModeRun},
		{"turned on, trailing slash", config.Connector{"enabled": true}, own + "/", true, ModeRun},
		{"turned on at another address", config.Connector{"enabled": true}, other, true, ModeWatch},
		{"table without enabled", config.Connector{}, own, true, ModeWatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainerMode(m, tt.table, tt.url, tt.toolListed); got != tt.want {
				t.Errorf("ContainerMode = %d, want %d", got, tt.want)
			}
		})
	}
}

// fakeDocker plays docker and the server behind the URL. It keeps one
// container's state, answers info, pull, inspect, run, start, restart,
// stop and rm, and records each command.
type fakeDocker struct {
	mu sync.Mutex // guards every field below
	// down makes info fail, as when Docker isn't running.
	down bool
	// the container: exists, running, its label and image.
	exists, running bool
	label, image    string
	// serves says whether the URL answers well. startServes says whether
	// a started container makes it do so.
	serves, startServes bool
	// listening says whether something answers at the URL at all when
	// serves is false.
	listening bool
	calls     []string
}

// run is the fake Runner.
func (d *fakeDocker) run(_ context.Context, c Cmd, _ func(string)) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, strings.Join(c.Args, " "))
	fail := func(msg string) (string, error) { return msg, errors.New("exit status 1") }
	switch c.Args[0] {
	case "info":
		if d.down {
			return fail("Cannot connect to the Docker daemon")
		}
		return "29.0.0", nil
	case "pull":
		return "Status: Downloaded", nil
	case "inspect":
		if !d.exists {
			return fail("error: no such object: " + c.Args[len(c.Args)-1])
		}
		return fmt.Sprintf("%v|%s|%s", d.running, d.label, d.image), nil
	case "run":
		if d.exists {
			return fail("Conflict. The container name is already in use")
		}
		d.exists, d.running = true, true
		for i, a := range c.Args {
			if a == "--label" {
				d.label = strings.TrimPrefix(c.Args[i+1], labelKey+"=")
			}
		}
		d.image = c.Args[len(c.Args)-1]
		d.serves = d.startServes
	case "start", "restart":
		d.running = true
		d.serves = d.startServes
	case "stop":
		d.running, d.serves = false, false
	case "rm":
		d.exists, d.running, d.serves = false, false, false
	}
	return "", nil
}

// check is the fake health check.
func (d *fakeDocker) check(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.serves:
		return nil
	case d.listening:
		return errors.New("it answers web pages, not JSON")
	}
	return fmt.Errorf("SearXNG isn't answering: %w", ErrNothingListens)
}

// set changes the fake under its lock.
func (d *fakeDocker) set(f func(d *fakeDocker)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

// commands returns the docker commands so far, by their first word.
func (d *fakeDocker) commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		first, _, _ := strings.Cut(c, " ")
		out = append(out, first)
	}
	return out
}

// changing lists the docker commands that change a container or an
// image: the ones the external rule forbids.
var changing = []string{"pull", "run", "start", "restart", "stop", "rm"}

// testContainer builds a Container over d, with a docker at a real file
// unless noDocker, and a fake clock.
func testContainer(t *testing.T, d *fakeDocker, noDocker bool) (*Container, *fakeClock, *int) {
	t.Helper()
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in := &Installer{MeruDir: filepath.Join(dir, "meru"), Home: dir, Run: d.run, DockerPaths: []string{docker}}
	if noDocker {
		in.DockerPaths = nil
	}
	prepared := 0
	c, err := NewContainer(searxngManifest(t), in, d.check, func() error { prepared++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	c.clock = clock
	c.startWait = 0 // one check after a start, then a crash
	t.Cleanup(c.Close)
	return c, clock, &prepared
}

// runTable turns the connector on, so Meru runs its own container.
var runTable = config.Connector{"enabled": true}

const searxURL = "http://127.0.0.1:8888"

// waitStatus waits up to 5 seconds for the container's state to be want
// and its sentence to hold part, and returns the sentence.
func waitStatus(t *testing.T, c *Container, want, part string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := c.Status()
		if st.State == want && strings.Contains(st.Sentence, part) {
			return st.Sentence
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s %q, want %s with %q", st.State, st.Sentence, want, part)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// pending counts the timers that haven't fired or stopped.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.ended {
			n++
		}
	}
	return n
}

// advance waits for the loop to sleep on the clock, then moves the clock
// on by d.
func advance(t *testing.T, clock *fakeClock, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for clock.pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the loop never waited on the clock")
		}
		time.Sleep(time.Millisecond)
	}
	clock.Advance(d)
}

// TestContainerStartsMeruContainer: nothing answers, so the supervisor
// pulls the pinned image, writes the settings, runs Meru's container with
// its label, loopback port and no restart policy, and reports ok.
func TestContainerStartsMeruContainer(t *testing.T) {
	d := &fakeDocker{startServes: true}
	c, _, prepared := testContainer(t, d, false)
	c.Configure(runTable, searxURL, true)
	waitStatus(t, c, StateOK, "Web search is running in the container meru-searxng.")
	if ok, _ := c.OK(); !ok {
		t.Error("OK = false for a running container")
	}
	if *prepared != 1 {
		t.Errorf("prepare ran %d times, want 1", *prepared)
	}
	if got := d.commands(); !slices.Contains(got, "pull") || !slices.Contains(got, "run") {
		t.Errorf("docker commands = %v, want a pull and a run", got)
	}
	var run string
	for _, call := range d.calls {
		if strings.HasPrefix(call, "run ") {
			run = call
		}
	}
	m := searxngManifest(t)
	for _, want := range []string{
		"--name meru-searxng", "--label meru.connector=searxng", "--restart no",
		"--publish 127.0.0.1:8888:8080", "/meru/searxng:/etc/searxng",
		"--env SEARXNG_BASE_URL=http://127.0.0.1:8888/", m.Install.Image,
	} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run lacks %q:\n%s", want, run)
		}
	}
}

// TestContainerStartFailsBacksOff: a container that never passes its
// check counts a crash each time, waits 1, 2, 4 and 8 seconds between
// starts, and fails at the fifth.
func TestContainerStartFailsBacksOff(t *testing.T) {
	d := &fakeDocker{startServes: false}
	c, clock, _ := testContainer(t, d, false)
	c.Configure(runTable, searxURL, true)
	waitStatus(t, c, StateStarting, "starts again in 1 s")
	for i, wait := range []time.Duration{1, 2, 4, 8} {
		advance(t, clock, wait*time.Second)
		if i < 3 {
			waitStatus(t, c, StateStarting, fmt.Sprintf("starts again in %d s", wait*2))
		}
	}
	sentence := waitStatus(t, c, StateFailed, "Web search keeps stopping: didn't pass its check")
	if ok, _ := c.OK(); ok {
		t.Errorf("OK = true while failed: %s", sentence)
	}
	// The second and later starts restart the container that exists.
	if n := strings.Count(strings.Join(d.commands(), " "), "restart"); n != 4 {
		t.Errorf("docker restarted %d times, want 4: %v", n, d.commands())
	}
}

// TestContainerRestartsAfterItStops: Meru's container stops answering at
// a minute's check; the supervisor waits a second and starts it again.
func TestContainerRestartsAfterItStops(t *testing.T) {
	d := &fakeDocker{startServes: true}
	c, clock, _ := testContainer(t, d, false)
	c.Configure(runTable, searxURL, true)
	waitStatus(t, c, StateOK, "running in the container")

	d.set(func(d *fakeDocker) { d.running, d.serves = false, false })
	advance(t, clock, containerCheckEvery)
	waitStatus(t, c, StateStarting, "Web search stopped (SearXNG isn't answering: nothing answers there) and starts again in 1 s.")
	advance(t, clock, time.Second)
	waitStatus(t, c, StateOK, "running in the container")
	if got := d.commands(); got[len(got)-1] != "start" {
		t.Errorf("last docker command = %v, want start", got)
	}
}

// TestContainerDocker covers Docker missing and Docker not running: each
// is needs_config with its own sentence, and nothing is pulled or run.
func TestContainerDocker(t *testing.T) {
	tests := []struct {
		name     string
		noDocker bool
		down     bool
		want     string
	}{
		{"missing", true, false, "Web search needs Docker, which isn't installed."},
		{"not running", false, true, "Web search can't start: Docker isn't running."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDocker{down: tt.down, startServes: true}
			c, clock, _ := testContainer(t, d, tt.noDocker)
			c.Configure(runTable, searxURL, true)
			waitStatus(t, c, StateNeedsConfig, tt.want)
			for _, cmd := range d.commands() {
				if slices.Contains(changing, cmd) {
					t.Errorf("docker %s ran without a working Docker", cmd)
				}
			}
			// Docker starts: the next minute's check runs the container.
			d.set(func(d *fakeDocker) { d.down = false })
			if tt.noDocker {
				return
			}
			advance(t, clock, containerCheckEvery)
			waitStatus(t, c, StateOK, "running in the container")
		})
	}
}

// TestContainerExternal is the external-server rule: whatever answers at
// the URL and isn't Meru's container is reported and never touched, in
// run mode as in watch mode, working or not, and a container of Meru's
// name without Meru's label is left alone too.
func TestContainerExternal(t *testing.T) {
	tests := []struct {
		name  string
		table config.Connector
		setup func(d *fakeDocker)
		state string
		want  string
	}{
		{"healthy, run mode", runTable, func(d *fakeDocker) { d.serves = true },
			StateOK, "Web search uses the SearXNG already running at http://127.0.0.1:8888."},
		{"healthy installer container, run mode", runTable, func(d *fakeDocker) {
			d.serves, d.exists, d.running, d.image = true, true, true, "docker.io/searxng/searxng:latest"
		}, StateOK, "uses the SearXNG already running"},
		{"healthy, watch mode", nil, func(d *fakeDocker) { d.serves = true },
			StateOK, "uses the SearXNG already running"},
		{"answers badly, run mode", runTable, func(d *fakeDocker) { d.listening = true },
			StateNeedsConfig, "Web search can't use SearXNG at http://127.0.0.1:8888: it answers web pages, not JSON."},
		{"nothing answers, watch mode", nil, func(*fakeDocker) {},
			StateNeedsConfig, "To have Meru run it, set enabled = true under [connectors.searxng]"},
		{"foreign container holds the name", runTable, func(d *fakeDocker) {
			d.exists, d.image = true, "docker.io/searxng/searxng:latest"
		}, StateNeedsConfig, "a container named meru-searxng that Meru didn't start holds the name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDocker{startServes: true}
			d.set(tt.setup)
			c, _, prepared := testContainer(t, d, false)
			c.Configure(tt.table, searxURL, true)
			waitStatus(t, c, tt.state, tt.want)
			for _, cmd := range d.commands() {
				if slices.Contains(changing, cmd) {
					t.Errorf("docker %s ran against a server Meru doesn't own: %v", cmd, d.commands())
				}
			}
			if *prepared != 0 {
				t.Error("prepare wrote settings for a server Meru doesn't own")
			}
			if tt.table == nil && len(d.commands()) != 0 {
				t.Errorf("watch mode ran docker: %v", d.commands())
			}
		})
	}
}

// TestContainerOffStopsOurs: turning the connector off stops Meru's own
// container, and turning it off with another server there stops nothing.
func TestContainerOffStopsOurs(t *testing.T) {
	d := &fakeDocker{startServes: true}
	c, _, _ := testContainer(t, d, false)
	c.Configure(runTable, searxURL, true)
	waitStatus(t, c, StateOK, "running in the container")
	c.Configure(config.Connector{"enabled": false}, searxURL, true)
	waitStatus(t, c, StateOff, "Web search is off.")
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(d.commands(), "stop") {
		if time.Now().After(deadline) {
			t.Fatalf("Meru's container wasn't stopped: %v", d.commands())
		}
		time.Sleep(5 * time.Millisecond)
	}

	d2 := &fakeDocker{serves: true, exists: true, running: true, image: "other"}
	c2, _, _ := testContainer(t, d2, false)
	c2.Configure(runTable, searxURL, true)
	waitStatus(t, c2, StateOK, "already running")
	c2.Configure(config.Connector{"enabled": false}, searxURL, true)
	waitStatus(t, c2, StateOff, "is off")
	c2.Close()
	if slices.Contains(d2.commands(), "stop") {
		t.Errorf("turning off stopped a container Meru doesn't own: %v", d2.commands())
	}
}

// TestContainerUnknownKey: a key the manifest doesn't know is a
// needs_config sentence, and nothing is checked or run.
func TestContainerUnknownKey(t *testing.T) {
	d := &fakeDocker{startServes: true}
	c, _, _ := testContainer(t, d, false)
	c.Configure(config.Connector{"enabled": true, "port": "9"}, searxURL, true)
	waitStatus(t, c, StateNeedsConfig, "Web search has no setting called port")
	if len(d.commands()) != 0 {
		t.Errorf("docker ran: %v", d.commands())
	}
}
