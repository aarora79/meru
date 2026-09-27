#!/usr/bin/env bash
# vault-digest.sh writes a weekly digest of your Obsidian vault by driving
# merud headless with `meru run --json`. It is a second agent built on Meru's
# harness: the instructions live in the question, and Meru's own prompt stays
# as it is. Every tool call goes through merud's dispatch, so `meru log` lists
# them afterwards.
#
# It needs merud running with the `obsidian` server connected and its
# obsidian_list_vaults, obsidian_search_vault and obsidian_read_note tools
# allowed (see docs/running.md), and jq on your PATH.
#
# Usage:
#
#	docs/examples/vault-digest.sh > digest.md
#
# The digest goes to stdout as Markdown. Each tool call and the turn's stats
# go to stderr. The script exits non-zero when the turn fails, and when the
# model answered without reading a note.

# -e stops on the first failing command, -u on an unset variable, and
# pipefail makes a pipeline fail when any command in it fails.
set -euo pipefail

question="Weekly digest of my obsidian vault. List the vaults, search the \
first vault by filename with the query '.', pick the three notes that look \
most recent, read each one, then answer with a Markdown list: one bullet per \
note with its name and a one-line summary. Nothing else."

# Keep the events in a file, so each jq pass below reads the same turn.
events=$(mktemp)
trap 'rm -f "$events"' EXIT

if ! meru run --json "$question" > "$events"; then
  jq -r 'select(.type == "error") | "vault-digest: merud: \(.error)"' "$events" >&2
  exit 1
fi

# Each tool call as it ended: its name and outcome, such as ok or declined.
jq -r 'select(.type == "tool_result") | "  \(.tool.name)  \(.tool.outcome)"' "$events" >&2

# The model writes a line or two before each tool round ("Let me read
# them."), and token events don't say which round they belong to. The
# answer is the text after the last tool_result: -s reads every event into
# one array, and the loop keeps only the tokens that follow it.
jq -rsj 'reduce .[] as $e (""; if $e.type == "tool_result" then ""
  elif $e.type == "token" then . + $e.text else . end)' "$events"
echo

# The last event, done, carries the turn's stats.
jq -r 'select(.type == "done") |
  "tokens \(.tokens_in) in, \(.tokens_out) out; " +
  "ttft \(.ttft_ms) ms, ttlt \(.ttlt_ms) ms, total \(.duration_ms) ms; " +
  "tpot \(.tpot_ms // 0 | . * 10 | round / 10) ms"' "$events" >&2

reads=$(jq -s '[.[] | select(.type == "tool_result" and .tool.outcome == "ok"
  and (.tool.name | endswith("obsidian_read_note")))] | length' "$events")
if [ "$reads" -eq 0 ]; then
  echo "vault-digest: the model read no note, so this digest has nothing behind it" >&2
  exit 1
fi
