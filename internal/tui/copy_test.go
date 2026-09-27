// This file tests copying code blocks: finding them in an answer, the
// "⧉ copy N" labels on screen, /copy and Ctrl-Y, the mouse, and the
// clipboard itself. A fake clipboard stands in for the real one throughout.

package tui

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// TestFindCodeBlocks checks which blocks findCodeBlocks finds in an answer
// and the text /copy would copy from each.
func TestFindCodeBlocks(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"fenced with a language", "Run:\n\n```sh\nbtop\nsudo btop\n```\n", []string{"btop\nsudo btop"}},
		{"fenced without a language", "```\nls -la\n```\n", []string{"ls -la"}},
		{"tildes", "~~~bash\necho hi\n~~~\n", []string{"echo hi"}},
		{"indented", "Try this:\n\n    make build\n    make test\n\nThen stop.\n", []string{"make build\nmake test"}},
		{"nested in a list", "1. Install it:\n\n   ```sh\n   brew install btop\n   ```\n2. Run it.\n", []string{"brew install btop"}},
		{"in a quote", "> ```\n> uptime\n> ```\n", []string{"uptime"}},
		{"two blocks", "```\none\n```\n\ntext\n\n```go\ntwo := 2\n```\n", []string{"one", "two := 2"}},
		{"none", "Use `btop` to watch the machine.\n", nil},
		{"at the very end, unclosed", "Run:\n\n```sh\nbtop", []string{"btop"}},
		{"keeps inner indentation", "```go\nif ok {\n\treturn\n}\n```\n", []string{"if ok {\n\treturn\n}"}},
		{"only blank lines", "```\n\n```\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, b := range findCodeBlocks(tt.src) {
				got = append(got, b.text)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findCodeBlocks = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMarkBlocks checks that each marker lands as the block's last line,
// with the block's prefix, and that a block with no newline at the end of
// the answer gets one.
func TestMarkBlocks(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"fenced", "```sh\nbtop\n```\n", "```sh\nbtop\nMERUCOPY4X\n```\n"},
		{"in a list", "- a:\n\n  ```\n  x\n  ```\n", "- a:\n\n  ```\n  x\n  MERUCOPY4X\n  ```\n"},
		{"indented", "    make\n\nend\n", "    make\n    MERUCOPY4X\n\nend\n"},
		{"at the very end", "```\nbtop", "```\nbtop\nMERUCOPY4X\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := markBlocks(tt.src, findCodeBlocks(tt.src), 4); got != tt.want {
				t.Errorf("markBlocks = %q, want %q", got, tt.want)
			}
		})
	}
}

// threeBlocks is an answer with three code blocks of different kinds: one
// with tildes inside a list, one indented inside a list, and one with a
// line too long for a narrow screen.
const threeBlocks = "Watch the machine with btop.\n\n" +
	"1. Start it:\n\n   ~~~\n   btop\n   ~~~\n\n" +
	"2. Or pick a preset:\n\n       btop --preset 1\n\n" +
	"For a slow link, turn the colours down:\n\n" +
	"```sh\nbtop --utf-force --low-color --update 2000\n```\n"

// chatWith builds a chat screen of the given size and runs one finished
// turn per answer, each with the stats line.
func chatWith(t *testing.T, width, height int, answers ...string) Model {
	t.Helper()
	m := screen(t, width, height, "", nil, false, nil)
	for i, a := range answers {
		m, _ = update(t, m, typeText(fmt.Sprintf("Question %d", i+1)), press(tea.KeyEnter))
		m, _ = update(t, m,
			eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventToken, Text: a}},
			eventMsg{turn: m.turn, ev: rpc.Event{Type: rpc.EventDone, TTFTMillis: 800, DurationMillis: 2400, TokensOut: 64}},
			turnDoneMsg{turn: m.turn})
	}
	return m
}

