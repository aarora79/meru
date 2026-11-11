// The collector for Meru's anonymous daily usage report, as a Cloudflare
// Worker. It answers three routes and nothing else:
//
//   POST /v1/report  stores one report, if the body is exactly what merud sends
//   GET  /totals     the counts, as JSON, for anyone
//   GET  /           the same counts as HTML tables
//
// ARCHITECTURE.md, "Anonymous usage report", is the contract for every field.
// The Worker never stores an IP address. It reads the sender's address only
// as the rate-limit key, in memory, and wrangler.toml turns Workers Logs off,
// because those logs would record it with the request headers.
//
// No report carries an ID, so the counts across days come from three flags
// a version 2 report carries: week_first, month_first and first mark an
// install's first report of the ISO week, of the UTC month, and ever.
// Counting the rows with a flag set counts each install once in that window.

// The versions merud sends, each with its exact keys and the values each
// may take. These lists must match internal/usagereport in Meru; a new
// field or value on either side needs a new report version. Version 1 came
// from Meru v0.7.6, version 2 from v0.7.7 on.
const COMMON = {
  os: ["darwin", "linux", "windows"],
  arch: ["arm64", "amd64"],
  profile: ["lite", "full"],
  age: ["week", "month", "year", "older"],
  questions: ["0", "1-5", "6-20", "21+"],
};
const SCHEMAS = {
  1: {
    keys: ["age", "arch", "meru", "os", "profile", "questions", "v"],
    allowed: COMMON,
    flags: [],
  },
  2: {
    keys: [
      "active_days", "age", "arch", "first", "meru", "month_first", "os",
      "previous", "profile", "questions", "streak", "v", "week_first",
    ],
    allowed: {
      ...COMMON,
      active_days: ["0", "1-2", "3-5", "6-7"],
      streak: ["1", "2-4", "5-12", "13+"],
    },
    flags: ["week_first", "month_first", "first"],
  },
};
const RELEASE = /^\d+\.\d+\.\d+$/;
const MAX_BODY = 1024; // a version 2 report is about 250 bytes
const DAYS_SHOWN = 90;
const DAYS_KEPT = 400;

// release reports whether v is a release number such as "0.7.7".
function release(v) {
  return typeof v === "string" && v.length <= 16 && RELEASE.test(v);
}

// parse returns the report when text is a JSON object with exactly the keys
// of the version it names and every value on its list. It returns null for
// anything else, and the caller answers 400.
export function parse(text) {
  let r;
  try {
    r = JSON.parse(text);
  } catch {
    return null;
  }
  if (r === null || typeof r !== "object" || Array.isArray(r)) return null;
  const schema = Object.hasOwn(SCHEMAS, r.v) && typeof r.v === "number" ? SCHEMAS[r.v] : null;
  if (!schema) return null;
  if (Object.keys(r).sort().join() !== [...schema.keys].sort().join()) return null;
  if (!release(r.meru)) return null;
  for (const [key, values] of Object.entries(schema.allowed)) {
    if (!values.includes(r[key])) return null;
  }
  for (const key of schema.flags) {
    if (typeof r[key] !== "boolean") return null;
  }
  if (r.v >= 2 && r.previous !== "none" && !release(r.previous)) return null;
  return r;
}

// empty returns a response with no body.
function empty(status) {
  return new Response(null, { status });
}

// flag turns a version 2 flag into 1 or 0 for D1, and a version 1 report's
// missing flag into null, which no count adds up.
function flag(r, key) {
  return r.v >= 2 ? (r[key] ? 1 : 0) : null;
}

// report handles POST /v1/report: size, shape, rate limit, then the insert.
// The rate limit comes after the shape: it protects the database's daily
// writes, and a body parse refuses writes nothing.
async function report(request, env) {
  const length = Number(request.headers.get("Content-Length") || "0");
  if (length > MAX_BODY) return empty(413);
  const text = await request.text();
  if (text.length > MAX_BODY) return empty(413);

  const r = parse(text);
  if (!r) return empty(400);

  // The address is the rate-limit key and nothing else. It is never logged
  // and never written to the database.
  const ip = request.headers.get("CF-Connecting-IP") || "unknown";
  const { success } = await env.LIMIT.limit({ key: ip });
  if (!success) return empty(429);

  const v2 = r.v >= 2;
  const writes = [
    env.DB.prepare(
      "INSERT INTO reports (day, v, meru, os, arch, profile, age, questions, " +
        "active_days, streak, previous, week_first, month_first, first) " +
        "VALUES (date('now'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
    ).bind(
      r.v, r.meru, r.os, r.arch, r.profile, r.age, r.questions,
      v2 ? r.active_days : null, v2 ? r.streak : null, v2 ? r.previous : null,
      flag(r, "week_first"), flag(r, "month_first"), flag(r, "first")
    ),
  ];
  // The all-time total lives in a counter, since the rows go after
  // DAYS_KEPT days and a sum over them would shrink.
  if (v2 && r.first) {
    writes.push(
      env.DB.prepare(
        "INSERT INTO counters (name, n) VALUES ('installs', 1) " +
          "ON CONFLICT (name) DO UPDATE SET n = n + 1"
      )
    );
  }
  await env.DB.batch(writes);
  return empty(204);
}

