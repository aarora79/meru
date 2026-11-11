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

// The page draws its charts itself, as SVG and HTML, so it loads nothing from
// anywhere and its Content-Security-Policy stays "default-src 'none'". The
// colours are the dataviz reference palette's: series 1 (blue) for active
// installs, series 2 (orange) for new ones, each with its own step for a dark
// screen. A hover shows a value through SVG <title> and HTML title, which need
// no script. Every chart has its numbers in a table under "Show the numbers".

// dayRange returns every UTC day, as YYYY-MM-DD, from n-1 days ago to today.
function dayRange(n) {
  const out = [];
  const today = new Date();
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - i));
    out.push(d.toISOString().slice(0, 10));
  }
  return out;
}

// niceMax rounds m up to a tidy top for a y-axis: 1, 2, 5 or 10 times a power
// of ten, and at least 1 so an empty chart still has a scale.
function niceMax(m) {
  if (m <= 1) return 1;
  const p = 10 ** Math.floor(Math.log10(m));
  for (const f of [1, 2, 5, 10]) if (f * p >= m) return f * p;
  return 10 * p;
}

// shortDay writes "2026-11-11" as "11 Nov".
function shortDay(day) {
  const d = new Date(day + "T00:00:00Z");
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
}

// lineChart draws series of daily values over days as SVG: a recessive grid
// with 0, half and the top on the y-axis, the first, middle and last day on the
// x-axis, a 2px line per series with a marker on each day that has data, and a
// direct label at each line's end. series is [{name, css, values}], values one
// number per day.
function lineChart(days, series, label) {
  const W = 720, H = 240, L = 48, R = 132, T = 16, B = 40;
  const max = niceMax(Math.max(0, ...series.flatMap((s) => s.values)));
  const x = (i) => L + (days.length === 1 ? (W - L - R) / 2 : (i * (W - L - R)) / (days.length - 1));
  const y = (v) => T + (H - T - B) * (1 - v / max);
  const grid = [0, max / 2, max]
    .map((v) => `<line class="grid" x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/>` +
      `<text class="tick" x="${L - 8}" y="${y(v) + 4}" text-anchor="end">${escape(Number.isInteger(v) ? v : v.toFixed(1))}</text>`)
    .join("");
  const ticks = [0, Math.floor((days.length - 1) / 2), days.length - 1]
    .filter((v, i, a) => a.indexOf(v) === i)
    .map((i) => `<text class="tick" x="${x(i)}" y="${H - 10}" text-anchor="middle">${escape(shortDay(days[i]))}</text>`)
    .join("");
  const lines = series.map((s) => {
    const pts = s.values.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
    const dots = s.values
      .map((v, i) => v > 0
        ? `<circle class="dot" style="fill:var(${s.css})" cx="${x(i).toFixed(1)}" cy="${y(v).toFixed(1)}" r="4"><title>${escape(`${shortDay(days[i])}: ${v} ${s.name.toLowerCase()}`)}</title></circle>`
        : "")
      .join("");
    const last = s.values.length - 1;
    const end = `<text class="end" x="${x(last) + 10}" y="${y(s.values[last]) + 4}">${escape(s.name)} ${escape(s.values[last])}</text>`;
    return `<polyline fill="none" style="stroke:var(${s.css})" stroke-width="2" stroke-linejoin="round" points="${pts}"/>${dots}${end}`;
  }).join("");
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="${escape(label)}">${grid}${ticks}${lines}</svg>`;
}

// columnChart draws one column per row, oldest first, for a short series such
// as installs per week. rows is [{label, value, title}].
function columnChart(rows, label) {
  const W = 720, H = 220, L = 48, R = 16, T = 16, B = 40;
  const max = niceMax(Math.max(0, ...rows.map((r) => r.value)));
  const slot = (W - L - R) / Math.max(rows.length, 1);
  const bw = Math.min(48, slot * 0.6);
  const y = (v) => T + (H - T - B) * (1 - v / max);
  const grid = [0, max / 2, max]
    .map((v) => `<line class="grid" x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/>` +
      `<text class="tick" x="${L - 8}" y="${y(v) + 4}" text-anchor="end">${escape(Number.isInteger(v) ? v : v.toFixed(1))}</text>`)
    .join("");
  const cols = rows.map((r, i) => {
    const cx = L + slot * i + slot / 2;
    const h = Math.max(0, y(0) - y(r.value));
    // A column has 4px rounded ends at the top and a square foot on the baseline.
    const top = y(r.value), rr = Math.min(4, h / 2);
    const path = h > 0
      ? `M${cx - bw / 2},${y(0)} V${top + rr} Q${cx - bw / 2},${top} ${cx - bw / 2 + rr},${top} H${cx + bw / 2 - rr} Q${cx + bw / 2},${top} ${cx + bw / 2},${top + rr} V${y(0)} Z`
      : "";
    return (path ? `<path class="col" d="${path}"><title>${escape(r.title)}</title></path>` : "") +
      `<rect class="hit" x="${cx - slot / 2}" y="${T}" width="${slot}" height="${H - T - B}"><title>${escape(r.title)}</title></rect>` +
      `<text class="tick" x="${cx}" y="${H - 10}" text-anchor="middle">${escape(r.label)}</text>`;
  }).join("");
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="${escape(label)}">${grid}${cols}</svg>`;
}

