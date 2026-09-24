//go:build e2e

// This file checks v0.2's "done when" with the real binaries: merud indexes
// a notes folder, answers a question about a note with the note's text in
// the prompt, and meru prints the answer with a Sources list naming the
// file. It also checks `meru index`, the watcher picking up a changed and a
// deleted file, and that a secret file never reaches the index.

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// The distinctive fact the test asks about, and the question. The other
// notes share no word with the question, so keyword search ranks the
// garden note first and it becomes excerpt [1].
const (
	gardenFact     = "The Q3 budget for the garden project is 4,200 dollars."
	gardenQuestion = "What is the Q3 budget for the garden project?"
	secretValue    = "PRIVATE-MARKER-NUMBER-NINE" // low entropy, so secret scanners see a marker, not a key
)

// indexTimeout bounds each wait for the indexer: the startup scan, the
// watcher's half-second debounce, and the race detector's slowdown.
const indexTimeout = 20 * time.Second

// searchRoute is a router reply that picks "search" with confidence 0.9.
func searchRoute() fakeollama.Reply {
	return routeReply(letter{"B", 0.9}, letter{"A", 0.05}, letter{"C", 0.03}, letter{"D", 0.02})
}

// notesFolder makes a notes folder with a few Markdown files, an empty
// file and a secret .env file, and returns its path with symlinks resolved,
// the form merud stores.
func notesFolder(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru-notes-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"garden.md":  "# Garden\n\n## Budget\n\n" + gardenFact + "\n",
		"recipes.md": "# Recipes\n\nPancakes need eggs, milk and flour.\n",
		"travel.md":  "# Travel\n\nFlight to Lisbon departs Tuesday morning.\n",
		"empty.md":   "",
		".env":       "GARDEN_API_KEY=" + secretValue + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// startIndexStack starts a fake Ollama and a merud that indexes notes.
func startIndexStack(t *testing.T, notes string) *stack {
	t.Helper()
	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, fakeConfig(f.url, fmt.Sprintf("[router]\ntemperature = 1.0\n\n[index]\nfolders = [%q]\n", notes)))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)
	return &stack{home: h, fake: f, merud: m}
}

// indexStatus runs `meru index -status` and returns its output.
func indexStatus(t *testing.T, h *home) string {
	t.Helper()
	res := runMeru(t, h, "index", "-status")
	if res.code != 0 {
		t.Fatalf("meru index -status exited %d: %s", res.code, res.stderr)
	}
	return res.stdout
}

// waitForStatus polls `meru index -status` until its output holds every
// one of want.
func waitForStatus(t *testing.T, h *home, want ...string) {
	t.Helper()
	waitFor(t, indexTimeout, fmt.Sprintf("index status with %q", want), func() bool {
		out := indexStatus(t, h)
		for _, w := range want {
			if !strings.Contains(out, w) {
				return false
			}
		}
		return true
	})
}

