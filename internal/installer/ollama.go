// This file holds the "Ollama and the models" step: finding Ollama,
// installing it with Homebrew or from Ollama's own download, starting it,
// and pulling models through Ollama's HTTP API so the screen can show each
// download's progress.

package installer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// OllamaDownloadURL is Ollama's own download of Ollama.app for macOS, a
// zip file. The installer fetches it over HTTPS only when Homebrew is
// missing and the user presses Continue; the screen shows the URL first.
const OllamaDownloadURL = "https://ollama.com/download/Ollama-darwin.zip"

// ollamaStartWait is how long the step waits for Ollama to answer after
// starting it. The app takes a few seconds on its first start.
const ollamaStartWait = 60 * time.Second

// OllamaVersion asks Ollama at baseURL for its version and returns it. It
// fails when Ollama doesn't answer within five seconds.
func OllamaVersion(ctx context.Context, client *http.Client, baseURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var body struct {
		Version string `json:"version"`
	}
	if err := getJSON(ctx, client, strings.TrimSuffix(baseURL, "/")+"/api/version", &body); err != nil {
		return "", fmt.Errorf("no answer from Ollama at %s: %w", baseURL, err)
	}
	return body.Version, nil
}

// OllamaModels returns the models Ollama has, by name, such as
// "nomic-embed-text:latest". It fails when Ollama doesn't answer.
func OllamaModels(ctx context.Context, client *http.Client, baseURL string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, client, strings.TrimSuffix(baseURL, "/")+"/api/tags", &body); err != nil {
		return nil, fmt.Errorf("list Ollama's models: %w", err)
	}
	var names []string
	for _, m := range body.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// HasModel reports whether have, a list from OllamaModels, holds name.
// Ollama adds ":latest" to a name with no tag, so "nomic-embed-text" and
// "nomic-embed-text:latest" match.
func HasModel(have []string, name string) bool {
	return slices.Contains(have, name) || slices.Contains(have, withTag(name))
}

// withTag adds ":latest" to a model name that has no tag. A tag follows
// the last ":" after the last "/", so "hf.co/a/b:Q4" already has one.
func withTag(name string) string {
	last := name[strings.LastIndex(name, "/")+1:]
	if strings.Contains(last, ":") {
		return name
	}
	return name + ":latest"
}

// getJSON sends a GET to url and decodes the JSON answer into v. It fails
// on a network error, a status other than 200, or bad JSON.
func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// OllamaApp returns where Ollama.app is installed, or "" when it isn't in
// either Applications folder.
func OllamaApp(home string) string {
	for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
		app := filepath.Join(dir, "Ollama.app")
		if info, err := os.Stat(app); err == nil && info.IsDir() {
			return app
		}
	}
	return ""
}

// ollamaFromBrew reports whether Homebrew installed the ollama command.
func ollamaFromBrew() bool {
	for _, p := range []string{"/opt/homebrew/bin/ollama", "/usr/local/bin/ollama"} {
		// Lstat looks at the link itself: Homebrew's commands are links
		// into its Cellar folder.
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(p); err == nil && strings.Contains(target, "Cellar") {
				return true
			}
		}
	}
	return false
}

// InstallOllama installs Ollama: with Homebrew when this Mac has it, or
// else by downloading Ollama.app from OllamaDownloadURL with web, unpacking
// it into apps with ditto and checking its signature with codesign. say
// gets each line of news. It returns what it did, and fails when a
// command or the download fails, or the signature doesn't check out.
func InstallOllama(ctx context.Context, run Runner, web *http.Client, home, apps string, say func(string)) (string, error) {
	// The Runner fails with ErrMissing when this Mac has no Homebrew; any
	// other failure is Homebrew's own and stops the step.
	say("Installing Ollama")
	_, err := run(ctx, "brew", []string{"install", "ollama"}, say)
	switch {
	case err == nil:
		return "Installed Ollama with Homebrew: brew install ollama.", nil
	case !errors.Is(err, ErrMissing):
		return "", err
	}

	say("Downloading Ollama.app from " + OllamaDownloadURL)
	zip, err := download(ctx, web, OllamaDownloadURL, say)
	if err != nil {
		return "", err
	}
	defer os.Remove(zip)
	if err := os.MkdirAll(apps, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", apps, err)
	}
	say("Unpacking Ollama.app into " + apps)
	// ditto -x -k unpacks a zip; it keeps the app bundle whole.
	if _, err := run(ctx, "ditto", []string{"-x", "-k", zip, apps}, nil); err != nil {
		return "", err
	}
	app := filepath.Join(apps, "Ollama.app")
	say("Checking Ollama.app's signature")
	// codesign --verify fails when the app was changed after Ollama
	// signed it, such as by a bad download.
	if out, err := run(ctx, "codesign", []string{"--verify", "--deep", "--strict", app}, nil); err != nil {
		_ = os.RemoveAll(app)
		return "", fmt.Errorf("the signature on Ollama.app doesn't check out, so the installer removed it: %s: %w", strings.TrimSpace(out), err)
	}
	return "Downloaded Ollama.app into " + apps + " and checked its signature.", nil
}