// barList draws horizontal bars, one per value, in HTML: the value's name,
// the bar, and the count in text, so no number depends on colour.
function barList(rows) {
  if (!rows.length) return `<p class="empty">No reports with this field yet this week.</p>`;
  const max = Math.max(...rows.map((r) => r.installs));
  return `<ul class="bars">` + rows.map((r) =>
    `<li title="${escape(`${r.value ?? "unknown"}: ${r.installs}`)}"><span class="bl">${escape(r.value ?? "unknown")}</span>` +
    `<span class="bt"><span class="bf" style="width:${((100 * r.installs) / max).toFixed(1)}%"></span></span>` +
    `<span class="bv">${escape(r.installs)}</span></li>`).join("") + `</ul>`;
}

// table writes rows as an HTML table with the columns cols, inside a fold, so
// every chart's numbers are there to read without the chart.
function table(cols, rows) {
  const head = cols.map(([, label]) => `<th>${escape(label)}</th>`).join("");
  const body = rows
    .map((r) => "<tr>" + cols.map(([key]) => `<td>${escape(r[key])}</td>`).join("") + "</tr>")
    .join("\n");
  return `<details><summary>Show the numbers</summary><table><thead><tr>${head}</tr></thead><tbody>\n${body}\n</tbody></table></details>`;
}

// tile writes one headline number.
function tile(label, value, note) {
  return `<div class="tile"><div class="tl">${escape(label)}</div><div class="tv">${escape(value)}</div>` +
    (note ? `<div class="tn">${escape(note)}</div>` : "") + `</div>`;
}

// FIELD_NAMES names each field the page breaks this week down by.
const FIELD_NAMES = {
  meru: "Meru version", os: "System", arch: "Chip", profile: "Model set",
  streak: "Weeks in a row", active_days: "Days used, last 7",
};

