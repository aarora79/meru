// The page's state and wiring: the rail (new chat, search, the list of
// past chats, merud's status), the conversation, the composer with its
// queue, and the side panel that shows what an answer used. turns.js draws
// each turn; api.js reaches Go.
//
// The Bridge in Go owns the running turn and the queue. The page keeps
// only what it draws, and redraws from each Update the Bridge sends.

import { bridge, onUpdate, copyText, errorText } from "./api.js";
import { icon } from "./icons.js";
import {
  el, button, baseName, seconds, createTurn, drawAll, drawStrip, drawApproval,
  drawBody, drawSources, drawFooter, appendToken,
} from "./turns.js";

// How often the rail asks merud for its status, in milliseconds.
const STATUS_EVERY = 15000;
// The most questions that wait behind a running turn, as in the Bridge.
const MAX_QUEUE = 5;
// The groups of the rail's list, in the order they show.
const GROUPS = ["Today", "Yesterday", "Earlier"];

const state = {
  session: "", // the open conversation's ID; "" for a new one
  title: "New chat",
  turns: [], // the open conversation's turns, oldest first
  running: 0, // the running turn's number, or 0
  queue: [], // questions waiting behind it
  sessions: [], // the rail's list
  sessionsError: "",
  filter: "",
  selected: null, // the turn the side panel shows
  status: null,
  machine: "this Mac",
  nextKey: 1, // numbers turn elements, for ids
};

// $ finds the one element with this id.
const $ = (id) => document.getElementById(id);

// handlers are what the turn's buttons call.
const handlers = {
  onSelect(t, openPanel) {
    select(t);
    if (openPanel) setPanel(true);
  },
  onApprove(t, choice) {
    const a = t.approval;
    if (!a || a.answered) return;
    bridge.approve(t.n, a.view.id, choice).then(
      () => {
        a.answered = choice;
        drawApproval(t, handlers);
        drawStrip(t, handlers);
        $("question").focus();
      },
      (err) => notice(errorText(err)),
    );
  },
  onOpenSource(path) {
    bridge.openSource(path).catch((err) => notice(errorText(err)));
  },
  onCopyCode(text, b) {
    copy(text, b);
  },
  onCopyAnswer(t, b) {
    copy(t.answer, b);
  },
  onRetry(t) {
    send(t.question);
  },
};

// copy puts text on the clipboard and says so on the button for a moment.
function copy(text, b) {
  copyText(text).then(
    () => {
      const label = b.lastChild;
      const was = label.textContent;
      label.textContent = " Copied";
      setTimeout(() => (label.textContent = was), 1500);
    },
    (err) => notice(errorText(err)),
  );
}

// notice shows one line above the composer; screen readers hear it.
function notice(text) {
  $("notice").textContent = text || "";
}

// ---- The conversation ----

// newTurn returns an empty turn record.
function newTurn(fields) {
  return {
    key: state.nextKey++,
    n: 0,
    question: "",
    state: "active",
    route: "",
    confidence: 0,
    fallback: false,
    skills: [],
    steps: [],
    sources: [],
    answer: "",
    stats: null,
    error: "",
    outcome: "",
    approval: null,
    contacted: [],
    showSteps: false,
    ...fields,
  };
}

// drawConversation redraws the title, every turn, or the empty state.
function drawConversation() {
  $("chat-title").textContent = state.title;
  const list = $("messages");
  list.replaceChildren();
  if (state.turns.length === 0) {
    list.append(emptyState());
  }
  for (const t of state.turns) {
    list.append(createTurn(t, handlers));
  }
  drawPanel();
  scrollDown(true);
}

// emptyState is what a new chat shows: a line on what Meru does, and a
// few questions to start with.
function emptyState() {
  const box = el("div", "empty");
  box.append(el("h2", "", "Ask Meru"));
  box.append(el("p", "", "Answers come from the model on " + state.machine +
    " and from the folders, mail and notes you connected."));
  const ideas = el("div", "ideas");
  for (const q of [
    "What time is check-in at the Lisbon hotel?",
    "Summarize my notes in ~/Notes on the garden plan",
    "Draft a reply to Dana Reyes about Friday",
  ]) {
    ideas.append(button(q, {
      className: "idea",
      onClick: () => {
        const box = $("question");
        box.value = q;
        fit(box);
        box.focus();
      },
    }));
  }
  box.append(ideas);
  return box;
}

// scrollDown keeps the newest text in view, unless the user has scrolled
// up to read; force scrolls anyway.
function scrollDown(force) {
  const list = $("messages");
  const near = list.scrollHeight - list.scrollTop - list.clientHeight < 120;
  if (force || near) list.scrollTop = list.scrollHeight;
}

