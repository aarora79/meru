// This file tests `meru usage` against an in-process rpc server that plays
// merud.

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestUsageCommand(t *testing.T) {
	windows := []rpc.UsageWindow{
		{Name: rpc.Usage1h, Sessions: 1, Turns: 4, TokensIn: 18_000, TokensOut: 2_100, ActiveMillis: 134_000, Docs: 3, ToolCalls: 1},
		{Name: rpc.UsageToday, Sessions: 2, Turns: 9, TokensIn: 41_200, TokensOut: 5_300, ActiveMillis: 301_000, Docs: 7, ToolCalls: 2},
		{Name: rpc.UsageWeek, Sessions: 5, Turns: 31, TokensIn: 150_000, TokensOut: 19_400, ActiveMillis: 1_210_000, Docs: 22, ToolCalls: 6},
		{Name: rpc.UsageMonth, Sessions: 12, Turns: 88, TokensIn: 420_000, TokensOut: 61_000, ActiveMillis: 3_700_000, Docs: 51, ToolCalls: 14},
		{Name: rpc.Usage30d, Sessions: 14, Turns: 97, TokensIn: 468_000, TokensOut: 66_500, ActiveMillis: 4_020_000, Docs: 55, ToolCalls: 15},
		{Name: rpc.UsageLifetime, Sessions: 30, Turns: 212, TokensIn: 1_400_000, TokensOut: 180_000, ActiveMillis: 11_100_000, Docs: 140, ToolCalls: 40},
	}
	table := `                  1h   today     week   month     30d     all
sessions           1       2        5      12      14      30
questions          4       9       31      88      97     212
tokens in        18k     41k     150k    420k    468k    1.4M
tokens out      2.1k    5.3k      19k     61k     66k    180k
active time   2m 14s  5m 01s  20m 10s  1h 01m  1h 07m  3h 05m
docs touched       3       7       22      51      55     140
tool calls         1       2        6      14      15      40

Today, week and month follow the local calendar.
`
	tests := []struct {
		name     string
		args     []string
		handler  rpc.Handler
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"table", []string{"usage"}, usageHandler(t, windows, ""), 0, table, ""},
		{"older merud", []string{"usage"}, usageHandler(t, nil, `unknown op "usage"`), 1, "",
			`meru: merud gave no usage numbers: unknown op "usage"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := startServer(t, tt.handler)
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}

// usageHandler plays merud for OpUsage: it replies with windows, or with
// an error event when refuse is set, as a merud without OpUsage does.
func usageHandler(t *testing.T, windows []rpc.UsageWindow, refuse string) rpc.Handler {
	return func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		if req.Op != rpc.OpUsage {
			t.Errorf("op = %q, want usage", req.Op)
		}
		if refuse != "" {
			return emit(rpc.Event{Type: rpc.EventError, Error: refuse})
		}
		return emit(rpc.Event{Type: rpc.EventUsage, Usage: windows})
	}
}