// TestCopyLabelsGolden draws answers with code blocks and compares the
// screen with golden files: three blocks at two widths. The markdown
// golden in view_test.go covers an answer with one block.
func TestCopyLabelsGolden(t *testing.T) {
	for _, tt := range []struct {
		name  string
		width int
	}{
		{"copy-three", 80},
		{"copy-narrow", 40},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := chatWith(t, tt.width, 34, threeBlocks)
			view := m.View()
			for i, l := range strings.Split(view, "\n") {
				if w := ansi.StringWidth(l); w > tt.width {
					t.Errorf("line %d is %d wide, want at most %d: %q", i+1, w, tt.width, l)
				}
			}
			if strings.Contains(view, "MERUCOPY") {
				t.Errorf("a marker shows on screen:\n%s", view)
			}
			golden(t, tt.name, view)
		})
	}
}

// TestLabelsAfterResize checks that the labels survive a new width: the
// answer is drawn again, and each block keeps its label and number.
func TestLabelsAfterResize(t *testing.T) {
	m := chatWith(t, 80, 40, threeBlocks)
	for _, w := range []int{50, 120, 80} {
		m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: 40})
		view := m.View()
		for n := 1; n <= 3; n++ {
			if !strings.Contains(view, copyLabel(n)) {
				t.Errorf("width %d: no %q on screen:\n%s", w, copyLabel(n), view)
			}
		}
	}
}

// TestLabelsCountAcrossTurns checks that block numbers go on counting from
// one answer to the next, and start again after /new.
func TestLabelsCountAcrossTurns(t *testing.T) {
	m := chatWith(t, 80, 60, "```\nfirst\n```\n", "No code here.", threeBlocks)
	if m.blockCount != 4 {
		t.Fatalf("blockCount = %d, want 4", m.blockCount)
	}
	view := m.View()
	for n := 1; n <= 4; n++ {
		if !strings.Contains(view, copyLabel(n)) {
			t.Errorf("no %q on screen:\n%s", copyLabel(n), view)
		}
	}
	if text, ok := m.block(3); !ok || text != "btop --preset 1" {
		t.Errorf("block(3) = %q, %v; want the indented block", text, ok)
	}
	m, _ = update(t, m, typeText("/new"), press(tea.KeyEnter))
	if m.blockCount != 0 {
		t.Errorf("after /new, blockCount = %d, want 0", m.blockCount)
	}
}

// TestStreamingHasNoLabels checks that an answer still streaming shows no
// labels: its blocks get numbers only once it ends.
func TestStreamingHasNoLabels(t *testing.T) {
	m := screen(t, 80, 30, "How do I run btop?", []rpc.Event{{Type: rpc.EventToken, Text: "```\nbtop\n```\n"}}, false, nil)
	if strings.Contains(m.View(), "⧉") {
		t.Errorf("a streaming answer shows a label:\n%s", m.View())
	}
}

// fakeClipboard records what the model copies.
type fakeClipboard struct {
	got []string
	via string
	err error
}

// copy is the fake copyFunc.
func (f *fakeClipboard) copy(text string) (string, error) {
	f.got = append(f.got, text)
	return f.via, f.err
}

// runCopy runs the command a copy returned, if any, and feeds its message
// back, as Bubble Tea would.
func runCopy(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd != nil {
		m, _ = update(t, m, cmd())
	}
	return m
}