// select makes t the turn the side panel shows, and marks it.
function select(t) {
  if (state.selected && state.selected.el) state.selected.el.answer.classList.remove("selected");
  state.selected = t;
  if (t && t.el) t.el.answer.classList.add("selected");
  drawPanel();
}

// findTurn returns the live turn numbered n, if the open conversation
// holds it.
function findTurn(n) {
  return state.turns.find((t) => t.n === n) || null;
}

// ---- Updates from the Bridge ----

onUpdate((u) => {
  switch (u.kind) {
    case "start":
      onStart(u);
      break;
    case "event":
      onEvent(u);
      break;
    case "approval":
      onApproval(u);
      break;
    case "end":
      onEnd(u);
      break;
    case "queue":
      state.queue = u.queue || [];
      drawQueue();
      if (u.notice) notice(u.notice);
      break;
  }
});

// onStart adds the question that just went to merud.
function onStart(u) {
  if (state.turns.length === 0) {
    state.title = u.question.length > 80 ? u.question.slice(0, 79) + "…" : u.question;
    $("messages").replaceChildren();
    $("chat-title").textContent = state.title;
  }
  const t = newTurn({ n: u.turn, question: u.question });
  state.turns.push(t);
  state.running = u.turn;
  $("messages").append(createTurn(t, handlers));
  select(t);
  drawComposer();
  scrollDown(true);
}

// onEvent applies one of merud's events to its turn.
function onEvent(u) {
  const t = findTurn(u.turn);
  const ev = u.event;
  if (ev.type === "session" && u.session) {
    state.session = u.session;
    drawSessions();
  }
  if (!t) return;
  switch (ev.type) {
    case "route":
      t.route = ev.route || "";
      t.confidence = ev.confidence || 0;
      t.fallback = !!ev.fallback;
      t.skills = (ev.skills || []).map((s) => s.name);
      drawStrip(t, handlers);
      break;
    case "sources":
      t.sources = ev.sources || [];
      drawSources(t, handlers);
      break;
    case "token":
      if (!t.answer) drawStrip(t, handlers); // drop "Working…"
      appendToken(t, ev.text || "");
      scrollDown(false);
      return; // the panel doesn't change
    case "tool_call":
    case "tool_result":
      if (u.step) {
        const i = t.steps.findIndex((s) => s.id === u.step.id);
        if (i >= 0) t.steps[i] = u.step;
        else t.steps.push(u.step);
      }
      drawStrip(t, handlers);
      break;
    case "done":
      t.stats = ev;
      break;
    case "error":
      t.error = ev.error || "merud reported an error.";
      break;
  }
  if (state.selected === t) drawPanel();
}

// onApproval shows the approval card and moves focus to "Don't allow", so
// a stray Enter can't approve a call.
function onApproval(u) {
  const t = findTurn(u.turn);
  if (!t) return;
  t.approval = { view: u.approval, answered: null };
  drawApproval(t, handlers);
  drawStrip(t, handlers);
  const deny = t.el.approval.querySelector('[data-choice="deny"]');
  if (deny) deny.focus();
  scrollDown(true);
}

// onEnd finishes a turn: the answer renders as Markdown, and the actions
// and stats line appear.
function onEnd(u) {
  if (state.running === u.turn) state.running = 0;
  const t = findTurn(u.turn);
  if (t) {
    t.state = u.stopped ? "stopped" : u.error || t.error ? "failed" : "done";
    if (!u.stopped && u.error) t.error = u.error;
    t.contacted = u.contacted || [];
    if (t.approval && !t.approval.answered) t.approval.answered = "ended";
    drawAll(t, handlers);
    if (state.selected === t) drawPanel();
  }
  drawComposer();
  loadSessions();
  loadStatus();
}

// ---- The composer and the queue ----

// send asks question, or queues it while a turn runs. The text stays in
// the box when the Bridge refuses it, so nothing typed is lost.
function send(question) {
  const text = question.trim();
  if (!text) return Promise.resolve(false);
  if (state.running && state.queue.length >= MAX_QUEUE) {
    notice(MAX_QUEUE + " questions already wait. Send this one when the next starts.");
    return Promise.resolve(false);
  }
  notice("");
  return bridge.send(state.session, text).then(
    () => true,
    (err) => {
      notice(errorText(err));
      return false;
    },
  );
}

// drawComposer shows Stop while a turn runs.
function drawComposer() {
  $("stop").hidden = !state.running;
  $("send").setAttribute("aria-label", state.running ? "Queue question" : "Send question");
}

