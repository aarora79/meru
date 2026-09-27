// This file holds the about_meru tool: merud's own facts about this setup,
// such as which models answer, what Ollama runs them, which folders Meru
// reads and which tools it can reach. A model asked "which model are you?"
// otherwise answers from its training, and a real one said it was a model
// from another company with no version number, while merud knew the exact
// name. The tool only reads, so it runs on every route, without asking.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/engine"
)

// AboutMeru is the about_meru tool's name, as the model sees it.
const AboutMeru = "about_meru"

// aboutDescription is what the model reads about the tool.
const aboutDescription = "Gives the facts about you and this Meru setup: the exact name of each model and what it does, " +
	"the Ollama version, the computer, the folders Meru reads, the connected tools, skills and memories. " +
	"Call it for any question about yourself, such as which model you are or what you can reach; " +
	"don't answer those from what you learned in training."

// maxAboutChars caps the tool's text. It goes into the model's context on
// every call, so it stays short; 2,000 characters is about 500 tokens.
const maxAboutChars = 2000

// maxAboutNames caps each list of names, such as the skills or the
// commands, so one long list can't push the others out of the cap.
const maxAboutNames = 12

// About is what merud knows about itself, for about_meru. merud fills it
// on each call, so the answer shows the setup as it is now. It holds names,
// counts and paths only: never a secret, an environment value, a header,
// a file's text, a memory's text or a line of a transcript.
type About struct {
	Version string // merud's build, such as "v0.3.0" or "(devel)"
	Profile string // [models] profile, such as "lite"

	Fast, Main, Embed string // the model names from config.toml

	// MainDetails is what Ollama says about the main model. Its fields
	// stay empty when Ollama didn't answer.
	MainDetails engine.ModelDetails

	RuntimeVersion string // Ollama's version; "" when it didn't answer
	Machine        string // the line that describes the computer

	Folders       []string // the [index] folders, with ~ for the home folder
	Files, Chunks int      // what the search index holds
	DBBytes       int64    // the size of meru.db on disk

	Sources  []AboutSource  // the MCP servers and A2A agents
	Commands []string       // the [[commands]] tools, such as "cmd.git-log"
	Skills   []string       // the skills that load
	Disabled []string       // [skills] disabled
	Memories map[string]int // how many memories of each kind

	// The tools fill these two themselves, from their own settings.
	builtins  []string
	outputDir string
}

// AboutSource is one MCP server or A2A agent, as about_meru reports it.
type AboutSource struct {
	Name      string
	Kind      string // "mcp" or "a2a", as dispatch names them
	Connected bool
	Tools     int // the allowed tools the model can use now
}

