#!/bin/sh
# check.sh sends the collector one good report and a set of bad ones, and
# says whether each got the status it should. Run it after every deploy:
#   ./check.sh                      the live collector
#   ./check.sh http://127.0.0.1:8787   wrangler dev on this machine
# The good reports write two rows with Meru version 0.0.0, which no real
# install sends, and first false, so they never count as new installs. Take
# them out after a run with
#   npx wrangler@4 d1 execute meru-reports --remote --command "DELETE FROM reports WHERE meru = '0.0.0'"

base="${1:-https://meru-report.meru-workers.workers.dev}"
u="$base/v1/report"
good='{"v":1,"meru":"0.0.0","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5"}'
good2='{"v":2,"meru":"0.0.0","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5","active_days":"1-2","streak":"1","previous":"none","week_first":false,"month_first":false,"first":false}'
failed=0

# expect NAME WANT CURL-ARGS... runs curl and compares the status code.
expect() {
  name=$1 want=$2
  shift 2
  got=$(curl -s -o /dev/null -w '%{http_code}' "$@")
  if [ "$got" = "$want" ]; then
    echo "ok    $name ($got)"
  else
    echo "FAIL  $name: got $got, want $want"
    failed=1
  fi
}

expect "good v1 report"      204 -X POST "$u" -d "$good"
expect "good v2 report"      204 -X POST "$u" -d "$good2"
expect "v2 flag as a string" 400 -X POST "$u" -d '{"v":2,"meru":"0.0.0","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5","active_days":"1-2","streak":"1","previous":"none","week_first":"no","month_first":false,"first":false}'
expect "v2 with an ID"       400 -X POST "$u" -d '{"v":2,"id":"x","meru":"0.0.0","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5","active_days":"1-2","streak":"1","previous":"none","week_first":false,"month_first":false,"first":false}'
expect "extra field"         400 -X POST "$u" -d '{"v":1,"meru":"0.7.5","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5","id":"x"}'
expect "missing field"       400 -X POST "$u" -d '{"v":1,"meru":"0.7.5","os":"darwin","arch":"arm64","profile":"lite","age":"week"}'
expect "v as a string"       400 -X POST "$u" -d '{"v":"1","meru":"0.7.5","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5"}'
expect "fork version"        400 -X POST "$u" -d '{"v":1,"meru":"0.7.5-acme","os":"darwin","arch":"arm64","profile":"lite","age":"week","questions":"1-5"}'
expect "unknown os"          400 -X POST "$u" -d '{"v":1,"meru":"0.7.5","os":"freebsd","arch":"arm64","profile":"lite","age":"week","questions":"1-5"}'
expect "not JSON"            400 -X POST "$u" -d 'not json'
expect "too big"             413 -X POST "$u" -d "$(head -c 2000 /dev/zero | tr '\0' 'a')"
expect "GET on report route" 405 "$u"
expect "unknown path"        404 "$base/nope"
expect "totals page"         200 "$base/"
expect "totals JSON"         200 "$base/totals"

exit $failed
