// Draws one turn of the conversation: the question bubble, and the answer
// with its work strip, approval card, body, source chips, actions and
// stats line. Each part has its own draw function, so a streamed token or
// a tool result redraws only the part it changes.
//
// All text goes in with textContent, which the browser never reads as
// HTML. The one exception is a finished answer, which markdown.js renders
// and sanitizes.

import { icon } from "./icons.js";
import { renderMarkdown } from "./markdown.js";

// ROUTES says in words what each route did.
const ROUTES = {
  direct: "Answered from the model",
  search: "Searched your files",
  tools: "Used tools",
  "search+tools": "Searched your files and used tools",
};

// el makes an element with a class and, optionally, text.
export function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

// button makes a real <button> with an optional icon and a label.
export function button(label, { iconName, className = "text-button", onClick, ariaLabel } = {}) {
  const b = el("button", className);
  b.type = "button";
  if (iconName) b.append(icon(iconName, 15));
  if (label) b.append(document.createTextNode(iconName ? " " + label : label));
  if (ariaLabel) b.setAttribute("aria-label", ariaLabel);
  if (onClick) b.addEventListener("click", onClick);
  return b;
}

// seconds writes a time in milliseconds as seconds: "0.42 s", "5.1 s".
export function seconds(ms) {
  const s = ms / 1000;
  return (s < 1 ? s.toFixed(2) : s.toFixed(1)) + " s";
}

// baseName returns the last part of a path such as "~/Notes/lisbon.md".
export function baseName(path) {
  const parts = String(path).split(/[\\/]/);
  return parts[parts.length - 1] || path;
}

// createTurn builds the elements for turn t and keeps them on t.el. h holds
// the handlers the buttons call.
export function createTurn(t, h) {
  const root = el("article", "turn");
  const q = el("div", "question");
  q.append(el("p", "bubble", t.question));

  const answer = el("section", "answer");
  answer.setAttribute("aria-label", "Meru's answer");
  answer.addEventListener("click", (e) => {
    // A click on the answer's text shows what it used in the side panel;
    // a click on a button or link does its own thing.
    if (!e.target.closest("button, a")) h.onSelect(t);
  });
  const strip = el("div", "work");
  const detail = el("ol", "steps-detail");
  detail.hidden = true;
  detail.id = "steps-" + t.key;
  const approval = el("div", "approval-slot");
  const body = el("div", "body");
  const notice = el("p", "answer-notice");
  notice.setAttribute("role", "note");
  const sources = el("div", "sources-line");
  const footer = el("div", "answer-foot");
  answer.append(strip, detail, approval, body, notice, sources, footer);
  root.append(q, answer);
  t.el = { root, answer, strip, detail, approval, body, notice, sources, footer };
  drawAll(t, h);
  return root;
}

// drawAll redraws every part of t.
export function drawAll(t, h) {
  drawStrip(t, h);
  drawApproval(t, h);
  drawBody(t, h);
  drawNotice(t);
  drawSources(t, h);
  drawFooter(t, h);
}

// drawStrip draws the one-line work strip: what the route did, each tool
// call's friendly label, and the Show steps toggle over the raw detail.
export function drawStrip(t, h) {
  const strip = t.el.strip;
  strip.replaceChildren();
  const route = el("span", "route" + (t.fallback ? " fallback" : ""), ROUTES[t.route] || (t.route ? t.route : "Working"));
  strip.append(route);
  for (const s of t.steps) {
    const failed = s.outcome && s.outcome !== "ok";
    const step = el("span", "step" + (failed ? " failed" : ""));
    step.append(icon(failed ? "alert" : "tool", 13), document.createTextNode(" " + s.label));
    strip.append(step);
  }
  if (t.approval && !t.approval.answered) {
    const chip = el("span", "waiting");
    chip.append(icon("ask", 13), document.createTextNode(" Waiting for you"));
    strip.append(chip);
  } else if (t.state === "active" && !t.answer) {
    strip.append(el("span", "working", "Working…"));
  }
  if (t.route || t.steps.length > 0) {
    const toggle = button(t.showSteps ? "Hide steps" : "Show steps", { className: "text-button toggle" });
    toggle.setAttribute("aria-expanded", String(t.showSteps));
    toggle.setAttribute("aria-controls", t.el.detail.id);
    toggle.addEventListener("click", () => {
      t.showSteps = !t.showSteps;
      drawStrip(t, h);
    });
    strip.append(toggle);
  }
  drawDetail(t);
}

