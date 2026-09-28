// Command meru is the thin client for merud. It sends one request over the
// Unix socket at ~/.meru/merud.sock and prints the reply. It holds no model or
// store logic; merud does the work. See ARCHITECTURE.md, "The shape: daemon +
// thin client".
//
// Usage:
//
//	meru [-socket path] "question"      ask one question; the answer streams to stdout
//	meru [-socket path] run --json "q"   ask one question; each event goes to stdout as a JSON line
//	meru [-socket path] ping             check that merud is up
//	meru [-socket path] chat             open the terminal UI
//	meru [-socket path] index [folder]   rescan the [index] folders, or just one
//	meru [-socket path] index -status    show what the search index holds
//	meru [-socket path] tools            list the tools the model may use
//	meru [-socket path] log [-n N] [-v]  show the latest tool calls
//	meru [-socket path] usage            show how much you use Meru
//	meru [-socket path] model            the model sets; also model use <name>, model save
//	meru [-socket path] setup            first-run setup: Ollama, models, config, tools
//	meru [-socket path] setup user       tell Meru who you are
//	meru config template                 print every config key with its default
//	meru [-socket path] memory list      show what Meru remembers; also add, forget
//	meru [-socket path] skills list      show the skills; also show, reset
//	meru [-socket path] mcp              the state of each MCP server; also mcp status [--json]
//	meru [-socket path] mcp list         the MCP server catalog and your servers
//	meru [-socket path] mcp add ...      add an MCP server; also remove
//	meru [-socket path] check [file]     rerun your own questions and grade the answers
//	meru check report <results>...       compare saved results across model sets, as Markdown
//
// A question whose first word is ping, chat, index, tools, log, usage, model,
// setup, memory, skills, mcp or check needs quotes, so meru reads it as a question and not
// as a command. So does the question "config template", and a question that
// starts with "run --json".
//
// Exit status: 0 on success, 1 on any error (including bad usage), 130 when
// interrupted with Ctrl-C.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/tui"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// Exit statuses. 130 is the shell's convention for "stopped by SIGINT"
// (128 + signal number 2).
const (
	exitOK          = 0
	exitError       = 1
	exitInterrupted = 130
)

