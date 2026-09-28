// This file tests the file tools on the past chats: list_folder, grep and
// read_file on a sessions folder under a hidden folder, and the window
// grep cuts around a match on a long line. Every chat here is made up.

package builtin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/index"
)

// chatTree holds the paths of the test tree for the past chats.
type chatTree struct {
	notes    string // the one [index] folder
	sessions string // the sessions folder, under the hidden .meru
	chat     string // one chat file
	config   string // a file in .meru beside the sessions folder
}

// newChatTree builds an [index] folder and a sessions folder under a
// hidden .meru folder in a temp directory, and returns built-in tools that
// read both.
func newChatTree(t *testing.T) (*Tools, chatTree) {
	t.Helper()
	// EvalSymlinks: on macOS the temp folder sits under a symlink, and
	// the indexer's Roots gives resolved paths.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := chatTree{
		notes:    filepath.Join(base, "notes"),
		sessions: filepath.Join(base, ".meru", "sessions"),
		chat:     filepath.Join(base, ".meru", "sessions", "2026", "09", "2026-09-17T141502-7f3a.jsonl"),
		config:   filepath.Join(base, ".meru", "config.toml"),
	}
	// A long answer, with the word grep looks for far from the line's
	// start, as in a real chat's answer line.
	long := `{"ts":"2026-09-17T14:16:00Z","type":"assistant","text":"` + strings.Repeat("Water the beds at dawn. ", 60) +
		`Add a layer of compost each spring.` + strings.Repeat(" Mulch keeps the soil damp.", 40) + `"}`
	files := map[string]string{
		"notes/beds.md": "# Beds\n\nTwo raised beds by the fence.\n",
		".meru/sessions/2026/09/2026-09-17T141502-7f3a.jsonl": `{"ts":"2026-09-17T14:15:02Z","type":"user","text":"how deep should a raised bed be?"}` + "\n" + long + "\n",
		".meru/sessions/2026/09/2026-09-20T081000-b2c4.jsonl": `{"ts":"2026-09-20T08:10:00Z","type":"user","text":"which raised bed gets the most sun?"}` + "\n",
		".meru/sessions/2026/09/.draft.jsonl":                 `{"type":"user","text":"compost draft"}` + "\n",
		".meru/config.toml":                                   "[index]\n",
	}
	for rel, body := range files {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := index.New(config.Index{Folders: []string{tr.notes}, MaxFileMB: 1}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(filepath.Join(base, ".meru", "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil)
	tools.ReadSessions(tr.sessions)
	return tools, tr
}

// TestChatsListed checks list_folder: with no path it names the sessions
// folder with its label, and on the folder it lists the chats but not the
// hidden file.
func TestChatsListed(t *testing.T) {
	tools, tr := newChatTree(t)
	tests := []struct {
		name    string
		args    string
		want    []string
		notWant []string
	}{
		{
			name: "no path lists the sessions folder with its label",
			args: `{}`,
			want: []string{tr.notes + "/", tr.sessions + "/  (" + sessionsLabel + ")"},
		},
		{
			name:    "the sessions folder lists the chats",
			args:    `{"path":` + jsonPath(tr.sessions) + `,"depth":3}`,
			want:    []string{"2026/09/2026-09-17T141502-7f3a.jsonl", "2026/09/2026-09-20T081000-b2c4.jsonl"},
			notWant: []string{".draft.jsonl"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tools, t.Context(), ListFolder, tt.args)
			if res.IsError {
				t.Fatalf("list_folder failed: %s", res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result lacks %q:\n%s", w, res.Text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(res.Text, w) {
					t.Errorf("result holds %q:\n%s", w, res.Text)
				}
			}
		})
	}
}

// TestChatsGrepAndRead checks grep and read_file on the chats: grep with
// the sessions folder as path finds a chat's line, grep with no path
// leaves the chats out, read_file opens a chat, and neither reaches a
// file beside the sessions folder in .meru.
func TestChatsGrepAndRead(t *testing.T) {
	tools, tr := newChatTree(t)
	tests := []struct {
		name    string
		tool    string
		args    string
		isError bool
		want    []string
		notWant []string
	}{
		{
			name: "grep in the sessions folder finds both chats",
			tool: Grep,
			args: `{"pattern":"raised bed","path":` + jsonPath(tr.sessions) + `}`,
			want: []string{"2 matching lines in 2 files", "2026-09-17T141502-7f3a.jsonl:1: ", "2026-09-20T081000-b2c4.jsonl:1: "},
		},
		{
			name:    "grep in the sessions folder skips the hidden file",
			tool:    Grep,
			args:    `{"pattern":"compost","path":` + jsonPath(tr.sessions) + `}`,
			want:    []string{"1 matching line in 1 file", "Add a layer of compost"},
			notWant: []string{".draft.jsonl"},
		},
		{
			name:    "grep with no path leaves the chats out",
			tool:    Grep,
			args:    `{"pattern":"raised bed"}`,
			want:    []string{"1 matching line in 1 file", "beds.md:3: "},
			notWant: []string{".jsonl"},
		},
		{
			name: "read_file opens a chat",
			tool: ReadFile,
			args: `{"path":` + jsonPath(tr.chat) + `}`,
			want: []string{`"type":"user","text":"how deep should a raised bed be?"`},
		},
		{
			name:    "read_file refuses a file beside the sessions folder",
			tool:    ReadFile,
			args:    `{"path":` + jsonPath(tr.config) + `}`,
			isError: true,
			want:    []string{"outside the folders the file tools read"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tools, t.Context(), tt.tool, tt.args)
			if res.IsError != tt.isError {
				t.Fatalf("IsError = %v, want %v:\n%s", res.IsError, tt.isError, res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result lacks %q:\n%s", w, res.Text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(res.Text, w) {
					t.Errorf("result holds %q:\n%s", w, res.Text)
				}
			}
		})
	}
}

// TestChatsInDescriptions checks the file tools' descriptions name the
// sessions folder, and that grep's says a search with no path leaves the
// chats out.
func TestChatsInDescriptions(t *testing.T) {
	tools, tr := newChatTree(t)
	for _, s := range tools.Tools() {
		if s.Name != ReadFile && s.Name != ListFolder && s.Name != Grep {
			continue
		}
		if !strings.Contains(s.Description, tr.sessions+" holds your past chats") {
			t.Errorf("%s's description doesn't name the sessions folder:\n%s", s.Name, s.Description)
		}
		if s.Name == Grep && !strings.Contains(s.Description, "but the past chats") {
			t.Errorf("grep's description doesn't say it leaves the chats out:\n%s", s.Description)
		}
	}
}

// TestReadSessionsWithoutFiles checks ReadSessions does nothing when the
// tools have no indexer, so the descriptions don't name a folder the tools
// can't read.
func TestReadSessionsWithoutFiles(t *testing.T) {
	tools := New("config.toml", config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	tools.ReadSessions(t.TempDir())
	if tools.sessionsDir != "" {
		t.Errorf("sessionsDir = %q with no indexer, want \"\"", tools.sessionsDir)
	}
}

// TestAround checks the window grep cuts from a long line: the whole line
// when it fits, and otherwise n characters around the first match, with
// "…" at each end it cut.
func TestAround(t *testing.T) {
	long := strings.Repeat("a", 300) + "needle" + strings.Repeat("b", 300)
	tests := []struct {
		name  string
		line  string
		re    string
		n     int
		want  string // a part the window must hold
		start bool   // the window starts with "…"
		end   bool   // the window ends with "…"
	}{
		{"a short line stays whole", "a needle here", "needle", 20, "a needle here", false, false},
		{"a match in the middle", long, "needle", 50, "needle", true, true},
		{"a match at the start", "needle" + strings.Repeat("b", 300), "needle", 50, "needle", false, true},
		{"a match at the end", strings.Repeat("a", 300) + "needle", "needle", 50, "needle", true, false},
		{"characters of more than one byte", strings.Repeat("é", 300) + "needle" + strings.Repeat("ü", 300), "needle", 50, "needle", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := around(tt.line, regexp.MustCompile(tt.re), tt.n)
			if !strings.Contains(got, tt.want) {
				t.Errorf("around = %q, want it to hold %q", got, tt.want)
			}
			if n := utf8.RuneCountInString(got); n > tt.n {
				t.Errorf("around is %d characters, want at most %d", n, tt.n)
			}
			if !utf8.ValidString(got) {
				t.Errorf("around split a character: %q", got)
			}
			if strings.HasPrefix(got, "…") != tt.start || strings.HasSuffix(got, "…") != tt.end {
				t.Errorf("around = %q, want a leading … %v and a trailing … %v", got, tt.start, tt.end)
			}
		})
	}
}