// drawQueue draws the questions waiting behind the running turn, each with
// a button that takes it out.
function drawQueue() {
  const list = $("queue");
  list.replaceChildren();
  state.queue.forEach((q, i) => {
    const li = el("li");
    li.append(el("span", "queued-label", "Queued"), el("span", "queued-text", q));
    li.append(button("", {
      className: "icon-button",
      iconName: "close",
      ariaLabel: "Remove queued question: " + q,
      onClick: () => bridge.unqueue(i).catch((err) => notice(errorText(err))),
    }));
    list.append(li);
  });
  list.hidden = state.queue.length === 0;
}

// fit grows the question box with its text, up to the CSS max-height.
function fit(box) {
  box.style.height = "auto";
  box.style.height = box.scrollHeight + "px";
}

// ---- The rail ----

// loadSessions asks merud for the list of past chats.
function loadSessions() {
  bridge.sessions().then(
    (list) => {
      state.sessions = list || [];
      state.sessionsError = "";
      drawSessions();
    },
    (err) => {
      state.sessionsError = errorText(err);
      drawSessions();
    },
  );
}

// drawSessions draws the list, filtered by the search box and grouped
// Today, Yesterday and Earlier.
function drawSessions() {
  const box = $("sessions");
  box.replaceChildren();
  const f = state.filter.toLowerCase();
  const shown = state.sessions.filter((s) => !f || s.title.toLowerCase().includes(f));
  if (state.sessionsError) {
    box.append(el("p", "rail-note", "Past chats need merud: " + state.sessionsError));
    return;
  }
  if (shown.length === 0) {
    box.append(el("p", "rail-note", f ? "No chat matches." : "No past chats yet."));
    return;
  }
  for (const g of GROUPS) {
    const rows = shown.filter((s) => s.group === g);
    if (rows.length === 0) continue;
    const section = el("section", "group");
    const headId = "group-" + g.toLowerCase();
    const head = el("h2", "group-head", g);
    head.id = headId;
    const ul = el("ul");
    ul.setAttribute("aria-labelledby", headId);
    for (const s of rows) {
      const li = el("li");
      const b = button(s.title, { className: "session", onClick: () => openSession(s) });
      b.title = s.title;
      if (s.id === state.session) b.setAttribute("aria-current", "true");
      li.append(b);
      ul.append(li);
    }
    section.append(head, ul);
    box.append(section);
  }
}

// newChat clears the conversation. A running turn stops first, and its
// queue goes with it: those questions belonged to the old chat.
function newChat() {
  if (state.running) bridge.stop();
  state.session = "";
  state.title = "New chat";
  state.turns = [];
  state.selected = null;
  drawConversation();
  drawSessions();
  $("question").focus();
}

// openSession shows a past chat. The next question continues it.
function openSession(s) {
  if (state.running) bridge.stop();
  bridge.sessionTurns(s.id).then(
    (turns) => {
      state.session = s.id;
      state.title = s.title;
      state.turns = (turns || []).map((v) =>
        newTurn({
          question: v.question,
          answer: v.answer || "",
          state: "done",
          route: v.route || "",
          outcome: v.outcome || "",
          sources: v.sources || [],
          steps: v.steps || [],
          contacted: v.contacted || [],
          stats: { duration_ms: v.duration_ms, tokens_out: v.tokens_out },
        }),
      );
      state.selected = state.turns[state.turns.length - 1] || null;
      drawConversation();
      if (state.selected) select(state.selected);
      drawSessions();
      $("question").focus();
    },
    (err) => notice(errorText(err)),
  );
}

// loadStatus asks the Bridge for merud's status and draws it.
function loadStatus() {
  bridge.status().then((s) => {
    state.status = s;
    if (s.machine) state.machine = s.machine;
    drawStatus();
  });
}

// drawStatus draws the block at the foot of the rail: where Meru runs,
// the answer model, how many files it can search and what it connects to.
// When merud doesn't answer, it says so and how to start it.
function drawStatus() {
  const box = $("status");
  box.replaceChildren();
  const s = state.status;
  if (!s) return;
  if (!s.up) {
    box.classList.add("down");
    const head = el("p", "status-head");
    head.append(icon("alert", 14), document.createTextNode(" merud isn't running"));
    box.append(head, el("p", "", s.problem || ""));
    const hint = el("p");
    hint.append(document.createTextNode("Start it in a terminal: "), el("code", "", "merud"));
    box.append(hint);
    box.append(button("Check again", { className: "text-button", onClick: loadStatus }));
    return;
  }
  box.classList.remove("down");
  const head = el("p", "status-head");
  head.append(el("span", "dot"), document.createTextNode(" Running on " + s.machine));
  box.append(head);
  if (s.model) box.append(row("Answer model", s.model));
  box.append(row("Files", s.documents.toLocaleString() + (s.scanning ? " · indexing" : "")));
  box.append(row("Connections", s.connections.length ? s.connections.join(", ") : "none"));
}