// main turns Ctrl-C into a cancelled context and exits with run's status.
func main() {
	// signal.NotifyContext cancels ctx on Ctrl-C. Cancelling closes the
	// socket, which tells merud to stop the turn.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is meru from start to finish. It returns the exit status instead of
// calling os.Exit, so tests can call it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("meru", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, `usage:
  meru "question"       ask one question
  meru run --json "question"
                        ask one question and write each event as a JSON line,
                        for scripts; a tool call that asks first is denied
  meru ping             check that merud is up
  meru chat             open the terminal UI
  meru index [folder]   rescan the [index] folders, or just one
  meru index -status    show what the search index holds
  meru tools            list the tools the model may use
  meru log [-n N] [-v]  show the latest tool calls, newest first
  meru usage            show how much you use Meru
  meru model            show the model sets and which one is in use
  meru model use <name> [--rebuild]
                        switch to a model set until merud stops
  meru model save       make the models in use the default in config.toml
  meru setup [--main <model>]
                        set up Ollama, the models, config and tools; --main
                        names the answer model for a new config.toml
  meru setup user       tell Meru who you are
  meru config template  print every config key with its default
  meru memory list [kind] | add <kind> <text...> | forget <id>
                        show, save or delete what Meru remembers
  meru skills list | show <name> | reset [--yes] <name>
                        show the skills, print one, or restore a built-in
  meru mcp [status] [--json]
                        show the state of each MCP server
  meru mcp list         show the server catalog and your MCP servers
  meru mcp add <catalog-name>
                        add a server from the catalog
  meru mcp add stdio <name> -- <command> [args...]
  meru mcp add http <name> <url> [--remote]
                        add a server that isn't in the catalog
  meru mcp remove [--yes] <name>
                        take a server out of config.toml
  meru check [file] [--only id,category] [--json] [--save]
                        rerun your questions from ~/.meru/checks.jsonl
                        and grade the answers
  meru check report <results.jsonl>...
                        compare saved check results across model sets, as Markdown

flags:`)
		flags.PrintDefaults()
	}
	socket := flags.String("socket", "", "merud's socket (default ~/.meru/merud.sock)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return exitError
	}

	if *socket == "" {
		dir, err := config.DefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "meru: %v\n", err)
			return exitError
		}
		*socket = filepath.Join(dir, "merud.sock")
	}

	var err error
	switch {
	case isRunCmd(flags.Args()):
		err = runCmd(ctx, *socket, flags.Args()[1:], stdout)
	case flags.NArg() == 1 && flags.Arg(0) == "ping":
		err = ping(ctx, *socket, stdout)
	case flags.NArg() == 1 && flags.Arg(0) == "chat":
		// tui.Run starts the Bubble Tea terminal UI (internal/tui).
		err = tui.Run(ctx, *socket, chatInfo())
	case flags.Arg(0) == "index":
		err = indexCmd(ctx, *socket, flags.Args()[1:], stdout, stderr)
	case flags.Arg(0) == "tools":
		err = toolsCmd(ctx, *socket, flags.Args()[1:], stdout)
	case flags.Arg(0) == "log":
		err = logCmd(ctx, *socket, flags.Args()[1:], stdout, stderr)
	case flags.NArg() == 1 && flags.Arg(0) == "usage":
		err = usageCmd(ctx, *socket, stdout)
	case flags.Arg(0) == "model":
		err = modelCmd(ctx, *socket, flags.Args()[1:], stdout)
	case flags.NArg() == 1 && flags.Arg(0) == "setup":
		err = setupCmd(ctx, *socket, terminal(stdout), "")
	case flags.NArg() == 3 && flags.Arg(0) == "setup" && flags.Arg(1) == "--main":
		err = setupCmd(ctx, *socket, terminal(stdout), flags.Arg(2))
	case flags.NArg() == 2 && flags.Arg(0) == "setup" && flags.Arg(1) == "user":
		err = setupUserCmd(ctx, *socket, terminal(stdout))
	case flags.NArg() == 2 && flags.Arg(0) == "config" && flags.Arg(1) == "template":
		err = configTemplateCmd(stdout)
	case flags.Arg(0) == "memory":
		err = memoryCmd(ctx, *socket, flags.Args()[1:], stdout)
	case flags.Arg(0) == "skills":
		err = skillsCmd(ctx, *socket, flags.Args()[1:], os.Stdin, stdout, isTerminal(os.Stdin))
	case flags.Arg(0) == "mcp":
		err = mcpCmd(ctx, *socket, flags.Args()[1:], terminal(stdout))
	case flags.NArg() >= 2 && flags.Arg(0) == "check" && flags.Arg(1) == "report":
		err = checkReportCmd(flags.Args()[2:], stdout)
	case flags.Arg(0) == "check":
		err = checkCmd(ctx, *socket, flags.Args()[1:], stdout, stderr)
	default:
		// Words after the flags form the question, so quotes are optional:
		// meru what time is it
		// A tool call that needs approval asks on this terminal (approve.go).
		p := newPrompter(os.Stdin, stderr, isTerminal(os.Stdin))
		err = ask(ctx, *socket, strings.Join(flags.Args(), " "), stdout, stderr, p.approve)
	}

	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(stderr) // end the half-printed line
		return exitInterrupted
	case errors.Is(err, errChecksFailed):
		// The summary already said how many failed.
		return exitError
	case err != nil:
		fmt.Fprintf(stderr, "meru: %v\n", err)
		return exitError
	}
	return exitOK
}

// chatInfo reads the profile and main model from the default config file for
// the chat screen's header, the [chat] settings, and Meru's folder for the
// /about box. A config that doesn't load leaves the header without them
// and the settings at their defaults, instead of stopping the chat; merud
// reports config errors when it starts.
func chatInfo() tui.Info {
	// A home folder that can't be found leaves the folder out of /about.
	dir, _ := config.DefaultDir()
	// fallback is what the chat gets when the config can't be read: no
	// profile or model in the header, and mouse copying on, its default.
	fallback := tui.Info{MouseCopy: true, Dir: dir}
	path, err := config.DefaultPath()
	if err != nil {
		return fallback
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fallback
	}
	return tui.Info{Profile: cfg.Profile, Model: cfg.Models.Main, MouseCopy: cfg.Chat.MouseCopy, Dir: dir}
}

// ping asks merud whether it is up and prints the answer.
func ping(ctx context.Context, socket string, stdout io.Writer) error {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpPing}, nil) {
		if err != nil {
			return err
		}
		if ev.Type == rpc.EventError {
			return errors.New(ev.Error)
		}
	}
	fmt.Fprintln(stdout, "merud is up")
	return nil
}

// ask sends question to merud and writes the answer to stdout as it streams
// in, ending with a newline. When merud searched your files for the answer,
// a "Sources:" list follows: the files the answer cites, numbered as it
// cites them. It fails when merud can't be reached or replies with an error.
//
// Tool calls show on stderr as dim lines, and approve answers merud's
// approval questions. When merud sends a notice about the answer, such as
// a claim of an action no tool took, a dim "note:" line on stderr follows
// the answer. Keeping all of these off stdout means `meru "..." > file`
// saves only the answer.
func ask(ctx context.Context, socket, question string, stdout, stderr io.Writer, approve rpc.ApproveFunc) error {
	req := rpc.Request{Op: rpc.OpAsk, Text: question, Source: rpc.SourceCLI}
	var answer strings.Builder // the whole answer, to find its citations
	var sources []rpc.Citation
	var notice string // merud's warning about the answer; "" for none
	endsInNewline := false
	dim := newLook(stderr).dim
	// breakLine starts a new line on the terminal before a tool line or a
	// prompt when the answer stopped mid-line. It writes to stderr, so
	// stdout keeps the answer as the model wrote it. midLine is
	// true while the last thing on screen is answer text with no new line.
	midLine := false
	breakLine := func() {
		if midLine {
			fmt.Fprintln(stderr)
			midLine = false
		}
	}
	prompt := func(ctx context.Context, a rpc.Approval) (rpc.Choice, error) {
		breakLine()
		return approve(ctx, a)
	}
	for ev, err := range rpc.Do(ctx, socket, req, prompt) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventSources:
			sources = ev.Sources
		case rpc.EventToolCall, rpc.EventToolResult:
			if line := toolLine(ev); line != "" {
				breakLine()
				fmt.Fprintln(stderr, dim.Render(line))
			}
		case rpc.EventToken:
			if _, err := io.WriteString(stdout, ev.Text); err != nil {
				return err // stdout closed, as with `meru ... | head -1`
			}
			answer.WriteString(ev.Text)
			endsInNewline = strings.HasSuffix(ev.Text, "\n")
			midLine = !endsInNewline
		case rpc.EventNotice:
			notice = ev.Text
		case rpc.EventError:
			if answer.Len() > 0 && !endsInNewline {
				fmt.Fprintln(stdout)
			}
			return errors.New(ev.Error)
		}
	}
	if answer.Len() > 0 && !endsInNewline {
		fmt.Fprintln(stdout)
	}
	if notice != "" {
		fmt.Fprintln(stderr, dim.Render("note: "+notice))
	}
	if cited := rpc.Cited(answer.String(), sources); len(cited) > 0 {
		fmt.Fprintln(stdout, "\nSources:")
		links := newLook(stdout).links
		home, _ := os.UserHomeDir() // "" leaves "~/..." paths unlinked
		for _, c := range cited {
			line := c.String()
			if links {
				line = rpc.Hyperlink(rpc.FileURL(c.Path, home), line)
			}
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}
