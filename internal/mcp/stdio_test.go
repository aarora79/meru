// This file tests the stdio helpers on their own: the trimmed child
// environment and the capped stderr log.

package mcp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestChildEnv(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("MERU_TEST_SECRET", "x")
	env := childEnv(map[string]string{"API_KEY": "k", "PATH": "/custom"})

	if !slices.Contains(env, "API_KEY=k") {
		t.Errorf("env %v lacks the configured API_KEY", env)
	}
	if !slices.Contains(env, "PATH=/custom") || slices.Contains(env, "PATH=/bin") {
		t.Errorf("env %v: config's PATH should win over merud's", env)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "MERU_TEST_SECRET=") {
			t.Errorf("env %v leaks merud's MERU_TEST_SECRET", env)
		}
	}
	if !slices.IsSorted(env) {
		t.Errorf("env %v isn't sorted", env)
	}
}

func TestStderrLog(t *testing.T) {
	tests := []struct {
		name      string
		writes    []string
		wantLines []string // the "line" values logged, in order
		wantCap   bool     // the "cap reached" line appears
	}{
		{"one line", []string{"hello\n"}, []string{"hello"}, false},
		{"split across writes", []string{"hel", "lo\nwor", "ld\n"}, []string{"hello", "world"}, false},
		{"blank lines skipped", []string{"\n\n  \nx\r\n"}, []string{"x"}, false},
		{"no newline yet", []string{"partial"}, nil, false},
		{"long line cut", []string{strings.Repeat("a", 3000) + "\n"}, []string{strings.Repeat("a", stderrLineCap)}, false},
		{"total cap", []string{"first\n", strings.Repeat("b\n", stderrTotalCap)}, []string{"first"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			w := &stderrLog{log: log}
			for _, s := range tt.writes {
				if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
					t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(s))
				}
			}
			var lines []string
			gotCap := false
			for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				if raw == "" {
					continue
				}
				if strings.Contains(raw, "cap reached") {
					gotCap = true
					continue
				}
				var rec struct{ Line string }
				if err := json.Unmarshal([]byte(raw), &rec); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, rec.Line)
			}
			if !slices.Equal(lines, tt.wantLines) {
				t.Errorf("logged lines = %q, want %q", lines, tt.wantLines)
			}
			if gotCap != tt.wantCap {
				t.Errorf("cap line logged = %v, want %v", gotCap, tt.wantCap)
			}
		})
	}
}
