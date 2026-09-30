// This file tests about_meru: the text it writes from merud's facts, its
// cap, and that it runs without asking once merud hands it its facts.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
)

// sampleAbout is a setup like the one in the question that led to the
// tool: a large main model, the lite fast model, one folder, one server.
func sampleAbout() About {
	return About{
		Version: "(devel)", Profile: "lite",
		Fast: "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M", Main: "qwen3.6:35b", Embed: "nomic-embed-text",
		MainDetails: engine.ModelDetails{
			Capabilities:  []string{"completion", "vision", "tools", "thinking"},
			ParameterSize: "36.0B", Quantization: "Q4_K_M", ContextLength: 262144,
		},
		RuntimeVersion: "0.34.0",
		Machine:        "The user's computer: macOS 26.0 (arm64), Apple M4 Max, 64 GB memory.",
		Folders:        []string{"~/notes"}, Files: 120, Chunks: 950, DBBytes: 42 << 20,
		Sources: []AboutSource{
			{Name: "google", Kind: dispatch.KindMCP, Connected: true, Tools: 9},
			{Name: "obsidian", Kind: dispatch.KindMCP},
			{Name: "notes", Kind: dispatch.KindMCP, Sentence: "Notes needs your vault folder."},
		},
		Commands: []string{"cmd.git-log"},
		Skills:   []string{"web-research", "writing"},
		Disabled: []string{"explainer"},
		Memories: map[string]int{"preferences": 2, "me": 3, "projects": 0},
	}
}

func TestAboutText(t *testing.T) {
	got := aboutText(sampleAbout())
	for _, want := range []string{
		"Answer model (main): qwen3.6:35b.",
		"Fast model: hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M.",
		"Embedding model: nomic-embed-text.",
		"Ollama runs every model on this computer; no model runs in the cloud.\nOllama version: 0.34.0\n",
		"36.0B parameters, Q4_K_M, a context of 262144 tokens. It can: vision, tools, thinking.",
		"Profile: lite.",
		"Apple M4 Max",
		"Indexed folders: ~/notes. The index holds 120 files in 950 chunks; meru.db is 42.0 MB.",
		"MCP servers: google (connected, 9 tools), obsidian (not connected), notes (not connected; Notes needs your vault folder.).",
		"A2A agents: none.",
		"Local commands: cmd.git-log.",
		"Skills: web-research, writing; turned off: explainer.",
		"Memories: me 3, preferences 2.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the text lacks %q:\n%s", want, got)
		}
	}
	if n := utf8.RuneCountInString(got); n > maxAboutChars {
		t.Errorf("the text is %d characters, over %d", n, maxAboutChars)
	}
}

// TestAboutTextWithoutOllama checks that a runtime that didn't answer
// leaves its facts out rather than making them up.
func TestAboutTextWithoutOllama(t *testing.T) {
	a := sampleAbout()
	a.RuntimeVersion, a.MainDetails = "", engine.ModelDetails{}
	got := aboutText(a)
	if !strings.Contains(got, "Answer model (main): qwen3.6:35b. It writes every answer, so it is the model the user talks to.\n") ||
		strings.Contains(got, "parameters") || strings.Contains(got, "Ollama version") {
		t.Errorf("text with no Ollama facts:\n%s", got)
	}
}

// TestAboutTextCap checks that long lists shrink to a count and the whole
// text stays under maxAboutChars.
func TestAboutTextCap(t *testing.T) {
	a := sampleAbout()
	for i := range 200 {
		a.Commands = append(a.Commands, fmt.Sprintf("cmd.command-with-a-long-name-%03d", i))
		a.Sources = append(a.Sources, AboutSource{Name: fmt.Sprintf("server-with-a-long-name-%03d", i), Kind: dispatch.KindA2A})
	}
	got := aboutText(a)
	if n := utf8.RuneCountInString(got); n > maxAboutChars {
		t.Errorf("the text is %d characters, over %d", n, maxAboutChars)
	}
	if !strings.Contains(got, "and 189 more") {
		t.Errorf("the command list didn't shrink to a count:\n%s", got)
	}
}

// TestAboutMeruTool runs about_meru through the backend: off until merud
// hands it its facts, then offered, run without asking, and holding the
// built-ins and the output folder, which the tools add themselves.
func TestAboutMeruTool(t *testing.T) {
	dir := t.TempDir()
	tools := New(dir+"/config.toml", config.Builtin{Tools: config.BuiltinTools()}, config.Web{MaxResults: 8}, nil, dir, nil, nil, nil)
	offered := func() []string {
		var out []string
		for _, s := range tools.Tools() {
			out = append(out, s.Name)
		}
		return out
	}
	if slices.Contains(offered(), AboutMeru) {
		t.Fatal("about_meru is offered before merud gave it facts")
	}
	tools.UseAbout(func(context.Context) About { return sampleAbout() })
	if !slices.Contains(offered(), AboutMeru) {
		t.Fatalf("about_meru isn't offered: %v", offered())
	}
	if c := tools.Confirm(AboutMeru); c != dispatch.ConfirmNever {
		t.Errorf("Confirm = %v, want ConfirmNever", c)
	}
	res, err := tools.Call(t.Context(), AboutMeru, json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("Call = %+v, %v", res, err)
	}
	for _, want := range []string{"qwen3.6:35b", "Built-in tools: configure, datetime, about_meru, write_file, web_fetch.",
		"Output folder, where write_file saves: " + dir + "."} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the result lacks %q:\n%s", want, res.Text)
		}
	}
}

// TestAboutDescription checks that the tool's description tells the model
// to quote names and numbers as the tool gives them. Without that line a
// model read "0.34.0" and wrote "Ollama 0.44".
func TestAboutDescription(t *testing.T) {
	d := aboutSpec().Description
	for _, want := range []string{"which model you are", "Quote model names, versions and numbers exactly as this tool gives them."} {
		if !strings.Contains(d, want) {
			t.Errorf("the description lacks %q:\n%s", want, d)
		}
	}
}
