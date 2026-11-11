# Usage-report collector

This folder holds the collector for Meru's anonymous daily usage report: a
Cloudflare Worker and one D1 table (D1 is Cloudflare's hosted SQLite). It lives
at `https://meru-report.meru-workers.workers.dev` and answers three routes:

| Route | What it does |
|-------|--------------|
| `POST /v1/report` | stores one report, if the body is exactly what `merud` sends |
| `GET /totals` | the last 90 days of counts, as JSON |
| `GET /` | the same counts as an HTML table |

ARCHITECTURE.md, "Anonymous usage report", is the contract for every field. The
Worker stores no IP address. It reads the sender's address only as the
rate-limit key, and `wrangler.toml` turns Workers Logs off, because those logs
would record it. Cloudflare's network still sees each sender's address, as any
host does; `docs/faq/usage-report.md` says so.

## Files

| File | What it holds |
|------|---------------|
| `worker.js` | the Worker: the schema check, the insert, the totals and the daily clean-up |
| `worker.test.js` | tests for the schema check; run `node --test` here |
| `schema.sql` | the `reports` table |
| `wrangler.toml` | the deploy settings: the database, the rate limit, logs off, the daily cron |
| `check.sh` | the checks to run after each deploy |
| `package.json` | marks the folder as JavaScript modules, so `node --test` can import `worker.js` |

## What the Worker accepts

It stores a report only when the body is a JSON object with exactly these seven
keys and each value on its list. Anything else gets 400 and stores nothing.

| Key | Allowed values |
|-----|----------------|
| `v` | the number `1` |
| `meru` | a version such as `0.7.3`, at most 16 characters |
| `os` | `darwin`, `linux`, `windows` |
| `arch` | `arm64`, `amd64` |
| `profile` | `lite`, `full` |
| `age` | `week`, `month`, `year`, `older` |
| `questions` | `0`, `1-5`, `6-20`, `21+` |

The lists in `worker.js` must match `internal/usagereport`. A change to either
needs a new report version, and Meru shows its notice again before it sends one.

## First deploy

You need a Cloudflare account with two-factor sign-in on, and Node 20 or later
on your own machine. Run every command from this folder.

1. Sign in. This opens a browser to approve Wrangler's access:
   ```sh
   npx wrangler@4 login
   ```
2. Create the database:
   ```sh
   npx wrangler@4 d1 create meru-reports
   ```
   Copy the `database_id` it prints into `wrangler.toml`, in place of
   `REPLACE-WITH-THE-ID-FROM-wrangler-d1-create`. The ID isn't a secret, so
   commit it.
3. Create the table on Cloudflare's copy of the database. Without `--remote`,
   Wrangler changes only a local test copy:
   ```sh
   npx wrangler@4 d1 execute meru-reports --remote --file=schema.sql
   ```
4. Run the tests, then deploy:
   ```sh
   node --test
   npx wrangler@4 deploy
   ```
   Wrangler prints the address. It must be
   `https://meru-report.meru-workers.workers.dev`.
5. Run `./check.sh` (see below).
6. In the Cloudflare dashboard, open the Worker, then **Settings →
   Observability**, and confirm that logs are off.
7. Delete the test rows the checks wrote:
   ```sh
   npx wrangler@4 d1 execute meru-reports --remote --command "DELETE FROM reports"
   ```

## Checks after every deploy

```sh
./check.sh
```

It sends one good report and eleven bad requests or reads, and prints `ok` or
`FAIL` for each. It exits non-zero if any check fails. Pass an address to check
another copy, such as `./check.sh http://127.0.0.1:8787` for `wrangler dev`.
The good report writes one row, so delete test rows before launch.

The script leaves out the rate limit, because Cloudflare counts per location
and enforces the limit loosely: on 2026-11-11, the first 429 came after about
22 requests in a few seconds. To see it, send a burst by hand:

```sh
for i in $(seq 1 30); do curl -s -o /dev/null -w '%{http_code} ' -X POST https://meru-report.meru-workers.workers.dev/v1/report -d 'not json'; done; echo
```

To confirm the table holds no IP address or time of day:

```sh
npx wrangler@4 d1 execute meru-reports --remote --command "SELECT * FROM reports LIMIT 5"
```

## Trying it on your machine

```sh
npx wrangler@4 d1 execute meru-reports --local --file=schema.sql
npx wrangler@4 dev
```

`wrangler dev` serves on `http://127.0.0.1:8787`.

Point a `merud` at it with `[usage_report] url = "http://127.0.0.1:8787"` in
`config.toml`. A plain `go run` build calls itself `dev` and never sends, so
build one with a release version, the way `scripts/release.sh` does:

```sh
m=github.com/aarora79/meru
go build -ldflags "-X $m/internal/obs.releaseVersion=0.7.3 -X $m/internal/about.releaseVersion=0.7.3" -o /tmp/merud ./cmd/merud
```
