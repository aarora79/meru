// This file holds the "Web search" step: it writes a settings.yml for
// SearXNG (catalog.WriteSearXNGSettings, which merud's SearXNG connector
// uses too), pulls and starts SearXNG's container with docker, bound to
// 127.0.0.1 only, checks that it answers JSON, and points [web]
// searxng_url at it. See ARCHITECTURE.md, "Web search" and "Installer".

package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// The container the step runs, and where it answers.
const (
	// SearXNGImage is SearXNG's own image on Docker Hub. It has no fixed
	// version, so a second run of the installer brings security fixes.
	SearXNGImage = "docker.io/searxng/searxng:latest"
	// SearXNGContainer is the container's name, so a second run finds it.
	// catalog holds it, since merud's SearXNG connector uses the same one.
	SearXNGContainer = catalog.SearXNGContainer
	// SearXNGURL is where Meru reaches it: the default [web] searxng_url.
	SearXNGURL = catalog.SearXNGURL
	// searxngPublish maps port 8888 on this Mac's loopback address to
	// port 8080 in the container. Docker listens on 127.0.0.1 only, so no
	// other machine on the network can use this SearXNG.
	searxngPublish = "127.0.0.1:8888:8080"
)

// DockerDownloadURL is where the screen sends a user who has no Docker.
const DockerDownloadURL = "https://www.docker.com/products/docker-desktop/"

// searxngStartWait is how long the step waits for SearXNG to answer after
// the container starts. Its first start takes several seconds.
const searxngStartWait = 90 * time.Second

// ErrNoDocker means docker isn't installed. The screen offers the download
// link and Skip.
var ErrNoDocker = errors.New("this Mac has no Docker. Install Docker Desktop, OrbStack or colima, then press Retry, or skip web search for now")

// DockerRunArgs returns the arguments for docker that start SearXNG, with
// settingsDir mounted as its settings folder. Each is a separate string,
// and the Runner passes them to docker with no shell.
func DockerRunArgs(settingsDir string) []string {
	return []string{
		"run", "--detach",
		"--name", SearXNGContainer,
		// Docker starts the container again after a restart of the Mac or
		// of Docker, unless the user stopped it.
		"--restart", "unless-stopped",
		"--publish", searxngPublish,
		"--volume", settingsDir + ":/etc/searxng",
		"--env", "SEARXNG_BASE_URL=" + SearXNGURL + "/",
		SearXNGImage,
	}
}

// containerState asks docker whether the SearXNG container exists and
// runs. It returns "running", "stopped" or "" for no such container.
func containerState(ctx context.Context, run Runner) string {
	out, err := run(ctx, "docker", []string{"inspect", "--format", "{{.State.Running}}", SearXNGContainer}, nil)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(out) == "true" {
		return "running"
	}
	return "stopped"
}

// VerifySearXNG sends one search, "test", to SearXNG at baseURL with
// format=json, and returns nil when a JSON object with a results list
// comes back. It fails when nothing answers, when SearXNG answers HTML
// because JSON is off, or on any other answer.
func VerifySearXNG(ctx context.Context, client *http.Client, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := strings.TrimSuffix(baseURL, "/") + "/search?q=test&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w on %s", catalog.ErrSearXNGDown, baseURL)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read SearXNG's answer: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(body)), "<") {
		return fmt.Errorf("%w on %s", catalog.ErrSearXNGNoJSON, baseURL)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SearXNG on %s answered %s", baseURL, resp.Status)
	}
	var parsed struct {
		Results *[]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Results == nil {
		return fmt.Errorf("SearXNG on %s answered JSON with no results list", baseURL)
	}
	return nil
}

