// The anonymous daily count: the banner at the top of the chat screen,
// and the section on the Usage screen that settings.js places. merud
// builds and sends the count, keeps its log and writes the choice to
// config.toml (ARCHITECTURE.md, "Anonymous usage report"); this file only
// shows what merud says and passes on the user's button presses.
//
// The banner sits above the chat in the page's flow, never over it, and
// nothing here calls focus(), so a user typing in the composer when the
// banner appears loses no keystrokes. Every word from merud goes in
// through textContent.

import { bridge, errorText } from "./api.js";
import { el, button } from "./turns.js";

// What the banner says, before its buttons, for each banner merud names.
const NEW_TEXT =
  "Meru sends one anonymous count a day: its version, your system and chip, the model set, and rough install age and question count. No ID, no questions, no files.";
const NEW_FIRST = " Your first one goes tomorrow.";
const ASK_TEXT =
  "Meru can now send one anonymous count a day: its version, your system and chip, the model set, and rough install age and question count. No ID, no questions, no files. It stays off unless you say yes.";

// What the Usage section calls each choice.
const CHOICES = { on: "On", off: "Off", "": "Not chosen" };

const report = {
  // seenSent is true once this app run has told merud the banner showed;
  // merud keeps one "seen" line however often it hears, but the status
  // poll would still ask every few seconds without the flag.
  seenSent: false,
  // drawn is the view the banner shows now, as JSON, so the poll redraws
  // only on a change and a user tabbing through the buttons keeps their
  // place.
  drawn: "",
};

// loadReport asks merud how the count stands and draws the banner. app.js
// calls it at start and on each status poll. A merud that can't be
// reached hides the banner until it answers again.
export function loadReport() {
  bridge.usageReport().then(drawBanner, () => drawBanner(null));
}

// drawBanner draws the banner from v, the Bridge's UsageReportView, or
// hides it when v is null, merud is too old to know the count, or merud
// names no banner. The first time the banner shows on the chat screen in
// this app run, it tells merud the user has seen it.
export function drawBanner(v) {
  const box = document.getElementById("report-banner");
  if (!box) return;
  const show = !!(v && v.available && (v.banner === "new" || v.banner === "ask"));
  // The banner lives on the chat screen, so it counts as seen only while
  // that screen shows, not while Settings or Setup covers it.
  const onScreen = !document.getElementById("chat-view").hidden;
  if (show && onScreen && !report.seenSent) {
    report.seenSent = true;
    bridge.usageReportMark("seen").then(drawBanner, () => {});
  }
  const key = show ? JSON.stringify([v.banner, v.sent, v.next]) : "";
  if (key === report.drawn) return;
  report.drawn = key;
  box.replaceChildren();
  box.hidden = !show;
  if (!show) return;

  const text = v.banner === "new" ? NEW_TEXT + (v.sent ? "" : NEW_FIRST) : ASK_TEXT;
  box.append(el("p", "report-text", text));

  const pre = el("pre", "report-json", pretty(v.next));
  pre.id = "report-banner-json";
  pre.hidden = true;
  const see = button("See the report", { className: "button secondary" });
  see.setAttribute("aria-expanded", "false");
  see.setAttribute("aria-controls", pre.id);
  see.addEventListener("click", () => {
    pre.hidden = !pre.hidden;
    see.setAttribute("aria-expanded", String(!pre.hidden));
  });

  const actions = el("div", "report-actions");
  const err = el("p", "error report-error");
  err.setAttribute("role", "status");
  // act sends one change to merud and redraws from its reply.
  const act = (call) => () =>
    call().then(drawBanner, (e) => {
      err.textContent = errorText(e);
    });
  actions.append(see);
  if (v.banner === "new") {
    actions.append(
      button("Turn off", { className: "button secondary", onClick: act(() => bridge.usageReportSet("off")) }),
      button("Got it", { className: "button primary", onClick: act(() => bridge.usageReportMark("dismiss")) }),
    );
  } else {
    actions.append(
      button("Share", { className: "button primary", onClick: act(() => bridge.usageReportSet("on")) }),
      button("Don't share", { className: "button secondary", onClick: act(() => bridge.usageReportSet("off")) }),
    );
  }
  box.append(actions, err, pre);
}

// reportSection returns the Usage screen's "Anonymous daily count"
// section, which fills itself once merud answers. A merud too old to
// know the count leaves it empty and hidden.
export function reportSection() {
  const section = el("section", "report-section");
  section.hidden = true;
  const fill = (v) => {
    section.replaceChildren();
    if (!v || !v.available) {
      section.hidden = true;
      return;
    }
    section.hidden = false;
    drawSection(section, v, fill);
  };
  bridge.usageReport().then(fill, () => fill(null));
  return section;
}

// drawSection fills section from v: the choice, why nothing will send,
// when the next one goes, where it goes when config changed that, the
// recent entries and the switch. fill redraws it from a reply.
function drawSection(section, v, fill) {
  section.append(el("h2", "section-head", "Anonymous daily count"));
  section.append(el("p", "report-choice", CHOICES[v.choice] ?? v.choice));
  if (v.blocked) section.append(el("p", "panel-note", v.blocked));
  if (v.next_at) section.append(el("p", "panel-note", "Next one after " + localTime(v.next_at)));
  if (v.url) section.append(el("p", "panel-note", "Sent to: " + v.url));

  const err = el("p", "error");
  err.setAttribute("role", "status");
  const on = v.choice === "on";
  const toggle = button(on ? "Turn off" : "Turn on", { className: "button secondary" });
  toggle.addEventListener("click", () => {
    bridge.usageReportSet(on ? "off" : "on").then(
      (next) => {
        fill(next);
        // The banner closes once the user chooses here too.
        drawBanner(next);
      },
      (e) => {
        err.textContent = errorText(e);
      },
    );
  });
  section.append(toggle, err);

  if (v.next) {
    const details = el("details", "report-next");
    details.append(el("summary", "", "The next report"), el("pre", "report-json", pretty(v.next)));
    section.append(details);
  }

  if (v.recent.length === 0) {
    section.append(el("p", "panel-note", "Nothing sent or shown yet."));
    return;
  }
  const list = el("ul", "report-log");
  list.setAttribute("aria-label", "Recent entries");
  for (const r of v.recent) {
    const li = el("li");
    const head = el("p", "report-entry-head");
    head.append(el("span", "report-type", r.type), document.createTextNode(" · " + localTime(r.at)));
    if (r.client) head.append(document.createTextNode(" · " + r.client));
    li.append(head);
    if (r.error) li.append(el("p", "error", r.error));
    if (r.body) li.append(el("pre", "report-json", pretty(r.body)));
    list.append(li);
  }
  section.append(list);
}

// pretty returns the report as merud would send it, byte for byte, so
// what the user reads is what leaves the machine. The CSS wraps the line.
function pretty(json) {
  return json || "";
}

// localTime writes an RFC 3339 time in the user's own time zone, or the
// text as it came when it isn't a time.
function localTime(at) {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}