// TestCopyCommands checks /copy N, /copy, Ctrl-Y and the notices when
// there is nothing to copy.
func TestCopyCommands(t *testing.T) {
	tests := []struct {
		name    string
		answers []string
		keys    []tea.Msg
		want    []string // what reached the clipboard
		notice  string
	}{
		{"/copy N", []string{threeBlocks}, []tea.Msg{typeText("/copy 2"), press(tea.KeyEnter)},
			[]string{"btop --preset 1"}, "copied block 2 (1 line)"},
		{"/copy alone", []string{"```\nls\npwd\n```\n", threeBlocks}, []tea.Msg{typeText("/copy"), press(tea.KeyEnter)},
			[]string{"btop --utf-force --low-color --update 2000"}, "copied block 4 (1 line)"},
		{"Ctrl-Y", []string{threeBlocks, "```\nls\npwd\n```\n"}, []tea.Msg{press(tea.KeyCtrlY)},
			[]string{"ls\npwd"}, "copied block 4 (2 lines)"},
		{"an older block", []string{"```\nls\npwd\n```\n", threeBlocks}, []tea.Msg{typeText("/copy 1"), press(tea.KeyEnter)},
			[]string{"ls\npwd"}, "copied block 1 (2 lines)"},
		{"out of range", []string{threeBlocks}, []tea.Msg{typeText("/copy 7"), press(tea.KeyEnter)},
			nil, "no code block 7 · blocks go from 1 to 3"},
		{"not a number", []string{threeBlocks}, []tea.Msg{typeText("/copy two"), press(tea.KeyEnter)},
			nil, "/copy takes a block number, such as /copy 2, or answer"},
		{"no blocks", []string{"No code here."}, []tea.Msg{typeText("/copy 1"), press(tea.KeyEnter)},
			nil, "no code block to copy yet"},
		{"no answer yet", nil, []tea.Msg{press(tea.KeyCtrlY)},
			nil, "no code block to copy yet"},
		{"newest answer has none", []string{threeBlocks, "No code here."}, []tea.Msg{press(tea.KeyCtrlY)},
			nil, "the newest answer has no code block · /copy N copies an older one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clip := &fakeClipboard{via: "pbcopy"}
			m := chatWith(t, 80, 40, tt.answers...)
			m.copy = clip.copy
			m, cmd := update(t, m, tt.keys...)
			m = runCopy(t, m, cmd)
			if !reflect.DeepEqual(clip.got, tt.want) {
				t.Errorf("copied %q, want %q", clip.got, tt.want)
			}
			if m.notice != tt.notice {
				t.Errorf("notice = %q, want %q", m.notice, tt.notice)
			}
			if m.input.Value() != "" {
				t.Errorf("input = %q, want it cleared", m.input.Value())
			}
		})
	}
}

// TestCopyNotices checks the notice after a copy through OSC 52 and after
// one that failed.
func TestCopyNotices(t *testing.T) {
	tests := []struct {
		msg  copiedMsg
		want string
	}{
		{copiedMsg{n: 3, lines: 2, via: "pbcopy"}, "copied block 3 (2 lines)"},
		{copiedMsg{n: 1, lines: 1, via: "OSC 52"}, "copied block 1 (1 line) through the terminal (OSC 52): no clipboard program found"},
		{copiedMsg{n: 2, err: errors.New("xclip: exit status 1")}, "couldn't copy block 2: xclip: exit status 1"},
	}
	for _, tt := range tests {
		if got := tt.msg.notice(); got != tt.want {
			t.Errorf("notice = %q, want %q", got, tt.want)
		}
	}
}

// TestCtrlYFreeInInput checks that the input box binds nothing to Ctrl-Y,
// the key the chat uses to copy, so the two can't clash.
func TestCtrlYFreeInInput(t *testing.T) {
	m := testModel(nil, newFakeSender())
	// reflect walks the key map's fields, one key.Binding each.
	km := reflect.ValueOf(m.input.KeyMap)
	for i := 0; i < km.NumField(); i++ {
		b, ok := km.Field(i).Interface().(key.Binding)
		if !ok {
			continue
		}
		for _, k := range b.Keys() {
			if k == "ctrl+y" {
				t.Errorf("the input box binds ctrl+y to %s", km.Type().Field(i).Name)
			}
		}
	}
}

// labelPos returns the screen row and column of label on m's screen, or
// fails the test.
func labelPos(t *testing.T, m Model, label string) (x, y int) {
	t.Helper()
	for row, l := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(l)
		if i := strings.Index(plain, label); i >= 0 {
			return ansi.StringWidth(plain[:i]), row
		}
	}
	t.Fatalf("no %q on screen:\n%s", label, m.View())
	return 0, 0
}

// TestMouseCopy checks a click on a label: with [chat] mouse_copy on it
// copies that block, a click beside the label copies nothing, and with the
// setting off no click copies.
func TestMouseCopy(t *testing.T) {
	click := func(x, y int) tea.MouseMsg {
		return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	}
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("mouse_copy=%v", on), func(t *testing.T) {
			clip := &fakeClipboard{via: "pbcopy"}
			m := chatWith(t, 80, 40, threeBlocks)
			m.info.MouseCopy = on
			m.copy = clip.copy
			x, y := labelPos(t, m, copyLabel(2))

			// The last cell of the label, then the cell after it.
			width := ansi.StringWidth(copyLabel(2))
			m, cmd := update(t, m, click(x+width-1, y))
			m = runCopy(t, m, cmd)
			m, cmd = update(t, m, click(x+width, y))
			m = runCopy(t, m, cmd)

			var want []string
			if on {
				want = []string{"btop --preset 1"}
			}
			if !reflect.DeepEqual(clip.got, want) {
				t.Errorf("copied %q, want %q", clip.got, want)
			}
		})
	}
}

