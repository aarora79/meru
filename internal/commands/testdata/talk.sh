#!/bin/sh
# talk.sh is the program the commands tests run. Its first argument picks
# what it does; the tests in run_unix_test.go describe each mode.
case "$1" in
talk)
	# Write $2 to stdout and stderr, then exit with $3 (0 by default).
	echo "out: $2"
	echo "err: $2" >&2
	exit "${3:-0}"
	;;
args)
	# Print each argument on its own line, so a test can count them.
	shift
	for a in "$@"; do
		echo "[$a]"
	done
	;;
env)
	# Print the whole environment.
	env
	;;
pwd)
	pwd
	;;
flood)
	# Write 2 MB to stdout, past the 1 MiB cap.
	head -c 2000000 /dev/zero | tr '\0' 'a'
	;;
hang)
	# Start a child that outlives nothing on its own, write its process ID
	# to the file $2, and wait for it.
	sleep 60 &
	echo $! >"$2"
	wait
	;;
esac