// row is one label and value line of the status block.
function row(label, value) {
  const p = el("p", "status-row");
  p.append(el("span", "status-label", label), el("span", "status-value", value));
  return p;
}

// ---- The side panel ----

// drawPanel shows what the selected answer used: its sources, its tool
// calls and who they contacted.
function drawPanel() {
  const body = $("panel-body");
  body.replaceChildren();
  const t = state.selected;
  if (!t) {
    body.append(el("p", "panel-note", "Pick an answer to see what it used."));
    return;
  }
  body.append(el("h3", "", "Sources"));
  if (t.sources.length === 0) {
    body.append(el("p", "panel-note", "No files from your folders."));
  } else {
    const ul = el("ul", "panel-list");
    for (const s of t.sources) {
      const li = el("li");
      const b = button("", { className: "panel-source", onClick: () => handlers.onOpenSource(s.path) });
      b.append(icon("file", 14), el("span", "", " " + baseName(s.path)));
      b.title = "Open " + s.path;
      li.append(b, el("span", "dim path", s.path));
      ul.append(li);
    }
    body.append(ul);
  }

  body.append(el("h3", "", "Tools"));
  if (t.steps.length === 0) {
    body.append(el("p", "panel-note", "No tools ran."));
  } else {
    const ul = el("ul", "panel-list");
    for (const s of t.steps) {
      const li = el("li");
      li.append(el("span", "", s.label));
      const where = s.server || (s.kind === "builtin" ? "built in" : s.kind);
      const time = s.duration_ms ? " · " + seconds(s.duration_ms) : "";
      const how = s.outcome && s.outcome !== "ok" ? " · " + s.outcome : "";
      li.append(el("span", "dim", where + time + how));
      ul.append(li);
    }
    body.append(ul);
  }

  const privacy = el("p", "privacy");
  privacy.append(icon("lock", 14), document.createTextNode(" " + privacyLine(t)));
  body.append(privacy);
}

// privacyLine says where the answer's work happened.
function privacyLine(t) {
  const start = "The model ran on " + state.machine + ".";
  if (t.state === "active") return start;
  const names = t.contacted || [];
  if (names.length === 0) return start + " Nothing else was contacted.";
  const list = names.length === 1 ? names[0] : names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
  return start + " Only " + list + (names.length === 1 ? " was" : " were") + " contacted.";
}

// setPanel opens or closes the side panel on a narrow window, where it
// sits over the conversation.
function setPanel(open) {
  $("panel").classList.toggle("open", open);
  $("toggle-panel").setAttribute("aria-expanded", String(open));
}

// ---- Wiring ----

function wire() {
  $("new-chat").prepend(icon("plus", 16));
  $("search-icon").append(icon("search", 15));
  $("stop").prepend(icon("stop", 14));
  $("send").prepend(icon("send", 16));
  $("toggle-panel").prepend(icon("panel", 16));
  $("close-panel").append(icon("close", 16));

  $("new-chat").addEventListener("click", newChat);
  $("search").addEventListener("input", (e) => {
    state.filter = e.target.value;
    drawSessions();
  });
  $("toggle-panel").addEventListener("click", () => setPanel(!$("panel").classList.contains("open")));
  $("close-panel").addEventListener("click", () => setPanel(false));
  $("stop").addEventListener("click", () => bridge.stop());

  const box = $("question");
  box.addEventListener("input", () => fit(box));
  box.addEventListener("keydown", (e) => {
    // Enter sends; Shift+Enter adds a line. isComposing is true while an
    // input method builds a character, when Enter belongs to it.
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      $("composer").requestSubmit();
    }
  });
  $("composer").addEventListener("submit", (e) => {
    e.preventDefault();
    send(box.value).then((ok) => {
      if (ok) {
        box.value = "";
        fit(box);
      }
    });
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && $("panel").classList.contains("open")) setPanel(false);
  });

  // Links in answers open through Go, which allows only http, https and
  // file URLs. The page itself never navigates anywhere.
  document.addEventListener("click", (e) => {
    const a = e.target.closest("a");
    if (!a) return;
    e.preventDefault();
    const href = a.getAttribute("href");
    if (href) bridge.openURL(href).catch((err) => notice(errorText(err)));
  });
  document.addEventListener("auxclick", (e) => {
    if (e.target.closest("a")) e.preventDefault();
  });
  // A file dropped on the window would make the WebView open it in place
  // of the page. Dropping files arrives in a later version.
  for (const name of ["dragover", "drop"]) {
    window.addEventListener(name, (e) => e.preventDefault());
  }

  drawConversation();
  drawComposer();
  drawQueue();
  drawSessions();
  loadStatus();
  loadSessions();
  setInterval(loadStatus, STATUS_EVERY);
  box.focus();
}

wire();