// lastMainPrompt returns the body of the last chat request the main model
// received, as text, or "" when there was none.
func lastMainPrompt(t *testing.T, f *fake) string {
	t.Helper()
	reqs := f.chatRequests(t, mainModel)
	if len(reqs) == 0 {
		return ""
	}
	// The body is JSON; decoding it turns escapes such as \u003c back into
	// the characters merud sent.
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
		t.Fatalf("decode chat request: %v", err)
	}
	var b strings.Builder
	for _, m := range body.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func TestAnswerCitesLocalNote(t *testing.T) {
	t.Parallel()
	notes := notesFolder(t)
	s := startIndexStack(t, notes)

	// Four files: the three notes and the empty one. The .env file is a
	// secret and never counts.
	waitForStatus(t, s.home, "4 files", "Scanning:   no")

	const answer = "The Q3 budget for the garden project is 4,200 dollars [1]."
	s.fake.enqueue(t, fastModel, searchRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: answer})
	res := runMeru(t, s.home, gardenQuestion)
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s\nlog:\n%s", res.code, res.stderr, s.home.log())
	}

	// The note's text reached the model, under the citation rule.
	prompt := lastMainPrompt(t, s.fake)
	for _, want := range []string{"From your files", "[1] " + filepath.Join(notes, "garden.md"), gardenFact, "Never invent"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("main model's prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, secretValue) {
		t.Errorf("the secret from .env reached the prompt:\n%s", prompt)
	}

	// meru prints the answer, then the file it cites, and only that file.
	wantOut := answer + "\n\nSources:\n[1] " + filepath.Join(notes, "garden.md") + `, "Garden > Budget"`
	if !strings.HasPrefix(res.stdout, wantOut) {
		t.Errorf("stdout = %q\nwant it to start with %q", res.stdout, wantOut)
	}
	if strings.Contains(res.stdout, "recipes.md") || strings.Contains(res.stdout, "travel.md") {
		t.Errorf("Sources lists files the answer doesn't cite:\n%s", res.stdout)
	}

	status := indexStatus(t, s.home)
	if !strings.Contains(status, "Folders:    "+notes) || !strings.Contains(status, "Index:      4 files, 3 chunks, 3 vectors") {
		t.Errorf("status =\n%s", status)
	}
}

func TestIndexFollowsChanges(t *testing.T) {
	t.Parallel()
	notes := notesFolder(t)
	s := startIndexStack(t, notes)
	waitForStatus(t, s.home, "4 files", "Scanning:   no")

	// Change the garden note: the watcher re-indexes it, and the next
	// search puts the new text in the prompt. Each try asks again; the
	// unscripted router is unsure, so merud takes the search+tools
	// fallback, which searches.
	const newFact = "The Q3 budget for the garden project is 5,000 dollars."
	if err := os.WriteFile(filepath.Join(notes, "garden.md"), []byte("# Garden\n\n## Budget\n\n"+newFact+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, indexTimeout, "the changed note in the prompt", func() bool {
		ask(t, s.home.socket, "", gardenQuestion)
		prompt := lastMainPrompt(t, s.fake)
		return strings.Contains(prompt, newFact) && !strings.Contains(prompt, gardenFact)
	})

	// Delete a note: the watcher removes it from the index.
	if err := os.Remove(filepath.Join(notes, "travel.md")); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s.home, "3 files")

	// A secret file added while merud runs stays out too.
	if err := os.WriteFile(filepath.Join(notes, "id_rsa"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"+secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// `meru index` rescans on demand; nothing is new by now except the
	// key file, which the skip rules leave out.
	res := runMeru(t, s.home, "index")
	if res.code != 0 {
		t.Fatalf("meru index exited %d: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "0 files indexed") || !strings.Contains(res.stderr, "scanning 1 folder") {
		t.Errorf("meru index printed stdout %q, stderr %q", res.stdout, res.stderr)
	}
	waitForStatus(t, s.home, "3 files")

	// Ask with the words round the secret, never the secret itself, which
	// would then sit in the prompt as the question.
	ask(t, s.home.socket, "", "GARDEN_API_KEY OPENSSH PRIVATE KEY")
	if prompt := lastMainPrompt(t, s.fake); strings.Contains(prompt, secretValue) {
		t.Errorf("a secret reached the prompt:\n%s", prompt)
	}

	// A folder outside [index] folders is refused with the fix spelled out.
	outside := t.TempDir()
	res = runMeru(t, s.home, "index", outside)
	if res.code != 1 || !strings.Contains(res.stderr, "isn't inside any [index] folder") || !strings.Contains(res.stderr, "restart merud") {
		t.Errorf("meru index %s: exit %d, stderr %q; want a refusal that names the config file", outside, res.code, res.stderr)
	}
	// One folder inside works.
	res = runMeru(t, s.home, "index", notes)
	if res.code != 0 || !strings.Contains(res.stdout, "unchanged") {
		t.Errorf("meru index %s: exit %d, stdout %q, stderr %q", notes, res.code, res.stdout, res.stderr)
	}
}
