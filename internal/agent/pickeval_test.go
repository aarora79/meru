// This file holds the labelled questions for the skill-pick eval and the
// rule that scores a pick. The eval itself talks to the local Ollama, so it
// lives in skills_integration_test.go behind the integration tag; the tests
// here check the fixture and the scoring rule on every `go test`.

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// labelledPick is one row of testdata/picks.jsonl: a question, the skills
// a person would pick for it, and the kind of question, which the eval
// report groups by. The `json:"q"` tags are struct tags: they tell
// encoding/json which key fills each field.
type labelledPick struct {
	Q    string   `json:"q"`
	Want []string `json:"want"`
	Kind string   `json:"kind"`
}

// researchSkills are the two skills whose mix-up the eval measures: one
// sends the model into the user's folders, the other to the web.
var researchSkills = []string{"file-research", "web-research"}

// loadPicks reads the labelled questions at path, one JSON object a line,
// skipping blank lines. It fails on a line that doesn't parse or has no
// question or kind.
func loadPicks(path string) ([]labelledPick, error) {
	f, err := os.Open(path) // #nosec G304 -- a test fixture path, not user input
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []labelledPick
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r labelledPick
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if r.Q == "" || r.Kind == "" {
			return nil, fmt.Errorf("%s:%d: want a question and a kind", path, n)
		}
		rows = append(rows, r)
	}
	return rows, sc.Err()
}

// pickRight reports whether got is a right pick for a question labelled
// want: got names every skill in want, and no research skill that want
// leaves out. A pick that adds writing or explainer still counts: those
// change how the answer reads, not where the facts come from.
func pickRight(got, want []string) bool {
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	for _, r := range researchSkills {
		if slices.Contains(got, r) && !slices.Contains(want, r) {
			return false
		}
	}
	return true
}

func TestPickRight(t *testing.T) {
	tests := []struct {
		name      string
		got, want []string
		right     bool
	}{
		{"exact", []string{"web-research"}, []string{"web-research"}, true},
		{"extra writing is fine", []string{"web-research", "writing"}, []string{"web-research"}, true},
		{"wrong research skill", []string{"file-research"}, []string{"web-research"}, false},
		{"both research skills", []string{"file-research", "web-research"}, []string{"web-research"}, false},
		{"missed the skill", nil, []string{"file-research"}, false},
		{"none for chat", nil, nil, true},
		{"research skill on chat", []string{"web-research"}, nil, false},
		{"writing on chat", []string{"writing"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickRight(tt.got, tt.want); got != tt.right {
				t.Errorf("pickRight(%v, %v) = %v, want %v", tt.got, tt.want, got, tt.right)
			}
		})
	}
}

// TestPickFixture checks that testdata/picks.jsonl parses and names only
// skills Meru ships, so a typo there fails here rather than as a low score.
func TestPickFixture(t *testing.T) {
	rows, err := loadPicks("testdata/picks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 20 {
		t.Errorf("got %d rows, want at least 20", len(rows))
	}
	reg := builtinRegistry(t)
	for _, r := range rows {
		for _, w := range r.Want {
			if !reg.Has(w) {
				t.Errorf("%q wants %q, which isn't a built-in skill", r.Q, w)
			}
		}
	}
}
