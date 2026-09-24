// This file holds `meru index`: it asks merud to rescan the [index]
// folders, or one folder in them, or to say what the index holds, and
// prints the reply. merud does the indexing; this side only sends the
// request and formats the answer.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// indexCmd runs `meru index [-status] [folder]`. args are the words after
// "index". It fails on bad usage, when merud can't be reached, and when
// merud refuses, for example for a folder outside [index] folders; merud's
// message says what to change.
func indexCmd(ctx context.Context, socket string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("meru index", flag.ContinueOnError)
	flags.SetOutput(stderr)
	status := flags.Bool("status", false, "show what the search index holds instead of indexing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch {
	case *status && flags.NArg() > 0:
		return errors.New("usage: meru index -status (with no folder)")
	case *status:
		return indexStatus(ctx, socket, stdout)
	case flags.NArg() > 1:
		return errors.New("usage: meru index [folder]: one folder at a time")
	}

	req := rpc.Request{Op: rpc.OpIndex}
	if flags.NArg() == 1 {
		// merud runs in another folder, so a relative path must become
		// absolute here, against the folder meru runs in.
		abs, err := filepath.Abs(flags.Arg(0))
		if err != nil {
			return fmt.Errorf("index %s: %w", flags.Arg(0), err)
		}
		req.Path = abs
	}
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventProgress:
			// News goes to stderr, so stdout holds only the report.
			fmt.Fprintln(stderr, ev.Text)
		case rpc.EventReport:
			if ev.Report != nil {
				fmt.Fprintln(stdout, reportLine(*ev.Report))
			}
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	return nil
}

// indexStatus asks merud what the index holds and prints it.
func indexStatus(ctx context.Context, socket string, stdout io.Writer) error {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpIndexStatus}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventStatus:
			if ev.Status != nil {
				fmt.Fprint(stdout, statusText(*ev.Status))
			}
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	return nil
}

// reportLine writes one index run as a sentence, such as
// "3 files indexed (5 chunks), 9 unchanged, 1 removed, 0 failed, 2 skipped, in 1.2s".
func reportLine(r rpc.IndexReport) string {
	return fmt.Sprintf("%s indexed (%d chunks), %d unchanged, %d removed, %d failed, %d skipped, in %s",
		plural(r.Indexed, "file"), r.Chunks, r.Unchanged, r.Removed, r.Failed, r.Skipped,
		time.Duration(r.DurationMillis)*time.Millisecond)
}

// statusText writes the index status as a few labelled lines:
//
//	Folders:    ~/notes, ~/papers
//	Index:      12 files, 87 chunks, 87 vectors
//	Scanning:   no
//	Last scan:  2026-09-23T10:15:00-04:00, 3 files indexed (5 chunks), ...
func statusText(s rpc.IndexStatus) string {
	var b strings.Builder
	folders := strings.Join(s.Folders, ", ")
	if folders == "" {
		folders = "none; list them under [index] folders in ~/.meru/config.toml and restart merud"
	}
	fmt.Fprintf(&b, "Folders:    %s\n", folders)
	fmt.Fprintf(&b, "Index:      %s, %d chunks, %d vectors\n", plural(s.Documents, "file"), s.Chunks, s.Vectors)
	if s.Vectors < s.Chunks {
		fmt.Fprintf(&b, "            %d chunks still need a vector; keyword search covers them until then\n", s.Chunks-s.Vectors)
	}
	scanning := "no"
	if s.Scanning {
		scanning = "yes"
	}
	fmt.Fprintf(&b, "Scanning:   %s\n", scanning)
	if s.LastScan != nil {
		fmt.Fprintf(&b, "Last scan:  %s, %s\n", s.LastScanAt, reportLine(*s.LastScan))
	}
	if s.LastError != "" {
		fmt.Fprintf(&b, "Last error: %s\n", s.LastError)
	}
	return b.String()
}

// plural writes n and a noun, adding an "s" unless n is 1: "1 file",
// "3 files".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