// drawDetail fills the raw step list behind Show steps: the route with the
// router's confidence, then each call's full tool name, where it ran, its
// outcome and its time.
function drawDetail(t) {
  const d = t.el.detail;
  d.hidden = !t.showSteps;
  d.replaceChildren();
  if (t.route) {
    const li = el("li");
    let text = "route " + t.route;
    if (t.confidence) text += " · confidence " + t.confidence.toFixed(2);
    if (t.fallback) text += " · the router wasn't sure and used its fallback";
    if (t.skills && t.skills.length) text += " · skills " + t.skills.join(", ");
    li.append(el("code", "", text));
    d.append(li);
  }
  for (const s of t.steps) {
    const li = el("li");
    li.append(el("code", "", s.name));
    const where = s.server ? s.kind + " · " + s.server : s.kind;
    const how = s.outcome ? s.outcome + (s.duration_ms ? " in " + seconds(s.duration_ms) : "") : "running";
    li.append(el("span", "dim", " " + where + " · " + how));
    d.append(li);
  }
}

// drawApproval draws the approval card merud asked for, or, once
// answered, one line that says what the user chose.
export function drawApproval(t, h) {
  const slot = t.el.approval;
  slot.replaceChildren();
  const a = t.approval;
  if (!a) return;
  if (a.answered) {
    const line = el("p", "approval-done");
    line.append(icon(a.answered === "deny" || a.answered === "edit" ? "close" : "check", 14),
      document.createTextNode(" " + answeredText(a.answered, a.view.label)));
    slot.append(line);
    return;
  }
  const card = approvalCard(a.view, "approval-" + t.key, (choice) => h.onApprove(t, choice));
  // The side panel starts closed, so the card itself says why Meru asks,
  // with a link to the panel's longer answer.
  const why = el("p", "approval-why", h.whyText(a.view) + " ");
  why.append(button("More in the side panel", { className: "text-button link", onClick: () => h.onSelect(t, true) }));
  card.insertBefore(why, card.querySelector(".choices"));
  slot.append(card);
}

// approvalCard builds the card for view v: what the call would do, its
// arguments laid out to read, and the answers. A mail card offers Send,
// Edit first and Don't send; any other card Allow once and Don't allow,
// with Edit first between. Allow for this chat comes when merud offers
// it. onChoice gets "once", "session", "deny" or "edit". A save's card,
// which sits above the composer, uses it too.
export function approvalCard(v, titleId, onChoice) {
  const card = el("div", "approval");
  card.setAttribute("role", "group");
  card.setAttribute("aria-labelledby", titleId);
  const title = el("p", "approval-title");
  title.id = titleId;
  title.append(icon("ask", 16), document.createTextNode(" Meru asks before it does this: "));
  title.append(el("strong", "", v.label));
  card.append(title, el("p", "approval-tool", v.name + " · " + v.kind));

  if (v.fields && v.fields.length) {
    const dl = el("dl", "fields");
    for (const f of v.fields) {
      dl.append(el("dt", "", f.label), el("dd", f.label === "Body" ? "field-body" : "", f.value));
    }
    card.append(dl);
  }
  if (v.json) card.append(el("pre", "args", v.json));

  const mail = !!(v.fields && v.fields.length);
  const offered = new Set(v.choices.map((c) => c.choice));
  const choices = el("div", "choices");
  const add = (label, choice, cls) => {
    const b = button(label, { className: cls, onClick: () => onChoice(choice) });
    b.dataset.choice = choice;
    choices.append(b);
  };
  if (offered.has("once")) add(mail ? "Send" : "Allow once", "once", "button ask");
  if (offered.has("session")) add("Allow for this chat", "session", "button secondary");
  if (v.draft) add("Edit first", "edit", "button secondary");
  add(mail ? "Don't send" : "Don't allow", "deny", "button secondary");
  card.append(choices);
  return card;
}

// answeredText says what the user chose on an approval card.
function answeredText(choice, label) {
  switch (choice) {
    case "once":
      return "You allowed this once: " + label + ".";
    case "session":
      return "You allowed this for the rest of this chat: " + label + ".";
    case "ended":
      return "The question ended before you answered: " + label + ".";
    case "edit":
      return "Not done yet. The draft is in the box below to change and send: " + label + ".";
  }
  return "You didn't allow this: " + label + ".";
}

