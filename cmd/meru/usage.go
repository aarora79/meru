// This file holds `meru usage`: it asks merud how much you have used Meru
// and prints one column per window of time, from the last hour to all
// time. merud counts; this side only lines the numbers up.

package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/aarora79/meru/internal/rpc"
)

// usageCmd runs `meru usage`. It fails when merud can't be reached or
// replies with an error, as a merud older than OpUsage does.
func usageCmd(ctx context.Context, socket string, stdout io.Writer) error {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpUsage}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventUsage:
			if err := writeUsage(stdout, ev.Usage); err != nil {
				return err
			}
		case rpc.EventError:
			return fmt.Errorf("merud gave no usage numbers: %s", ev.Error)
		}
	}
	return nil
}

// writeUsage prints the usage table and the note on calendar windows:
//
//	                  1h   today     week   month     30d     all
//	sessions           1       2        5      12      14      30
//	questions          4       9       31      88      97     212
//	…
//
//	Today, week and month follow the local calendar.
//
// rpc.UsageTable gives the cells, the same ones `meru chat` shows. It fails
// only when w does.
func writeUsage(w io.Writer, windows []rpc.UsageWindow) error {
	// A tabwriter lines up columns: each tab ends a cell, and Flush pads
	// every cell to the widest in its column, plus two spaces. AlignRight
	// puts the padding on the left, so the numbers line up on their last
	// digit and the two spaces fall between columns. Labels read better
	// flush left, so they stay out of the tabwriter and join each line
	// afterwards.
	rows := rpc.UsageTable(windows)
	var numbers strings.Builder
	tw := tabwriter.NewWriter(&numbers, 0, 0, 2, ' ', tabwriter.AlignRight)
	labelWidth := 0
	for _, row := range rows {
		labelWidth = max(labelWidth, len(row[0]))
		// Each cell, the last one too, ends in a tab, so every column is
		// padded.
		for _, cell := range row[1:] {
			fmt.Fprintf(tw, "%s\t", cell)
		}
		fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	var b strings.Builder
	lines := strings.Split(strings.TrimSuffix(numbers.String(), "\n"), "\n")
	for i, line := range lines {
		// %-*s pads the label with spaces on the right to labelWidth; the
		// * takes the width from the argument before the label.
		b.WriteString(strings.TrimRight(fmt.Sprintf("%-*s%s", labelWidth, rows[i][0], line), " ") + "\n")
	}
	b.WriteString("\n" + rpc.UsageNote + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}