// SetUpWebSearch runs the web search step and returns what it did. say
// gets each line of news, including every line docker prints while it
// downloads the image.
//
// When SearXNG already answers JSON at SearXNGURL, such as one the user
// started with docker compose, it only writes config. Otherwise it needs
// docker, and fails with ErrNoDocker without it, or when Docker isn't
// running, or when the container doesn't answer within 90 seconds.
func SetUpWebSearch(ctx context.Context, run Runner, client *http.Client, p Paths, say func(string)) (string, error) {
	var done []string
	if err := VerifySearXNG(ctx, client, SearXNGURL); err == nil {
		done = append(done, "SearXNG already answers JSON at "+SearXNGURL+", so the installer left it as it is.")
	} else {
		say("Checking that Docker runs")
		// docker version asks the Docker engine for its version, so it
		// fails when the engine isn't running. The Runner fails with
		// ErrMissing when docker isn't installed at all.
		if _, err := run(ctx, "docker", []string{"version", "--format", "{{.Server.Version}}"}, nil); err != nil {
			if errors.Is(err, ErrMissing) {
				return "", ErrNoDocker
			}
			return "", errors.New("the Docker engine isn't running. Open Docker Desktop, OrbStack or colima, wait until it says it runs, then press Retry")
		}
		wrote, err := catalog.WriteSearXNGSettings(p.SearXNG())
		if err != nil {
			return "", err
		}
		settings := p.Tilde(filepath.Join(p.SearXNG(), "settings.yml"))
		if wrote {
			done = append(done, "Wrote SearXNG's settings to "+settings+", with JSON on and a new secret key.")
		} else {
			done = append(done, "Kept SearXNG's settings in "+settings+".")
		}

		switch containerState(ctx, run) {
		case "running":
			say("The " + SearXNGContainer + " container already runs")
		case "stopped":
			say("Starting the " + SearXNGContainer + " container")
			if _, err := run(ctx, "docker", []string{"start", SearXNGContainer}, say); err != nil {
				return "", err
			}
		default:
			say("Downloading the web search container…")
			if _, err := run(ctx, "docker", []string{"pull", SearXNGImage}, say); err != nil {
				return "", err
			}
			say("Starting it…")
			if _, err := run(ctx, "docker", DockerRunArgs(p.SearXNG()), say); err != nil {
				return "", err
			}
			done = append(done, "Started SearXNG in the container "+SearXNGContainer+", on "+SearXNGURL+" only. Docker starts it again after a restart.")
		}

		say("Waiting for SearXNG to answer a test search")
		if _, err := waitFor(ctx, searxngStartWait, func() (string, error) {
			return "", VerifySearXNG(ctx, client, SearXNGURL)
		}); err != nil {
			return "", fmt.Errorf("SearXNG didn't answer a test search: %w. Its log: docker logs %s", err, SearXNGContainer)
		}
		done = append(done, "SearXNG answered a test search.")
	}

	msg, err := saveWebConfig(p)
	if err != nil {
		return "", err
	}
	return strings.Join(append(done, msg), "\n"), nil
}

// webTools are the two built-in tools web search needs on.
func webTools() []string { return []string{"web_search", "web_fetch"} }

// saveWebConfig sets [web] searxng_url and makes sure [builtin] tools
// lists web_search and web_fetch, and returns what it did.
func saveWebConfig(p Paths) (string, error) {
	cfg, err := loadConfig(p.Config())
	if err != nil {
		return "", err
	}
	if cfg.Web.SearXNGURL != SearXNGURL {
		if err := catalog.SetTableString(p.Config(), "web", "searxng_url", SearXNGURL, nil); err != nil {
			return "", err
		}
	}
	tools := slices.Clone(cfg.Builtin.Tools)
	for _, t := range webTools() {
		if !slices.Contains(tools, t) {
			tools = append(tools, t)
		}
	}
	if len(tools) != len(cfg.Builtin.Tools) {
		err := catalog.SetTableLists(p.Config(), "builtin", map[string][]string{"tools": tools}, func(c config.Config) error {
			if !slices.Equal(c.Builtin.Tools, tools) {
				return fmt.Errorf("[builtin] tools came out as %v", c.Builtin.Tools)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return "Set [web] searxng_url = \"" + SearXNGURL + "\" and turned on web_search and web_fetch in " + p.Tilde(p.Config()) + ".", nil
}