// drawBody draws the answer: plain text while it streams, because
// half-written Markdown renders wrong, and sanitized Markdown once it ends.
export function drawBody(t, h) {
  const body = t.el.body;
  body.replaceChildren();
  body.classList.toggle("streaming", t.state === "active");
  if (t.state === "active") {
    t.el.stream = el("p", "stream-text", t.answer);
    body.append(t.el.stream);
    return;
  }
  if (t.answer) {
    body.append(renderMarkdown(t.answer, h.onCopyCode));
  }
  if (t.outcome) {
    body.append(el("p", "note", outcomeText(t.outcome)));
  }
  if (t.state === "stopped") {
    body.append(el("p", "note", "You stopped this answer."));
  } else if (t.error) {
    const p = el("p", "error");
    p.append(icon("alert", 14), document.createTextNode(" " + t.error));
    body.append(p);
  } else if (!t.answer && t.state === "done") {
    body.append(el("p", "note", "No answer came back."));
  }
}

// appendToken adds streamed text to t's answer without redrawing the rest.
export function appendToken(t, text) {
  t.answer += text;
  if (t.el.stream) {
    t.el.stream.textContent = t.answer;
  }
}

// outcomeText says how a turn ended without a full answer, as the
// transcript records it.
function outcomeText(outcome) {
  switch (outcome) {
    case "timeout":
      return "This answer ran out of time.";
    case "cut_off":
      return "This answer hit the length limit and stops short.";
    case "gave_up":
      return "Meru stopped after too many tool rounds.";
  }
  return "";
}

// drawNotice draws merud's warning under the answer, as an amber note
// with a warning icon: the answer claimed an action, such as moving a
// folder, and no tool did it. It shows for a live turn and for a past one
// reopened from history, and hides when there is none.
export function drawNotice(t) {
  const p = t.el.notice;
  p.replaceChildren();
  p.hidden = !t.notice;
  if (t.notice) p.append(icon("alert", 14), document.createTextNode(" " + t.notice));
}

// drawSources draws the sources the finished answer cites: one closed
// line, "3 sources", whose button opens a chip for each; a chip's click
// opens the file. An answer that cites nothing gets no line. The side
// panel still lists every file the prompt held.
export function drawSources(t, h) {
  const line = t.el.sources;
  line.replaceChildren();
  line.hidden = t.cited.length === 0;
  if (line.hidden) return;
  const list = el("ul", "chips");
  list.id = "sources-" + t.key;
  list.setAttribute("aria-label", "Sources");
  list.hidden = !t.showSources;
  const n = t.cited.length;
  const toggle = button(n + (n === 1 ? " source" : " sources"), { className: "text-button toggle sources-toggle", iconName: "chevron" });
  toggle.setAttribute("aria-expanded", String(t.showSources));
  toggle.setAttribute("aria-controls", list.id);
  toggle.addEventListener("click", () => {
    t.showSources = !t.showSources;
    drawSources(t, h);
  });
  line.append(toggle, list);
  for (const s of t.cited) {
    const li = el("li");
    const chip = button("", { className: "chip", onClick: () => h.onOpenSource(s.path) });
    chip.append(icon("file", 14), el("span", "chip-n", "[" + s.n + "]"), el("span", "", baseName(s.path)));
    chip.title = "Open " + s.path;
    chip.setAttribute("aria-label", "Open source " + s.n + ", " + s.path);
    li.append(chip);
    list.append(li);
  }
}

// drawFooter draws Copy, Try again and Details, and the dim stats line.
export function drawFooter(t, h) {
  const f = t.el.footer;
  f.replaceChildren();
  if (t.state === "active") return;
  const actions = el("div", "actions");
  if (t.answer) {
    actions.append(button("Copy", { iconName: "copy", onClick: (e) => h.onCopyAnswer(t, e.currentTarget) }));
    actions.append(button("Save to a note", { iconName: "note", onClick: () => h.onSaveNote(t) }));
  }
  actions.append(button("Try again", { iconName: "retry", onClick: () => h.onRetry(t) }));
  actions.append(button("Details", { iconName: "panel", onClick: () => h.onSelect(t, true) }));
  f.append(actions);
  const stats = statsText(t);
  if (stats) f.append(el("p", "stats", stats));
}

// statsText writes the stats line: time to first token, the model's speed
// in tokens a second, and the total time. A past turn has only its total
// time and token count.
function statsText(t) {
  const s = t.stats;
  if (!s) return "";
  const parts = [];
  if (s.ttft_ms) parts.push("First token " + seconds(s.ttft_ms));
  if (s.tokens_out && s.eval_ms) parts.push(Math.round(s.tokens_out / (s.eval_ms / 1000)) + " tokens/s");
  else if (s.tokens_out) parts.push(s.tokens_out + " tokens");
  if (s.duration_ms) parts.push(seconds(s.duration_ms) + " total");
  return parts.join(" · ");
}