// The queries behind the totals. Each reads only the last DAYS_SHOWN days.
// date(day, 'weekday 0', '-6 days') is the Monday that starts day's ISO
// week: 'weekday 0' moves to the Sunday that ends it.
const WEEK = "date(day, 'weekday 0', '-6 days')";
const QUERIES = {
  daily:
    "SELECT day, COUNT(*) AS active, COALESCE(SUM(first), 0) AS new " +
    "FROM reports WHERE day >= date('now', ?1) GROUP BY day ORDER BY day DESC",
  weekly:
    `SELECT ${WEEK} AS week, COALESCE(SUM(week_first), 0) AS active, COALESCE(SUM(first), 0) AS new ` +
    "FROM reports WHERE day >= date('now', ?1) GROUP BY week ORDER BY week DESC",
  monthly:
    "SELECT substr(day, 1, 7) AS month, COALESCE(SUM(month_first), 0) AS active, COALESCE(SUM(first), 0) AS new " +
    "FROM reports WHERE day >= date('now', ?1) GROUP BY month ORDER BY month DESC",
};

// THIS_WEEK counts the installs active this ISO week by one field at a
// time, from each install's first report of the week. Each count stands
// alone: the page never crosses two fields, which on a quiet week could
// narrow a row down to one install.
const THIS_WEEK = ["meru", "os", "arch", "profile", "streak", "active_days"];

// counts gathers every number the page and /totals show.
async function counts(env) {
  const since = `-${DAYS_SHOWN} days`;
  const out = {};
  for (const [name, sql] of Object.entries(QUERIES)) {
    out[name] = (await env.DB.prepare(sql).bind(since).all()).results;
  }
  const total = await env.DB.prepare("SELECT n FROM counters WHERE name = 'installs'").first();
  out.total_installs = total ? total.n : 0;
  out.this_week = {};
  for (const field of THIS_WEEK) {
    out.this_week[field] = (
      await env.DB.prepare(
        `SELECT ${field} AS value, COUNT(*) AS installs FROM reports ` +
          `WHERE week_first = 1 AND ${WEEK} = date('now', 'weekday 0', '-6 days') ` +
          `GROUP BY ${field} ORDER BY installs DESC`
      ).all()
    ).results;
  }
  // Upgrade paths over the last 30 days: the version an install reported
  // last, and the one it reports now, once per install and month.
  out.upgrades = (
    await env.DB.prepare(
      "SELECT previous AS from_version, meru AS to_version, COUNT(*) AS installs FROM reports " +
        "WHERE month_first = 1 AND previous != 'none' AND previous != meru AND day >= date('now', '-30 days') " +
        "GROUP BY previous, meru ORDER BY installs DESC"
    ).all()
  ).results;
  return out;
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
  return String(s ?? "").replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

// table writes rows as an HTML table under heading, with the columns cols.
function table(heading, cols, rows) {
  const head = cols.map(([, label]) => `<th>${escape(label)}</th>`).join("");
  const body = rows
    .map((r) => "<tr>" + cols.map(([key]) => `<td>${escape(r[key])}</td>`).join("") + "</tr>")
    .join("\n");
  return `<h2>${escape(heading)}</h2>\n<table><thead><tr>${head}</tr></thead><tbody>\n${body}\n</tbody></table>`;
}

// page handles GET /: the counts as plain tables.
async function page(env) {
  const c = await counts(env);
  const byField = THIS_WEEK.map((f) =>
    table(`This week by ${f.replace("_", " ")}`, [["value", f], ["installs", "Installs"]], c.this_week[f])
  ).join("\n");
  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Meru usage totals</title>
<style>
  body { font: 15px/1.5 system-ui, sans-serif; margin: 2rem auto; max-width: 52rem; padding: 0 1rem; }
  table { border-collapse: collapse; width: 100%; margin-bottom: 1.5rem; }
  th, td { padding: 0.3rem 0.6rem; border-bottom: 1px solid #ddd; text-align: left; }
  td:last-child, th:last-child { text-align: right; }
</style>
</head>
<body>
<h1>Meru usage totals</h1>
<p>Each install sends at most one anonymous report a day, with no ID. An install
counts as active in a day, a week or a month when it sent a report in it, and as
new on the day of its first report. Weekly and monthly counts come from reports
of Meru v0.7.7 and later. Days are UTC. <a href="/totals">The same numbers as JSON.</a></p>
<p><strong>Installs so far: ${escape(c.total_installs)}</strong></p>
${table("By day", [["day", "Day"], ["active", "Active"], ["new", "New"]], c.daily)}
${table("By ISO week, starting Monday", [["week", "Week"], ["active", "Active"], ["new", "New"]], c.weekly)}
${table("By month", [["month", "Month"], ["active", "Active"], ["new", "New"]], c.monthly)}
${byField}
${table("Upgrades in the last 30 days", [["from_version", "From"], ["to_version", "To"], ["installs", "Installs"]], c.upgrades)}
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
  // older than DAYS_KEPT days. The all-time counter stays.
  async scheduled(controller, env) {
    await env.DB.prepare("DELETE FROM reports WHERE day < date('now', ?)")
      .bind(`-${DAYS_KEPT} days`)
      .run();
  },
};
