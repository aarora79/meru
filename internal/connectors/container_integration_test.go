//go:build integration

// This file holds the test that runs the real SearXNG connector under
// each container runtime Meru supports: Docker (Docker Desktop on a Mac)
// and Podman. For each one it points the Container supervisor at that
// runtime's command alone, lets it pull the pinned image and start
// Meru's container, runs one search, stops the container and checks the
// supervisor starts the same one again, then turns the connector off and
// checks the container stopped. It runs only with the integration build
// tag:
//
//	go test -tags integration -run TestIntegrationContainer -v ./internal/connectors/
//
// A runtime that isn't installed or isn't running is skipped, and -v
// prints why. CI never runs it, since it needs a runtime and the
// internet. The test's container is named meru-searxng_itest and listens
// on 127.0.0.1:18888, so it leaves a SearXNG on 8888, Meru's or the
// user's own, alone.

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/platform"
)

// itestURL is where the test's SearXNG answers: not 8888, which a real
// Meru or the user's own SearXNG may hold.
const itestURL = "http://127.0.0.1:18888"

// TestIntegrationContainer runs the SearXNG connector once under Docker
// and once under Podman.
func TestIntegrationContainer(t *testing.T) {
	for _, runtimeName := range []string{"docker", "podman"} {
		t.Run(runtimeName, func(t *testing.T) { runContainerConnector(t, runtimeName) })
	}
}

// runContainerConnector runs the whole connector under the one runtime
// whose command is named runtimeName.
func runContainerConnector(t *testing.T, runtimeName string) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// Home stays the real one: docker and podman keep their settings, and
	// the address of the engine they talk to, under it.
	meruDir := filepath.Join(t.TempDir(), ".meru")
	in := NewInstaller(meruDir, home)
	// Keep only this runtime's paths, so the supervisor can't fall back
	// to the other one. slices.DeleteFunc drops each path the function
	// returns true for.
	in.DockerPaths = slices.DeleteFunc(dockerPaths(platform.Current().ProgramDirs), func(p string) bool {
		return filepath.Base(p) != runtimeName
	})
	bin, err := in.findDocker()
	if err != nil {
		t.Skipf("%s isn't installed in any of %v", runtimeName, in.DockerPaths)
	}

	m := itestManifest(t)
	settings := filepath.Join(meruDir, "searxng")
	// prepare writes settings.yml with JSON on, as merud's does.
	prepare := func() error {
		_, err := catalog.WriteSearXNGSettings(settings)
		return err
	}
	c, err := NewContainer(m, in, checkSearXNG, prepare, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := c.dockerReady(ctx); err != nil {
		t.Skipf("%s at %s doesn't answer: %v", runtimeName, bin, err)
	}
	if err := catalog.CheckSearXNG(ctx, itestURL); !errors.Is(err, catalog.ErrSearXNGDown) {
		t.Fatalf("something already answers on %s (%v); stop it and run the test again", itestURL, err)
	}
	// A container left by an earlier run that stopped part way would
	// make docker run fail on the name, so remove it before and after.
	// rm fails when there is none, which is fine.
	removeIt := func() { _, _ = c.docker(context.Background(), bin, "rm", "--force", c.name) }
	removeIt()
	// t.Cleanup runs its functions when the test ends, last added first:
	// Close stops the supervisor, then the container goes.
	t.Cleanup(removeIt)
	t.Cleanup(c.Close)

	on := config.Connector{"enabled": true}
	c.Configure(on, itestURL, true)
	// The first run pulls the image, about 100 MB.
	waitContainer(t, c, StateOK, 10*time.Minute)

	info, err := c.inspect(ctx, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !info.running || info.label != m.ID || !sameImage(info.image, m.Install.Image) {
		t.Fatalf("%s inspect = %+v, want running, label %s, image %s", runtimeName, info, m.ID, m.Install.Image)
	}
	t.Logf("%s ran %s: %s", bin, c.name, c.Status().Sentence)

	n := search(t, itestURL+"/search?q=golang&format=json")
	t.Logf("a search through %s's container gave %d results", runtimeName, n)

	// Stop the container behind the supervisor's back, as a restart of
	// the computer does (it runs with --restart no). Recheck must start
	// the same container again, not remove it and run a new one.
	before := containerID(t, c, bin)
	if _, err := c.docker(ctx, bin, "stop", c.name); err != nil {
		t.Fatal(err)
	}
	c.Recheck()
	waitContainer(t, c, StateOK, 2*time.Minute)
	if after := containerID(t, c, bin); after != before {
		t.Fatalf("%s replaced the container (%.12s, then %.12s); want the same one started again", runtimeName, before, after)
	}
	search(t, itestURL+"/search?q=golang&format=json")

	c.Configure(config.Connector{"enabled": false}, itestURL, true)
	waitContainer(t, c, StateOff, time.Minute)
	deadline := time.Now().Add(time.Minute)
	for {
		info, err := c.inspect(ctx, bin)
		if err != nil {
			t.Fatal(err)
		}
		if !info.running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still runs %s a minute after the connector went off", runtimeName, c.name)
		}
		time.Sleep(time.Second)
	}
}

// containerID returns the ID of the container named c.name.
func containerID(t *testing.T, c *Container, bin string) string {
	t.Helper()
	out, err := c.docker(context.Background(), bin, "inspect", "--format", "{{.Id}}", c.name)
	if err != nil {
		t.Fatal(err)
	}
	return lastLine(out)
}

// itestManifest returns the shipped SearXNG manifest with its own ID and
// address. The ID names the container (meru-searxng_itest) and its
// label, so the test never touches a real Meru's meru-searxng.
func itestManifest(t *testing.T) Manifest {
	t.Helper()
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m Manifest) bool { return m.ID == "searxng" })
	if i < 0 {
		t.Fatal("no searxng manifest")
	}
	m := ms[i]
	m.ID = "searxng_itest"
	m.Launch.URL = itestURL
	// A new map, so the change stays out of the manifest Load returned.
	m.Launch.Env = map[string]string{"SEARXNG_BASE_URL": itestURL + "/"}
	return m
}

// checkSearXNG is merud's check (cmd/merud/connectors.go) in short: a
// port where nothing listens reads as ErrNothingListens, which tells the
// supervisor it may start its container there.
func checkSearXNG(ctx context.Context, url string) error {
	err := catalog.CheckSearXNG(ctx, url)
	if errors.Is(err, catalog.ErrSearXNGDown) {
		return ErrNothingListens
	}
	return err
}

// waitContainer waits up to limit for the container to reach want, and
// fails at once when it reaches failed or needs_config instead.
func waitContainer(t *testing.T, c *Container, want string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		st := c.Status()
		switch {
		case st.State == want:
			return
		case st.State == StateFailed, st.State == StateNeedsConfig:
			t.Fatalf("state %s: %s", st.State, st.Sentence)
		case time.Now().After(deadline):
			t.Fatalf("still %s after %s: %s", st.State, limit, st.Sentence)
		}
		time.Sleep(time.Second)
	}
}

// search runs one real search and returns the number of results. It
// fails unless SearXNG answers 200 with a JSON object that has a results
// list; some search engines may give nothing, so zero results pass.
func search(t *testing.T, u string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results *[]json.RawMessage `json:"results"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &got) != nil || got.Results == nil {
		t.Fatalf("search gave %d %.200s", resp.StatusCode, body)
	}
	return len(*got.Results)
}
