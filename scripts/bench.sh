#!/usr/bin/env bash
# bench.sh runs the private benchmark: every question in bench/tasks.jsonl,
# against each model set, a few times over. `make bench` runs it, and
# `make bench-report` turns the results into docs/benchmarks/results.md.
# docs/benchmarks/README.md explains the benchmark and how to build a
# dataset of your own.
#
# bench/ is in .gitignore: the tasks quote the owner's own files, mail and
# calendar, so they and their results never leave this machine. Only the
# report, with no question or answer in it, is published.
#
# The benchmark runs in its own Meru home, bench/home/, so it never touches
# your real sessions, memories or meru.db:
#   1. On the first run it copies config.toml, secrets.toml, skills/ and
#      memory/ from ~/.meru, starts merud there, and waits while merud
#      indexes the [index] folders. It keeps that index as pristine.db.
#   2. Before each pass it puts the home back as step 1 left it: no
#      sessions, the copied memories, the pristine index. A pass can't
#      recall an earlier pass's answers.
#   3. Each pass starts merud, switches to one model set with
#      `meru model use`, runs `meru check --json` and stops merud.
#
# Your own merud must be stopped first: two answer models of about 30 GB
# each don't fit in memory together.
#
# Settings, from the environment:
#   SETS     model sets to run, space-separated (default: every set in config)
#   REPEATS  passes per set (default 3)
#   ONLY     ids or categories to run, as `meru check --only` takes them

# set -e stops at the first command that fails, -u treats an unset variable
# as an error, and -o pipefail makes a pipe fail when any command in it does.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bench="$root/bench"
home="$bench/home"
sock="$home/merud.sock"
tasks="$bench/tasks.jsonl"
results="$bench/results"
bin="$bench/bin"
repeats="${REPEATS:-3}"
only="${ONLY:-}"
merud_pid=""

# fail prints a message to stderr and stops the script.
fail() {
  echo "bench: $*" >&2
  exit 1
}

# say prints one progress line.
say() {
  echo "bench: $*"
}

# start_merud starts merud on the bench home in the background and waits
# up to a minute for it to answer.
start_merud() {
  "$bin/merud" -config "$home/config.toml" >>"$home/merud.out" 2>&1 &
  merud_pid=$!
  for _ in $(seq 1 60); do
    if "$bin/meru" -socket "$sock" ping >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "merud didn't answer on $sock within a minute; see $home/merud.out"
}

# stop_merud stops the bench merud, if one runs, and waits for it to exit.
stop_merud() {
  if [ -n "$merud_pid" ] && kill -0 "$merud_pid" 2>/dev/null; then
    kill -TERM "$merud_pid"
    wait "$merud_pid" 2>/dev/null || true
  fi
  merud_pid=""
}
# trap runs stop_merud when the script exits for any reason, Ctrl-C too, so
# no merud is left running.
trap stop_merud EXIT

# wait_for_scan waits until merud's first scan of the [index] folders ends.
wait_for_scan() {
  while "$bin/meru" -socket "$sock" index -status 2>/dev/null | grep -q '^Scanning: *yes'; do
    sleep 10
  done
}

# reset_home puts the bench home back to the state setup left it in.
reset_home() {
  rm -rf "$home/sessions" "$home/memory" "$home/checks-results"
  rm -f "$home/meru.db" "$home/meru.db-wal" "$home/meru.db-shm"
  cp -R "$home/memory.snapshot" "$home/memory"
  cp "$home/pristine.db" "$home/meru.db"
}

# setup_home makes the bench home on the first run.
setup_home() {
  say "first run: making $home from ~/.meru"
  mkdir -p "$home"
  cp "$HOME/.meru/config.toml" "$home/config.toml"
  if [ -f "$HOME/.meru/secrets.toml" ]; then
    cp "$HOME/.meru/secrets.toml" "$home/secrets.toml"
    chmod 600 "$home/secrets.toml"
  fi
  if [ -d "$HOME/.meru/skills" ]; then
    cp -R "$HOME/.meru/skills" "$home/skills"
  fi
  mkdir -p "$home/memory"
  if [ -d "$HOME/.meru/memory" ]; then
    cp -R "$HOME/.meru/memory/." "$home/memory/"
  fi
  cp -R "$home/memory" "$home/memory.snapshot"

  say "indexing the [index] folders into a fresh meru.db; this takes a while once"
  start_merud
  sleep 5
  wait_for_scan
  stop_merud
  rm -rf "$home/sessions"
  cp "$home/meru.db" "$home/pristine.db"
  say "index ready"
}

[ -f "$tasks" ] || fail "no $tasks; docs/benchmarks/README.md says how to write one"
if meru ping >/dev/null 2>&1; then
  fail "your own merud is running; stop it first (pkill merud), since two answer models don't fit in memory together"
fi

say "building merud and meru"
mkdir -p "$bin" "$results"
(cd "$root" && go build -o "$bin/" ./cmd/merud ./cmd/meru)

[ -f "$home/pristine.db" ] || setup_home

# With no SETS given, run every set that `meru model` lists. Its rows start
# with the set's name, after "→ " on the set in use.
if [ -z "${SETS:-}" ]; then
  reset_home
  start_merud
  SETS=$("$bin/meru" -socket "$sock" model | awk 'NR > 1 && NF >= 4 { sub(/^→ */, ""); print $1 }' | grep -v '^meru$' | tr '\n' ' ')
  stop_merud
fi
[ -n "$SETS" ] || fail "config.toml names no model sets ([[models.sets]])"

stamp=$(date +%Y%m%d-%H%M%S)
for set in $SETS; do
  for pass in $(seq 1 "$repeats"); do
    out="$results/$stamp-$set-$pass.jsonl"
    say "$set, pass $pass of $repeats -> ${out#"$root"/}"
    reset_home
    start_merud
    "$bin/meru" -socket "$sock" model use "$set" >/dev/null
    args=(check "$tasks" --json)
    if [ -n "$only" ]; then
      args+=(--only "$only")
    fi
    # meru check exits 1 when any question fails, which is normal here.
    "$bin/meru" -socket "$sock" "${args[@]}" >"$out" || true
    passed=$(grep -c '"pass":true' "$out" || true)
    total=$(grep -c '"id":' "$out" || true)
    say "$set, pass $pass: $passed of $total passed"
    stop_merud
  done
done
say "done; run make bench-report to write docs/benchmarks/results.md"