// page handles GET /: tiles, a daily line chart, a weekly column chart, one
// bar list per field for this week, and the upgrades.
async function page(env) {
  const c = await counts(env);
  const byDay = new Map(c.daily.map((r) => [r.day, r]));
  const firstDay = c.daily.length ? c.daily[c.daily.length - 1].day : null;
  // Show from the first day with data, but at least two weeks, at most 90 days.
  let span = 14;
  if (firstDay) {
    const age = Math.round((Date.now() - Date.parse(firstDay + "T00:00:00Z")) / 86400000) + 1;
    span = Math.min(DAYS_SHOWN, Math.max(14, age));
  }
  const days = dayRange(span);
  const active = days.map((d) => byDay.get(d)?.active ?? 0);
  const fresh = days.map((d) => byDay.get(d)?.new ?? 0);
  const today = days[days.length - 1];
  const week = c.weekly[0], month = c.monthly[0];
  const weeks = [...c.weekly].reverse().slice(-12);

  // The bucketed fields keep their own order, 1 before 2-4; the rest go by
  // count, largest first, as the query returns them.
  const fields = Object.entries(FIELD_NAMES).map(([f, name]) => {
    const order = SCHEMAS[2].allowed[f];
    const rows = order && (f === "streak" || f === "active_days")
      ? [...c.this_week[f]].sort((a, b) => order.indexOf(a.value) - order.indexOf(b.value))
      : c.this_week[f];
    return `<section class="card small"><h3>${escape(name)}</h3>${barList(rows)}</section>`;
  }).join("");

  const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Meru usage totals</title>
<style>
  :root {
    color-scheme: light;
    --surface: #fcfcfb; --page: #f4f3f0; --border: #e3e2de;
    --text-primary: #0b0b0b; --text-secondary: #52514e; --text-muted: #77756f;
    --grid: #e8e7e3; --series-1: #2a78d6; --series-2: #eb6834; --track: #ecebe7;
  }
  @media (prefers-color-scheme: dark) {
    :root:where(:not([data-theme="light"])) {
      color-scheme: dark;
      --surface: #1a1a19; --page: #121211; --border: #33332f;
      --text-primary: #ffffff; --text-secondary: #c3c2b7; --text-muted: #9a998f;
      --grid: #2c2c29; --series-1: #3987e5; --series-2: #d95926; --track: #2a2a27;
    }
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--page); color: var(--text-primary);
    font: 15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 64rem; margin: 0 auto; padding: 2rem 1rem 3rem; }
  h1 { font-size: 1.75rem; margin: 0 0 0.25rem; }
  h2 { font-size: 1.05rem; margin: 0 0 0.75rem; }
  h3 { font-size: 0.9rem; margin: 0 0 0.5rem; color: var(--text-secondary); font-weight: 600; }
  .lede { color: var(--text-secondary); margin: 0 0 1.5rem; max-width: 46rem; }
  a { color: var(--series-1); }
  .tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr)); gap: 0.75rem; margin-bottom: 1rem; }
  .tile, .card { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; }
  .tile { padding: 0.9rem 1rem; }
  .tl { color: var(--text-secondary); font-size: 0.8rem; }
  .tv { font-size: 1.9rem; font-weight: 700; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .tn { color: var(--text-muted); font-size: 0.75rem; }
  .card { padding: 1rem 1.1rem; margin-bottom: 1rem; }
  .grid2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(18rem, 1fr)); gap: 0.75rem; margin-bottom: 1rem; }
  .grid2 .card { margin: 0; }
  .legend { display: flex; gap: 1rem; font-size: 0.8rem; color: var(--text-secondary); margin-bottom: 0.25rem; }
  .key { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: 0.35rem; vertical-align: -1px; }
  .chart { width: 100%; height: auto; display: block; }
  .chart .grid { stroke: var(--grid); stroke-width: 1; }
  .chart .tick { fill: var(--text-muted); font-size: 11px; }
  .chart .end { fill: var(--text-secondary); font-size: 12px; }
  .chart .dot { stroke: var(--surface); stroke-width: 2; }
  .chart .col { fill: var(--series-1); }
  .chart .hit { fill: transparent; }
  .chart .hit:hover { fill: var(--grid); fill-opacity: 0.4; }
  .bars { list-style: none; margin: 0; padding: 0; }
  .bars li { display: grid; grid-template-columns: 7.5rem 1fr 2.5rem; align-items: center; gap: 0.5rem; padding: 0.2rem 0; font-size: 0.85rem; }
  .bl { color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bt { background: var(--track); border-radius: 4px; height: 12px; }
  .bf { display: block; height: 100%; background: var(--series-1); border-radius: 4px; min-width: 4px; }
  .bv { text-align: right; font-variant-numeric: tabular-nums; }
  .empty { color: var(--text-muted); font-size: 0.85rem; margin: 0; }
  details { margin-top: 0.5rem; font-size: 0.85rem; }
  summary { cursor: pointer; color: var(--text-secondary); }
  table { border-collapse: collapse; width: 100%; margin-top: 0.5rem; font-variant-numeric: tabular-nums; }
  th, td { padding: 0.35rem 0.6rem; border-bottom: 1px solid var(--border); text-align: left; }
  th { color: var(--text-secondary); font-weight: 600; font-size: 0.8rem; }
  td:not(:first-child), th:not(:first-child) { text-align: right; }
  footer { color: var(--text-muted); font-size: 0.8rem; margin-top: 1.5rem; }
  /* A chart's text is in SVG units, which shrink with the chart; on a phone
     the 720-unit chart is about 330px wide, so the text doubles to stay
     about 10px on screen. */
  @media (max-width: 600px) {
    .chart .tick { font-size: 20px; }
    .chart .end { font-size: 21px; }
  }
</style>
</head>
<body>
<main>
<h1>Meru usage totals</h1>
<p class="lede">Each install of Meru sends at most one anonymous report a day, with no ID. An install counts as
active in a day, a week or a month when it sent a report in it, and as new on the day of its first report.
Weekly and monthly counts come from Meru v0.7.7 and later. Days are UTC.
<a href="/totals">The same numbers as JSON</a>.</p>

<div class="tiles">
${tile("Installs so far", c.total_installs)}
${tile("Active today", byDay.get(today)?.active ?? 0, shortDay(today))}
${tile("Active this week", week?.active ?? 0, week ? `week of ${shortDay(week.week)}` : "")}
${tile("Active this month", month?.active ?? 0, month?.month ?? "")}
${tile("New this week", week?.new ?? 0)}
</div>

<section class="card">
<h2>Installs per day</h2>
<div class="legend"><span><span class="key" style="background:var(--series-1)"></span>Active</span><span><span class="key" style="background:var(--series-2)"></span>New</span></div>
${lineChart(days, [{ name: "Active", css: "--series-1", values: active }, { name: "New", css: "--series-2", values: fresh }], "Active and new installs per day")}
${table([["day", "Day"], ["active", "Active"], ["new", "New"]], c.daily)}
</section>

<section class="card">
<h2>Active installs per week</h2>
${columnChart(weeks.map((w) => ({ label: shortDay(w.week), value: w.active, title: `Week of ${shortDay(w.week)}: ${w.active} active, ${w.new} new` })), "Active installs per ISO week")}
${table([["week", "Week of"], ["active", "Active"], ["new", "New"]], c.weekly)}
${table([["month", "Month"], ["active", "Active"], ["new", "New"]], c.monthly).replace("Show the numbers", "Show the months")}
</section>

<h2>This week, one field at a time</h2>
<div class="grid2">${fields}</div>

<section class="card">
<h2>Upgrades in the last 30 days</h2>
${c.upgrades.length ? barList(c.upgrades.map((u) => ({ value: `${u.from_version} to ${u.to_version}`, installs: u.installs }))) : `<p class="empty">No upgrades reported yet.</p>`}
</section>

<footer>Each count stands alone; the page never crosses two fields, which on a quiet week could single out one install.
The collector stores no IP address. The rules are in Meru's FAQ, "What does Meru send about me".</footer>
</main>
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
