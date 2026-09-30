// This file holds the "Web search" step. The installer no longer runs
// SearXNG itself: merud's SearXNG connector pulls the pinned image, runs
// the container and restarts it. The step points [web] searxng_url at
// Meru's address, turns on web_search and web_fetch, and, unless a
// SearXNG already answers there, asks the Start Meru step to turn the
// connector on through merud (connectors.go). See ARCHITECTURE.md,
// "Installer" and "SearXNG and Ollama".

package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// SearXNGURL is where Meru reaches SearXNG: the default [web] searxng_url,
// and the one address where merud runs its own container.
const SearXNGURL = catalog.SearXNGURL

// DockerDownloadURL is where the screen sends a user who has no Docker.
const DockerDownloadURL = "https://www.docker.com/products/docker-desktop/"

// ErrNoDocker means docker isn't installed. The screen offers the download
// link and Skip.
var ErrNoDocker = errors.New("this Mac has no Docker. Install Docker Desktop, OrbStack or colima, then press Retry, or skip web search for now")

// dockerPaths are where Docker Desktop, OrbStack and colima put the docker
// command. The installer only looks for one, to say early that web search
// needs Docker; merud runs it.
func dockerPaths(home string) []string {
	return []string{
		"/usr/local/bin/docker", "/opt/homebrew/bin/docker", expandHome("~/.orbstack/bin/docker", home),
		expandHome("~/.docker/bin/docker", home), "/Applications/Docker.app/Contents/Resources/bin/docker",
	}
}

// HasDocker reports whether the docker command is at one of dockerPaths.
func HasDocker(home string) bool {
	for _, p := range dockerPaths(home) {
		// os.Stat follows links: Docker's command is a link into its app.
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
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

// SetUpWebSearch runs the web search step. It writes [web] and [builtin]
// in config.toml and returns what it did, with the hand-off for the Start
// Meru step, or nil when there is none.
//
// A SearXNG that already answers JSON at SearXNGURL, such as one the user
// runs with docker compose or the container an older installer started,
// stays as it is: merud watches it and never touches it, so the step asks
// for no hand-off. Otherwise the step needs Docker, which docker says this
// Mac has (HasDocker), and fails with ErrNoDocker without it; merud pulls
// and runs the container once the Start Meru step turns the connector on.
// It runs no program.
func SetUpWebSearch(ctx context.Context, client *http.Client, p Paths, docker bool) (string, *HandOff, error) {
	var done []string
	var hand *HandOff
	if err := VerifySearXNG(ctx, client, SearXNGURL); err == nil {
		done = append(done, "SearXNG already answers JSON at "+SearXNGURL+". merud uses it as it is and never starts, stops or updates it.")
	} else {
		if !docker {
			return "", nil, ErrNoDocker
		}
		on := true
		hand = &HandOff{ID: "searxng", Name: "Web search", Change: &rpc.ConnectorChange{Enabled: &on}}
		done = append(done, "When Meru starts, in the last step, merud downloads SearXNG at the version pinned in this release and runs it in Docker as the container meru-searxng, on "+SearXNGURL+" only.")
	}
	msg, err := saveWebConfig(p)
	if err != nil {
		return "", nil, err
	}
	return strings.Join(append(done, msg), "\n"), hand, nil
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