// TestLabelAt checks the hit test on one screen line: the label's cells
// hit, the cells round it don't.
func TestLabelAt(t *testing.T) {
	line := "    ⧉ copy 12"
	tests := []struct {
		col, want int
	}{
		{3, 0}, {4, 12}, {10, 12}, {12, 12}, {13, 0},
	}
	for _, tt := range tests {
		if got := labelAt(line, tt.col); got != tt.want {
			t.Errorf("labelAt(col %d) = %d, want %d", tt.col, got, tt.want)
		}
	}
}

// TestClipboardCommand checks which program copies on each system, with a
// fake PATH lookup.
func TestClipboardCommand(t *testing.T) {
	// on returns a lookPath that finds only the named programs, in /bin.
	on := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	tests := []struct {
		name string
		goos string
		path func(string) (string, error)
		want []string
	}{
		{"macOS", "darwin", on("pbcopy"), []string{"/bin/pbcopy"}},
		{"Windows", "windows", on("clip.exe"), []string{"/bin/clip.exe"}},
		{"Wayland first", "linux", on("wl-copy", "xclip", "xsel"), []string{"/bin/wl-copy"}},
		{"xclip next", "linux", on("xclip", "xsel"), []string{"/bin/xclip", "-selection", "clipboard"}},
		{"xsel last", "linux", on("xsel"), []string{"/bin/xsel", "-b", "-i"}},
		{"none on Linux", "linux", on(), nil},
		{"macOS ignores Linux programs", "darwin", on("xclip"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clipboardCommand(tt.goos, tt.path); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("clipboardCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClipboardCopy checks both paths of clipboard.copy: a program found
// gets the text on its standard input, exactly, and with none found the
// text goes to the terminal as OSC 52.
func TestClipboardCopy(t *testing.T) {
	const text = "btop\nsudo btop"
	t.Run("program", func(t *testing.T) {
		var gotArgv []string
		var gotText string
		var term bytes.Buffer
		c := clipboard{
			goos:     "darwin",
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			run: func(argv []string, s string) error {
				gotArgv, gotText = argv, s
				return nil
			},
			terminal: &term,
		}
		via, err := c.copy(text)
		if err != nil || via != "pbcopy" {
			t.Fatalf("copy = %q, %v; want pbcopy", via, err)
		}
		if !reflect.DeepEqual(gotArgv, []string{"/usr/bin/pbcopy"}) || gotText != text || term.Len() != 0 {
			t.Errorf("ran %q with %q and wrote %q to the terminal", gotArgv, gotText, term.String())
		}
	})
	t.Run("program fails", func(t *testing.T) {
		c := clipboard{
			goos:     "linux",
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			run:      func([]string, string) error { return errors.New("exit status 1") },
			terminal: &bytes.Buffer{},
		}
		if _, err := c.copy(text); err == nil || !strings.Contains(err.Error(), "wl-copy") {
			t.Errorf("copy error = %v, want one naming wl-copy", err)
		}
	})
	t.Run("OSC 52", func(t *testing.T) {
		var term bytes.Buffer
		c := clipboard{
			goos:     "linux",
			lookPath: func(string) (string, error) { return "", errors.New("not found") },
			run: func([]string, string) error {
				t.Error("ran a program when none was found")
				return nil
			},
			terminal: &term,
		}
		via, err := c.copy(text)
		if err != nil || via != "OSC 52" {
			t.Fatalf("copy = %q, %v; want OSC 52", via, err)
		}
		// "btop\nsudo btop" in base64.
		if want := "\x1b]52;c;YnRvcApzdWRvIGJ0b3A=\a"; term.String() != want {
			t.Errorf("terminal got %q, want %q", term.String(), want)
		}
	})
}
