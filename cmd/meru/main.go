// Command meru is the thin client for merud. It sends one request over the
// Unix socket at ~/.meru/merud.sock and prints the reply. It holds no model or
// store logic; merud does the work. See ARCHITECTURE.md, "The shape: daemon +
// thin client".
//
// Usage:
//
//	meru [-socket path] "question"   ask one question; the answer streams to stdout
//	meru [-socket path] ping          check that merud is up
//	meru [-socket path] chat          open the terminal UI
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
  meru "question"   ask one question
  meru ping         check that merud is up
  meru chat         open the terminal UI

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
	case flags.NArg() == 1 && flags.Arg(0) == "ping":
		err = ping(ctx, *socket, stdout)
	case flags.NArg() == 1 && flags.Arg(0) == "chat":
		// tui.Run starts the Bubble Tea terminal UI (internal/tui).
		err = tui.Run(ctx, *socket, chatInfo())
	default:
		// Words after the flags form the question, so quotes are optional:
		// meru what time is it
		err = ask(ctx, *socket, strings.Join(flags.Args(), " "), stdout)
	}

	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(stderr) // end the half-printed line
		return exitInterrupted
	case err != nil:
		fmt.Fprintf(stderr, "meru: %v\n", err)
		return exitError
	}
	return exitOK
}

// chatInfo reads the profile and main model from the default config file for
// the chat screen's header. They only label the screen, so a config that
// doesn't load leaves the header without them instead of stopping the chat;
// merud reports config errors when it starts.
func chatInfo() tui.Info {
	path, err := config.DefaultPath()
	if err != nil {
		return tui.Info{}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return tui.Info{}
	}
	return tui.Info{Profile: cfg.Profile, Model: cfg.Models.Main}
}

// ping asks merud whether it is up and prints the answer.
func ping(ctx context.Context, socket string, stdout io.Writer) error {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpPing}) {
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
// in, ending with a newline. It fails when merud can't be reached or replies
// with an error.
func ask(ctx context.Context, socket, question string, stdout io.Writer) error {
	req := rpc.Request{Op: rpc.OpAsk, Text: question, Source: rpc.SourceCLI}
	printed := false
	endsInNewline := false
	for ev, err := range rpc.Do(ctx, socket, req) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventToken:
			if _, err := io.WriteString(stdout, ev.Text); err != nil {
				return err // stdout closed, as with `meru ... | head -1`
			}
			printed = true
			endsInNewline = strings.HasSuffix(ev.Text, "\n")
		case rpc.EventError:
			if printed && !endsInNewline {
				fmt.Fprintln(stdout)
			}
			return errors.New(ev.Error)
		}
	}
	if printed && !endsInNewline {
		fmt.Fprintln(stdout)
	}
	return nil
}
