// The collector for Meru's anonymous daily usage report, as a Cloudflare
// Worker. It answers three routes and nothing else:
//
//   POST /v1/report  stores one report, if the body is exactly what merud sends
//   GET  /totals     the last 90 days of counts, as JSON, for anyone
//   GET  /           the same counts as an HTML table
//
// ARCHITECTURE.md, "Anonymous usage report", is the contract for every field.
// The Worker never stores an IP address. It reads the sender's address only
// as the rate-limit key, in memory, and wrangler.toml turns Workers Logs off,
// because those logs would record it with the request headers.

// The fields and the values each may take. These lists must match
// internal/usagereport in Meru; a new value on either side needs a new
// report version.
const VERSION = 1;
const ALLOWED = {
  os: ["darwin", "linux", "windows"],
  arch: ["arm64", "amd64"],
  profile: ["lite", "full"],
  age: ["week", "month", "year", "older"],
  questions: ["0", "1-5", "6-20", "21+"],
};
const KEYS = ["age", "arch", "meru", "os", "profile", "questions", "v"]; // sorted
const MAX_BODY = 512; // a real report is about 100 bytes
const DAYS_SHOWN = 90;
const DAYS_KEPT = 400;

// parse returns the report when text is a JSON object with exactly the
// fields merud sends and every value on its list. It returns null for
// anything else, and the caller answers 400.
export function parse(text) {
  let r;
  try {
    r = JSON.parse(text);
  } catch {
    return null;
  }
  if (r === null || typeof r !== "object" || Array.isArray(r)) return null;
  if (Object.keys(r).sort().join() !== KEYS.join()) return null;
  if (r.v !== VERSION) return null;
  if (typeof r.meru !== "string" || r.meru.length > 16) return null;
  if (!/^\d+\.\d+\.\d+$/.test(r.meru)) return null;
  for (const [key, values] of Object.entries(ALLOWED)) {
    if (!values.includes(r[key])) return null;
  }
  return r;
}

// empty returns a response with no body.
function empty(status) {
  return new Response(null, { status });
}

// report handles POST /v1/report: rate limit, size, shape, then one insert.
async function report(request, env) {
  // The address is the rate-limit key and nothing else. It is never logged
  // and never written to the database.
  const ip = request.headers.get("CF-Connecting-IP") || "unknown";
  const { success } = await env.LIMIT.limit({ key: ip });
  if (!success) return empty(429);

  const length = Number(request.headers.get("Content-Length") || "0");
  if (length > MAX_BODY) return empty(413);
  const text = await request.text();
  if (text.length > MAX_BODY) return empty(413);

  const r = parse(text);
  if (!r) return empty(400);

  await env.DB.prepare(
    "INSERT INTO reports (day, v, meru, os, arch, profile, age, questions) " +
      "VALUES (date('now'), ?, ?, ?, ?, ?, ?, ?)"
  )
    .bind(r.v, r.meru, r.os, r.arch, r.profile, r.age, r.questions)
    .run();
  return empty(204);
}

// counts returns one row per day, Meru version, system, chip and model set
// over the last DAYS_SHOWN days, newest first.
async function counts(env) {
  const { results } = await env.DB.prepare(
    "SELECT day, meru, os, arch, profile, COUNT(*) AS installs FROM reports " +
      "WHERE day >= date('now', ?) " +
      "GROUP BY day, meru, os, arch, profile " +
      "ORDER BY day DESC, installs DESC"
  )
    .bind(`-${DAYS_SHOWN} days`)
    .all();
  return results;
}

// totals handles GET /totals.
async function totals(env) {
  return Response.json(await counts(env), {
    headers: { "Cache-Control": "public, max-age=3600" },
  });
}

// escape makes a value safe to put in HTML. Every stored value already comes
// from a short list, but the page shouldn't depend on that.
function escape(s) {
  return String(s).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

// page handles GET /: the same counts as a plain table.
async function page(env) {
  const rows = (await counts(env))
    .map(
      (r) =>
        `<tr><td>${escape(r.day)}</td><td>${escape(r.meru)}</td>` +
        `<td>${escape(r.os)}</td><td>${escape(r.arch)}</td>` +
        `<td>${escape(r.profile)}</td><td>${escape(r.installs)}</td></tr>`
    )
    .join("\n");
  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Meru usage totals</title>
<style>
  body { font: 15px/1.5 system-ui, sans-serif; margin: 2rem auto; max-width: 52rem; padding: 0 1rem; }
  table { border-collapse: collapse; width: 100%; }
  th, td { padding: 0.3rem 0.6rem; border-bottom: 1px solid #ddd; text-align: left; }
  td:last-child, th:last-child { text-align: right; }
</style>
</head>
<body>
<h1>Meru usage totals</h1>
<p>Installs that reported that day, over the last ${DAYS_SHOWN} days (UTC).
Each install sends at most one anonymous report a day, with no ID.
A computer that was off or asleep all day doesn't appear.
<a href="/totals">The same numbers as JSON.</a></p>
<table>
<thead><tr><th>Day</th><th>Meru</th><th>System</th><th>Chip</th><th>Models</th><th>Installs</th></tr></thead>
<tbody>
${rows}
</tbody>
</table>
</body>
</html>`;
  return new Response(html, {
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "public, max-age=3600",
      "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'",
    },
  });
}

export default {
  // fetch answers every request. A route not listed gets 404, and the right
  // path with the wrong method gets 405.
  async fetch(request, env) {
    const { pathname } = new URL(request.url);
    if (pathname === "/v1/report") {
      return request.method === "POST" ? report(request, env) : empty(405);
    }
    if (pathname === "/totals" || pathname === "/") {
      if (request.method !== "GET") return empty(405);
      return pathname === "/" ? page(env) : totals(env);
    }
    return empty(404);
  },

  // scheduled runs once a day from the cron in wrangler.toml and deletes rows
  // older than DAYS_KEPT days.
  async scheduled(controller, env) {
    await env.DB.prepare("DELETE FROM reports WHERE day < date('now', ?)")
      .bind(`-${DAYS_KEPT} days`)
      .run();
  },
};