// aboutSpec returns the tool's spec. It takes no arguments.
func aboutSpec() engine.ToolSpec {
	return engine.ToolSpec{
		Name:        AboutMeru,
		Description: aboutDescription,
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

// UseAbout gives about_meru the function that gathers merud's facts. merud
// calls it once at startup, after the tool sources exist, since the facts
// name them. Until then, or with a nil facts, about_meru stays off.
func (t *Tools) UseAbout(facts func(ctx context.Context) About) {
	t.about = facts
}

// aboutMeru answers one about_meru call. It adds the built-in tools and the
// output folder, which the tools know better than merud, to the facts and
// writes them as text.
func (t *Tools) aboutMeru(ctx context.Context) string {
	a := t.about(ctx)
	for _, s := range t.Tools() {
		a.builtins = append(a.builtins, s.Name)
	}
	if t.outputDir != "" {
		a.outputDir = t.show(t.outputDir)
	}
	return aboutText(a)
}

// aboutText writes a as short lines the model can quote. It leaves out a
// line whose facts are missing, rather than guess, and cuts the text at
// maxAboutChars.
func aboutText(a About) string {
	var b strings.Builder
	// line writes one line; fmt.Fprintf writes to b as Printf writes to
	// the terminal.
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	line("You are Meru %s, a personal assistant that runs on the user's own computer. Profile: %s.", a.Version, a.Profile)
	where := "Ollama runs every model on this computer; no model runs in the cloud."
	if a.RuntimeVersion != "" {
		where = "Ollama " + a.RuntimeVersion + " runs every model on this computer; no model runs in the cloud."
	}
	line("%s", where)
	line("Answer model (main): %s. It writes every answer, so it is the model the user talks to.%s", a.Main, mainDetails(a.MainDetails))
	line("Fast model: %s. It picks each question's route and skills, and summarizes quiet sessions.", a.Fast)
	line("Embedding model: %s. It turns text into vectors for search.", a.Embed)
	if a.Machine != "" {
		line("%s", a.Machine)
	}

	if len(a.Folders) == 0 {
		line("Indexed folders: none yet.")
	} else {
		line("Indexed folders: %s. The index holds %s in %s; meru.db is %s.",
			nameList(a.Folders), plural(a.Files, "file"), plural(a.Chunks, "chunk"), size(a.DBBytes))
	}
	line("MCP servers: %s.", sources(a.Sources, "mcp"))
	line("A2A agents: %s.", sources(a.Sources, "a2a"))
	line("Local commands: %s.", nameList(a.Commands))
	line("Built-in tools: %s.", nameList(a.builtins))
	skills := nameList(a.Skills)
	if len(a.Disabled) > 0 {
		skills += "; turned off: " + nameList(a.Disabled)
	}
	line("Skills: %s.", skills)
	line("Memories: %s.", memoryCounts(a.Memories))
	if a.outputDir != "" {
		line("Output folder, where write_file saves: %s.", a.outputDir)
	}
	return cut(strings.TrimRight(b.String(), "\n"), maxAboutChars)
}

// mainDetails writes what Ollama said about the main model, such as
// " 36.0B parameters, Q4_K_M, a context of 262144 tokens. It can: vision,
// tools, thinking.", or "" when Ollama said nothing.
func mainDetails(d engine.ModelDetails) string {
	var parts []string
	if d.ParameterSize != "" {
		parts = append(parts, d.ParameterSize+" parameters")
	}
	if d.Quantization != "" {
		parts = append(parts, d.Quantization)
	}
	if d.ContextLength > 0 {
		parts = append(parts, fmt.Sprintf("a context of %d tokens", d.ContextLength))
	}
	out := ""
	if len(parts) > 0 {
		out = " " + strings.Join(parts, ", ") + "."
	}
	// "completion" is every chat model's, so it says nothing.
	caps := slices.DeleteFunc(slices.Clone(d.Capabilities), func(c string) bool { return c == "completion" })
	if len(caps) > 0 {
		out += " It can: " + strings.Join(caps, ", ") + "."
	}
	return out
}

// nameList joins list with commas, or says "none". Past maxAboutNames it
// says how many more there are.
func nameList(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	if len(list) > maxAboutNames {
		return strings.Join(list[:maxAboutNames], ", ") + fmt.Sprintf(" and %d more", len(list)-maxAboutNames)
	}
	return strings.Join(list, ", ")
}

// sources lists the sources of one kind, such as "google (connected, 9
// tools), obsidian (not connected)", or says "none".
func sources(all []AboutSource, kind string) string {
	var out []string
	for _, s := range all {
		if s.Kind != kind {
			continue
		}
		if s.Connected {
			out = append(out, fmt.Sprintf("%s (connected, %s)", s.Name, plural(s.Tools, "tool")))
		} else {
			out = append(out, s.Name+" (not connected)")
		}
	}
	return nameList(out)
}

// memoryCounts writes the counts by kind in kind order, such as "me 3,
// preferences 2", or says "none".
func memoryCounts(m map[string]int) string {
	var kinds []string
	for k, n := range m {
		if n > 0 {
			kinds = append(kinds, k)
		}
	}
	slices.Sort(kinds)
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return nameList(out)
}