// download fetches url to a temporary file and returns its path, telling
// say how far it has got every 10 MB. It fails on a network error or a
// status other than 200.
func download(ctx context.Context, client *http.Client, url string, say func(string)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: answered %s", url, resp.Status)
	}
	f, err := os.CreateTemp("", "meru-ollama-*.zip")
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	counter := &progressWriter{total: resp.ContentLength, say: say}
	// io.Copy writes the body to the file and to counter at once, through
	// io.MultiWriter.
	_, err = io.Copy(io.MultiWriter(f, counter), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	return f.Name(), nil
}

// progressWriter counts bytes as they pass and reports every 10 MB.
type progressWriter struct {
	done, total, next int64
	say               func(string)
}

// Write counts p and never fails, as io.Writer requires of a counter. The
// pointer receiver (*progressWriter) lets Write change the counts.
func (w *progressWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	if w.done >= w.next {
		w.next = w.done + 10<<20
		if w.total > 0 {
			w.say(fmt.Sprintf("Downloaded %s of %s", sizeText(w.done), sizeText(w.total)))
		} else {
			w.say("Downloaded " + sizeText(w.done))
		}
	}
	return len(p), nil
}

// StartOllama starts Ollama when it doesn't answer at baseURL, then waits
// for it to answer. It opens Ollama.app when that is installed, or asks
// Homebrew to run it as a service. It returns Ollama's version, and fails
// when Ollama isn't installed or doesn't answer within a minute.
func StartOllama(ctx context.Context, run Runner, client *http.Client, home, baseURL string, say func(string)) (string, error) {
	if v, err := OllamaVersion(ctx, client, baseURL); err == nil {
		return v, nil
	}
	switch app := OllamaApp(home); {
	case app != "":
		say("Starting Ollama.app")
		// open -g starts the app without bringing it to the front.
		if _, err := run(ctx, "open", []string{"-g", app}, nil); err != nil {
			return "", err
		}
	case ollamaFromBrew():
		say("Starting Ollama with Homebrew: brew services start ollama")
		if _, err := run(ctx, "brew", []string{"services", "start", "ollama"}, say); err != nil {
			return "", err
		}
	default:
		return "", errors.New("no Ollama is installed; press Retry to install it")
	}
	return waitFor(ctx, ollamaStartWait, func() (string, error) {
		return OllamaVersion(ctx, client, baseURL)
	})
}

// waitFor calls check once a second until it succeeds or limit passes, and
// returns its last result.
func waitFor(ctx context.Context, limit time.Duration, check func() (string, error)) (string, error) {
	deadline := time.Now().Add(limit)
	for {
		v, err := check()
		if err == nil || time.Now().After(deadline) {
			return v, err
		}
		// select waits for whichever comes first: the context ending or
		// one second passing.
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Pull is how far one model's download has got, for the screen.
type Pull struct {
	Model     string `json:"model"`
	Status    string `json:"status"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
}

// Text writes p as one line, such as "gemma3:12b: 1.2 GB of 8.1 GB".
func (p Pull) Text() string {
	if p.Total > 0 {
		return fmt.Sprintf("%s: %s of %s", p.Model, sizeText(p.Completed), sizeText(p.Total))
	}
	return p.Model + ": " + p.Status
}

// PullModel asks Ollama at baseURL to download model with POST /api/pull
// and calls report with the progress after each line Ollama sends. It
// fails when Ollama refuses or reports an error, such as a model name it
// doesn't know. The download has no time limit: a large model on a slow
// line takes an hour.
func PullModel(ctx context.Context, client *http.Client, baseURL, model string, report func(Pull)) error {
	body, err := json.Marshal(map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("pull %s: %w", model, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pull %s: Ollama answered %s: %s", model, resp.Status, strings.TrimSpace(string(msg)))
	}
	return readPull(resp.Body, model, report)
}

// pullLine is one line of Ollama's pull stream. Each part of the model,
// a "layer", has a digest and its own total and completed counts.
type pullLine struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// readPull reads Ollama's pull stream from r, one JSON object per line
// (NDJSON), and reports the progress over all layers after each line. It
// fails on an "error" line, on a line that isn't JSON, and when the stream
// ends without "success".
func readPull(r io.Reader, model string, report func(Pull)) error {
	type layer struct{ completed, total int64 }
	layers := map[string]layer{}
	var order []string // digests in the order they appeared
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var l pullLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return fmt.Errorf("pull %s: Ollama sent a line that isn't JSON: %w", model, err)
		}
		if l.Error != "" {
			return fmt.Errorf("pull %s: %s", model, l.Error)
		}
		if l.Digest != "" && l.Total > 0 {
			if _, seen := layers[l.Digest]; !seen {
				order = append(order, l.Digest)
			}
			layers[l.Digest] = layer{completed: l.Completed, total: l.Total}
		}
		p := Pull{Model: model, Status: l.Status}
		for _, d := range order {
			p.Completed += layers[d].completed
			p.Total += layers[d].total
		}
		report(p)
		if l.Status == "success" {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("pull %s: %w", model, err)
	}
	return fmt.Errorf("pull %s: Ollama stopped before it said success", model)
}

// sizeText writes n bytes as "812 MB" or "8.1 GB", in the units Ollama
// uses: 1 GB is 1000 MB.
func sizeText(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%d MB", n/1e6)
	default:
		return fmt.Sprintf("%d KB", n/1e3)
	}
}
