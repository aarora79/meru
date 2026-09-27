// The page's state and wiring: the rail (new chat, search, the list of
// past chats, merud's status, Library and Setup), the conversation, the
// composer with its scope switch, attachments, slash commands and queue,
// and the side panel. turns.js draws each turn, library.js the Library,
// setup.js the Setup screen, commands.js the slash commands; api.js
// reaches Go.
//
// The Bridge in Go owns the running turn and the queue. The page keeps
// only what it draws, and redraws from each Update the Bridge sends.

import { bridge, onUpdate, copyText, errorText } from "./api.js";
import { icon } from "./icons.js";
import {
  el, button, baseName, seconds, createTurn, drawAll, drawStrip, drawApproval, drawNotice,
  appendToken, approvalCard, thumb,
} from "./turns.js";
import { setupCommands, menuKey, runCommand } from "./commands.js";
import { openLibrary, libraryPanel, policyWords } from "./library.js";
import { openSetup } from "./setup.js";

// How often the rail asks merud for its status, in milliseconds.
const STATUS_EVERY = 15000;
// The most questions that wait behind a running turn, as in the Bridge.
const MAX_QUEUE = 5;
// The groups of the rail's list, in the order they show.
const GROUPS = ["Today", "Yesterday", "Earlier"];
// The composer's "Where Meru looks" switch, in the order it shows. The
// values are merud's scopes (internal/rpc/settings.go).
const SCOPES = [
  { value: "auto", label: "Auto", hint: "Meru decides where to look" },
  { value: "files", label: "My files", hint: "Search the folders Meru indexes" },
  { value: "mail", label: "Mail and calendar", hint: "Use your mail and calendar connections only" },
  { value: "web", label: "Web", hint: "Search the web only" },
  { value: "talk", label: "Just talk", hint: "No search and no tools" },
];

const state = {
  view: "chat", // "chat", "library" or "setup"
  session: "", // the open conversation's ID; "" for a new one
  title: "New chat",
  turns: [], // the open conversation's turns, oldest first
  running: 0, // the running turn's number, or 0
  queue: [], // questions waiting behind it
  quietDrop: false, // the next queue notice is part of /new's own notice
  sessions: [], // the rail's list
  sessionsError: "",
  filter: "",
  selected: null, // the turn the side panel shows
  asking: null, // the turn whose approval card is open, for the panel
  askingTools: null, // that server's connection, once the Bridge sends it
  status: null,
  setupShown: false, // Setup opened on its own once already
  machine: "this Mac",
  scope: "auto",
  attachments: [], // the files and images attached to the next question, as the Bridge last sent them
  saves: {}, // open save approval cards, by card ID
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
    // Edit first answers "Don't send", then puts the draft in the box:
    // merud runs a call with the arguments it asked about or not at all.
    const answer = choice === "edit" ? "deny" : choice;
    bridge.approve(a.view.id, answer).then(
      () => {
        a.answered = choice;
        drawApproval(t, handlers);
        drawStrip(t, handlers);
        if (state.asking === t) {
          state.asking = null;
          drawPanel();
        }
        const box = $("question");
        if (choice === "edit") {
          box.value = a.view.draft;
          fit(box);
          notice("Change the draft, then send it. Meru asks again before it does anything.");
        }
        box.focus();
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
  onSaveNote(t) {
    save(() => bridge.saveNote(state.session, t.answer), "note");
  },
  onRetry(t) {
    send(t.question, t.scope || state.scope, (t.images || []).map((i) => i.path));
  },
  whyText: (v) => whyText(v),
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

// notice shows one line above the composer; screen readers hear it. An
// action, { label, onClick }, adds a button after the text.
function notice(text, action) {
  const p = $("notice");
  p.replaceChildren(document.createTextNode(text || ""));
  if (action) p.append(" ", button(action.label, { className: "text-button", onClick: action.onClick }));
}

// ---- Views: the chat, the Library and Setup ----

// showView shows one of the three screens in the middle column. The side
// panel follows: what an answer used in the chat, "On this Mac" in the
// Library, and nothing during Setup.
function showView(view) {
  state.view = view;
  $("chat-view").hidden = view !== "chat";
  $("library-view").hidden = view !== "library";
  $("setup-view").hidden = view !== "setup";
  $("app").classList.toggle("no-panel", view === "setup");
  $("open-library").setAttribute("aria-current", String(view === "library"));
  $("open-setup").setAttribute("aria-current", String(view === "setup"));
  drawPanel();
  if (view === "chat") $("question").focus();
}

// pages is what library.js and setup.js call back into.
const pages = {
  back: () => {
    showView("chat");
    loadStatus();
  },
  notice,
  status: () => state.status,
  refreshStatus: () => loadStatus(),
  openLibrary: (section, focus) => goLibrary(section, focus),
};

// goLibrary opens the Library at section, and at the connection focus
// when given.
function goLibrary(section, focus) {
  showView("library");
  openLibrary($("library-view"), section || "connections", focus, pages);
}

// goSetup opens the Setup screen.
function goSetup() {
  showView("setup");
  openSetup($("setup-view"), pages);
}

// ---- The conversation ----

// newTurn returns an empty turn record.
function newTurn(fields) {
  return {
    key: state.nextKey++,
    n: 0,
    question: "",
    images: [], // the images the question carried, each with its preview
    scope: "",
    state: "active",
    route: "",
    confidence: 0,
    fallback: false,
    skills: [],
    steps: [],
    sources: [], // every file the prompt held, for the side panel
    cited: [], // the ones the finished answer cites, for the line under it
    showSources: false, // whether that line is open
    memories: [],
    answer: "",
    stats: null,
    error: "",
    outcome: "",
    notice: "", // merud's warning under an answer: a claim no tool backs, or a model without tools
    approval: null,
    contacted: [],
    showSteps: false,
    ...fields,
  };
}

// drawConversation redraws the title, every turn, or the empty state.
function drawConversation() {
  $("chat-title").textContent = state.title;
  $("share").hidden = !state.session;
  const list = $("messages");
  list.replaceChildren();
  if (state.turns.length === 0) {
    list.append(emptyState());
  }
  for (const t of state.turns) {
    list.append(createTurn(t, handlers));
  }
  numberBlocks();
  drawPanel();
  scrollDown(true);
}

// emptyState is what a new chat shows: the logo beside "Ask Meru", a line
// on what Meru does, and a few questions to start with. The logo's alt
// text is empty, because the heading beside it already says Meru.
function emptyState() {
  const box = el("div", "empty");
  const head = el("div", "empty-head");
  const logo = el("img", "empty-logo");
  logo.src = "img/meru-logo.svg";
  logo.alt = "";
  logo.width = 48;
  logo.height = 48;
  head.append(logo, el("h2", "", "Ask Meru"));
  box.append(head);
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

// numberBlocks numbers every code block in the chat's finished answers,
// oldest first, from 1, as `meru chat` does, so /copy N names one.
function numberBlocks() {
  blocks().forEach((b, i) => {
    const n = b.querySelector(".code-n");
    if (n) n.textContent = String(i + 1);
    const copyButton = b.querySelector(".code-head button:last-child");
    if (copyButton) copyButton.setAttribute("aria-label", "Copy code block " + (i + 1));
  });
}

// blocks returns every code block in the chat, oldest first. A streaming
// answer shows plain text, so only finished answers have blocks.
function blocks() {
  return [...$("messages").querySelectorAll(".code-block")];
}

// newestBlocks returns the code blocks of the newest finished answer, or
// null when no answer has finished.
function newestBlocks() {
  for (let i = state.turns.length - 1; i >= 0; i--) {
    const t = state.turns[i];
    if (t.state !== "active" && t.el) return [...t.el.body.querySelectorAll(".code-block")];
  }
  return null;
}

// codeText returns a code block's text, without the line break a code
// block ends with.
function codeText(block) {
  const code = block.querySelector("pre code") || block.querySelector("pre");
  return (code ? code.textContent : "").replace(/\n$/, "");
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
      if (u.approval.task === "save") onSaveApproval(u);
      else onApproval(u);
      break;
    case "end":
      onEnd(u);
      break;
    case "queue":
      state.queue = u.queue || [];
      drawQueue();
      if (u.notice && !state.quietDrop) notice(u.notice);
      state.quietDrop = false;
      break;
    case "attachments":
      onAttachments(u);
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
  const t = newTurn({ n: u.turn, question: u.question, images: u.images || [], scope: state.scope });
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
    $("share").hidden = false;
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
      // The line under the answer waits for the end, when the Bridge
      // says which of these the answer cites.
      t.sources = ev.sources || [];
      break;
    case "memories":
      t.memories = ev.memories || [];
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
    // "notice" is rpc.EventNotice, which #29 (branch honest-actions) adds:
    // merud sends it after the last token when the answer claims an action,
    // such as moving a folder, and no tool call in the turn succeeded. The
    // page names it here, in one place, so it works once #29 merges; until
    // then merud never sends it. Any other type the page doesn't know, it
    // skips.
    case "notice":
      t.notice = ev.text || "";
      drawNotice(t);
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

// onApproval shows the approval card, turns the side panel to "Why Meru
// is asking", and moves focus to the answer that does nothing, so a stray
// Enter can't approve a call.
function onApproval(u) {
  const t = findTurn(u.turn);
  if (!t) return;
  t.approval = { view: u.approval, answered: null };
  drawApproval(t, handlers);
  drawStrip(t, handlers);
  state.asking = t;
  state.askingTools = null;
  drawPanel();
  loadAskingTools(u.approval);
  const deny = t.el.approval.querySelector('[data-choice="deny"]');
  if (deny) deny.focus();
  scrollDown(true);
}

// loadAskingTools fetches the connection the open card's call goes to, so
// the panel can list its tools and their policies.
function loadAskingTools(v) {
  bridge.connections().then(
    (cv) => {
      const kind = v.kind === "builtin" ? "builtin" : v.kind;
      const name = v.kind === "builtin" ? "meru" : v.server;
      state.askingTools = (cv.connections || []).find((c) => c.kind === kind && c.name === name) || null;
      drawPanel();
    },
    () => {},
  );
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
    t.cited = u.cited || [];
    if (t.approval && !t.approval.answered) t.approval.answered = "ended";
    if (state.asking === t) state.asking = null;
    drawAll(t, handlers);
    numberBlocks();
    if (state.selected === t) drawPanel();
  }
  drawComposer();
  loadSessions();
  loadStatus();
}

// ---- Saving: Share as file and Save to a note ----

// save runs a save through merud, which asks first as write_file does.
// The card shows above the composer; the notice says where the file went
// and offers to show it in its folder.
function save(run, what) {
  if (!state.session) {
    notice("Ask something first; there is nothing to save yet.");
    return;
  }
  notice("Saving the " + what + "…");
  run().then(
    (path) => {
      clearSaveCards();
      notice("Saved the " + what + " to " + path + ".", {
        label: "Show in folder",
        onClick: () => bridge.reveal(path).catch((err) => notice(errorText(err))),
      });
    },
    (err) => {
      clearSaveCards();
      notice(errorText(err));
    },
  );
}

// onSaveApproval shows a save's approval card above the composer.
function onSaveApproval(u) {
  const v = u.approval;
  const slot = $("task-slot");
  const card = approvalCard(v, "save-" + v.id, (choice) => {
    if (choice === "edit") choice = "deny";
    bridge.approve(v.id, choice).catch((err) => notice(errorText(err)));
    card.remove();
  });
  // A save's content is the chat or the answer; the card needn't repeat
  // it, and Edit first has nothing to edit.
  for (const extra of card.querySelectorAll(".args, [data-choice='edit']")) extra.remove();
  state.saves[v.id] = card;
  slot.append(card);
  const deny = card.querySelector('[data-choice="deny"]');
  if (deny) deny.focus();
}

// clearSaveCards takes down any save card left open.
function clearSaveCards() {
  for (const id of Object.keys(state.saves)) {
    state.saves[id].remove();
    delete state.saves[id];
  }
}

// ---- The composer and the queue ----

// send asks question, or queues it while a turn runs. The text stays in
// the box when the Bridge refuses it, so nothing typed is lost. images,
// for Try again, are the paths of the images the first asking carried.
function send(question, scope, images) {
  let text = question.trim();
  if (!text) return Promise.resolve(false);
  if (state.running && state.queue.length >= MAX_QUEUE) {
    notice(MAX_QUEUE + " questions already wait. Send this one when the next starts.");
    return Promise.resolve(false);
  }
  notice("");
  // The Bridge adds a "Read this file" line for each attached file,
  // sends the images with the question, and clears the chips.
  const where = scope === "auto" ? "" : scope;
  const call = images && images.length > 0
    ? bridge.retry(state.session, text, where, images)
    : bridge.send(state.session, text, where);
  return call.then(
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

// drawScope draws the "Where Meru looks" switch: one radio button per
// scope, the arrow keys moving between them as in any radio group.
function drawScope() {
  const box = $("scope");
  box.replaceChildren();
  SCOPES.forEach((s, i) => {
    const on = s.value === state.scope;
    const b = button(s.label, { className: "scope-option" });
    b.setAttribute("role", "radio");
    b.setAttribute("aria-checked", String(on));
    b.tabIndex = on ? 0 : -1;
    b.title = s.hint;
    b.addEventListener("click", () => setScope(s.value));
    b.addEventListener("keydown", (e) => {
      const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
      if (!step) return;
      e.preventDefault();
      const next = SCOPES[(i + step + SCOPES.length) % SCOPES.length];
      setScope(next.value);
      box.querySelector('[aria-checked="true"]').focus();
    });
    box.append(b);
  });
}

// setScope picks where the next questions look.
function setScope(value) {
  state.scope = value;
  drawScope();
}

// attach shows the system's file dialog. merud copies each file picked
// into its output folder, where read_file may read a file and a question
// may carry an image, and the Bridge sends the chips back as an
// "attachments" update. A file dropped on the chat takes the same path,
// from Go.
function attach() {
  bridge.attachFile().catch((err) => notice(errorText(err)));
}

// onAttachments draws the chips the Bridge sent, and says which files
// stayed out and why. A new file switches a scope other than Auto or My
// files to My files, the scope that offers read_file. A new image
// switches nothing: it goes with the question in every scope.
function onAttachments(u) {
  const files = (list) => list.filter((a) => a.kind !== "image").length;
  const before = state.attachments.length;
  const filesBefore = files(state.attachments);
  state.attachments = u.attachments || [];
  drawAttachments();
  let text = u.notice || "";
  const added = state.attachments.length > before;
  const addedFile = files(state.attachments) > filesBefore;
  if (addedFile && state.scope !== "auto" && state.scope !== "files") {
    setScope("files");
    text = (text + " Switched to My files, so Meru can read the files.").trim();
  }
  if (text) notice(text);
  if (added) $("question").focus();
}

// drawAttachments shows each attachment as a chip in the composer, with
// its size and a button that takes it off. An image's chip shows its
// preview in place of the file icon.
function drawAttachments() {
  const box = $("attachments");
  box.replaceChildren();
  box.hidden = state.attachments.length === 0;
  state.attachments.forEach((a, i) => {
    const image = a.kind === "image";
    const chip = el("span", "attachment" + (image ? " image" : ""));
    chip.append(image ? thumb(a, "attachment-thumb") : icon("file", 14), el("span", "", a.name));
    if (a.size) chip.append(el("span", "attachment-size", a.size));
    chip.title = image ? "Meru sends this image with your question" : "Meru reads its copy, " + a.path;
    chip.append(button("", {
      className: "icon-button small",
      iconName: "close",
      ariaLabel: "Remove " + a.name,
      onClick: () => bridge.detach(i).catch((err) => notice(errorText(err))),
    }));
    box.append(chip);
  });
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

// askQuit runs /exit: it closes the app at once, or, while a turn runs,
// asks first in the notice line.
function askQuit() {
  if (!state.running) {
    bridge.quit().catch((err) => notice(errorText(err)));
    return;
  }
  notice("A question is still running. Close Meru anyway?", {
    label: "Close Meru",
    onClick: () => bridge.quit().catch((err) => notice(errorText(err))),
  });
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

// newChat clears the conversation, from the New chat button or /new. A
// running turn stops first, and its queue goes with it: those questions
// belonged to the old chat. The notice matches `meru chat`'s.
function newChat() {
  const dropped = state.queue.length;
  if (state.running) {
    state.quietDrop = true;
    bridge.stop();
  }
  state.session = "";
  state.title = "New chat";
  state.turns = [];
  state.selected = null;
  state.asking = null;
  bridge.detachAll().catch((err) => notice(errorText(err)));
  showView("chat");
  drawConversation();
  drawSessions();
  let text = "new session: the next question starts fresh";
  if (dropped === 1) text += " · dropped 1 queued question";
  if (dropped > 1) text += " · dropped " + dropped + " queued questions";
  notice(text);
  $("question").focus();
}

// openSession shows a past chat. The next question continues it.
function openSession(s) {
  if (state.running) bridge.stop();
  bridge.sessionTurns(s.id).then(
    (turns) => {
      state.session = s.id;
      state.title = s.title;
      state.asking = null;
      state.turns = (turns || []).map((v) =>
        newTurn({
          question: v.question,
          images: v.images || [],
          answer: v.answer || "",
          state: "done",
          route: v.route || "",
          outcome: v.outcome || "",
          notice: v.notice || "",
          sources: v.sources || [],
          cited: v.cited || [],
          steps: v.steps || [],
          contacted: v.contacted || [],
          stats: { duration_ms: v.duration_ms, tokens_out: v.tokens_out },
        }),
      );
      state.selected = state.turns[state.turns.length - 1] || null;
      showView("chat");
      drawConversation();
      if (state.selected) select(state.selected);
      drawSessions();
      $("question").focus();
    },
    (err) => notice(errorText(err)),
  );
}

// loadStatus asks the Bridge for merud's status and draws it. The first
// time merud reports no folders and no profile, Setup opens on its own.
function loadStatus() {
  bridge.status().then((s) => {
    state.status = s;
    if (s.machine) state.machine = s.machine;
    drawStatus();
    if (s.up && s.folders === 0 && s.profile === 0 && !state.setupShown && state.turns.length === 0) {
      state.setupShown = true;
      goSetup();
    }
    if (state.view === "library") drawPanel();
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
  $("mini-status").classList.toggle("down", !s.up);
  $("mini-status").setAttribute("aria-label", s.up ? "merud is running" : "merud isn't running");
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

// setRail collapses the rail to a column of icons, or opens it again.
function setRail(open) {
  $("app").classList.toggle("rail-collapsed", !open);
  $("rail").hidden = !open;
  $("mini-rail").hidden = open;
  $("rail-toggle").setAttribute("aria-expanded", String(open));
  (open ? $("rail-toggle") : $("mini-expand")).focus();
}

// ---- The side panel ----

// drawPanel draws the side panel for the screen and the moment: "On this
// Mac" in the Library, "Why Meru is asking" while an approval card is
// open, and otherwise what the selected answer used.
function drawPanel() {
  const body = $("panel-body");
  body.replaceChildren();
  if (state.view === "library") {
    $("panel-title").textContent = "On this Mac";
    libraryPanel(body, state.status);
    return;
  }
  if (state.asking && state.asking.approval && !state.asking.approval.answered) {
    $("panel-title").textContent = "Why Meru is asking";
    drawAsking(body, state.asking.approval.view);
    return;
  }
  $("panel-title").textContent = "What this answer used";
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

  body.append(el("h3", "", "Remembered"));
  if (t.memories.length === 0) {
    body.append(el("p", "panel-note", t.state === "active" || t.n ? "Nothing Meru remembered came up." :
      "Past chats don't record what was remembered."));
  } else {
    const ul = el("ul", "panel-list");
    for (const m of t.memories) {
      const li = el("li", "memory-row");
      li.append(el("span", "", m.text));
      li.append(button("Forget", {
        className: "text-button",
        ariaLabel: "Forget: " + m.text,
        onClick: () => forget(t, m),
      }));
      ul.append(li);
    }
    body.append(ul);
  }
  const profile = el("p", "panel-note");
  profile.append(document.createTextNode("What you told Meru about yourself goes into every answer. "));
  profile.append(button("About you", { className: "text-button link", onClick: () => goLibrary("you") }));
  body.append(profile);

  const privacy = el("p", "privacy");
  privacy.append(icon("lock", 14), document.createTextNode(" " + privacyLine(t)));
  body.append(privacy);
}

// forget deletes memory m through merud and takes it off the panel.
function forget(t, m) {
  bridge.forgetMemory(m.id).then(
    () => {
      t.memories = t.memories.filter((x) => x.id !== m.id);
      notice("Forgot: " + m.text);
      drawPanel();
    },
    (err) => notice(errorText(err)),
  );
}

// drawAsking fills the panel while an approval card is open: why Meru
// asks, what else the server may do, and where the call is logged.
function drawAsking(body, v) {
  body.append(el("p", "", whyText(v)));
  const c = state.askingTools;
  body.append(el("h3", "", v.server ? "What " + v.server + " may do" : "Meru's own tools"));
  if (!c) {
    body.append(el("p", "panel-note", "Loading…"));
  } else {
    const ul = el("ul", "panel-list");
    const on = c.tools.filter((p) => p.policy !== "off");
    for (const p of on) {
      const li = el("li", "policy-row" + (p.name === v.tool ? " current" : ""));
      li.append(el("span", "", p.name), el("span", "policy-badge " + p.policy, policyWords(p.policy)));
      ul.append(li);
    }
    body.append(ul);
    const off = c.tools.length - on.length;
    if (off > 0) body.append(el("p", "panel-note", off + " more " + (off === 1 ? "tool is" : "tools are") + " off."));
  }
  const name = v.server || "Meru";
  body.append(button("Change what " + name + " may do", {
    className: "text-button link",
    // The built-in web tools have a card of their own in the Library.
    onClick: () => goLibrary("connections", v.server || (["web_search", "web_fetch"].includes(v.tool) ? "web" : "meru")),
  }));
  const log = el("p", "privacy");
  log.append(icon("lock", 14), document.createTextNode(" Every call, allowed or not, goes in the tool log. "));
  log.append(button("Library › Activity", { className: "text-button link", onClick: () => goLibrary("activity") }));
  body.append(log);
}

// whyText says why the call on approval card v waits for the user. The
// card and the side panel both show it.
function whyText(v) {
  const where = v.server || (v.kind === "builtin" ? "Meru" : v.kind);
  const policy = state.askingTools && (state.askingTools.tools.find((p) => p.name === v.tool) || {}).policy;
  if (v.tool === "configure" || policy === "always") {
    return v.tool + " asks every time. It can change Meru's settings or run commands, so no approval lasts beyond one call.";
  }
  if (v.tool === "web_fetch") {
    return "Meru asks before it reads a web address that no search or question of yours gave, and before any download.";
  }
  return "This call goes to " + where + " and can change something there. " + v.tool +
    " is set to Ask, so Meru waits for you before each call.";
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

// setPanel shows or hides the side panel. On a wide window it takes its
// column back; on a narrow one it slides over the conversation.
function setPanel(open) {
  $("app").classList.toggle("panel-hidden", !open);
  $("panel").classList.toggle("open", open);
  $("toggle-panel").setAttribute("aria-expanded", String(open));
  $("toggle-panel").setAttribute("aria-label", open ? "Hide the side panel" : "Show the side panel");
}

// panelOpen reports whether the side panel shows.
function panelOpen() {
  return $("toggle-panel").getAttribute("aria-expanded") === "true";
}

// ---- Wiring ----

function wire() {
  $("new-chat").prepend(icon("plus", 16));
  $("search-icon").append(icon("search", 15));
  $("stop").prepend(icon("stop", 14));
  $("send").prepend(icon("send", 16));
  $("toggle-panel").append(icon("panel", 18));
  $("close-panel").append(icon("close", 16));
  $("rail-toggle").append(icon("sidebar", 17));
  $("mini-expand").append(icon("sidebar", 18));
  $("mini-new").append(icon("plus", 18));
  $("mini-chats").append(icon("chats", 18));
  $("mini-library").append(icon("book", 18));
  $("open-library").prepend(icon("book", 16), document.createTextNode(" "));
  $("open-setup").prepend(icon("sliders", 16), document.createTextNode(" "));
  $("share").prepend(icon("share", 15), document.createTextNode(" "));
  $("attach").append(icon("clip", 17));

  $("new-chat").addEventListener("click", newChat);
  $("mini-new").addEventListener("click", newChat);
  $("rail-toggle").addEventListener("click", () => setRail(false));
  $("mini-expand").addEventListener("click", () => setRail(true));
  $("mini-chats").addEventListener("click", () => {
    setRail(true);
    $("search").focus();
  });
  $("mini-library").addEventListener("click", () => goLibrary("connections"));
  $("open-library").addEventListener("click", () => goLibrary("connections"));
  $("open-setup").addEventListener("click", goSetup);
  // The logo and the name at the top of the rail open the Library's About.
  $("open-about").addEventListener("click", () => goLibrary("about"));
  // The version beside the logo: short in the rail, in full as its
  // tooltip. It stays empty when the build carries no useful version.
  bridge.about().then((a) => {
    const v = $("brand-version");
    v.textContent = a.short_version || "";
    if (a.version) v.title = "Version " + a.version;
  }).catch(() => {});
  $("share").addEventListener("click", () => save(() => bridge.saveChat(state.session), "chat"));
  $("attach").addEventListener("click", attach);
  $("search").addEventListener("input", (e) => {
    state.filter = e.target.value;
    drawSessions();
  });
  $("toggle-panel").addEventListener("click", () => setPanel(!panelOpen()));
  $("close-panel").addEventListener("click", () => setPanel(false));
  $("stop").addEventListener("click", () => bridge.stop());

  const box = $("question");
  setupCommands(box, $("command-menu"), {
    newChat,
    openLibrary: (section) => goLibrary(section),
    blocks,
    newestBlocks,
    textOf: codeText,
    copy: copyText,
    notice,
    askQuit,
    fit: () => fit(box),
    running: () => state.running !== 0,
    refreshStatus: () => loadStatus(),
  });
  box.addEventListener("input", () => fit(box));
  box.addEventListener("keydown", (e) => {
    if (menuKey(e)) return;
    // Enter sends; Shift+Enter adds a line. isComposing is true while an
    // input method builds a character, when Enter belongs to it.
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      $("composer").requestSubmit();
    }
  });
  $("composer").addEventListener("submit", (e) => {
    e.preventDefault();
    // A line that starts with "/" is a command: it runs here, at once,
    // and never reaches the model.
    if (box.value.trim().startsWith("/")) {
      if (runCommand(box.value)) {
        box.value = "";
        fit(box);
      }
      return;
    }
    send(box.value, state.scope).then((ok) => {
      if (ok) {
        box.value = "";
        fit(box);
      }
    });
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && $("panel").classList.contains("open") && window.innerWidth <= 1180) setPanel(false);
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
  // of the page. Wails reports a drop on the chat screen to Go, which
  // attaches the files; the overlay that says so is CSS on the class Wails
  // puts on the screen while files hover over it.
  for (const name of ["dragover", "drop"]) {
    window.addEventListener(name, (e) => e.preventDefault());
  }

  // The side panel starts closed, and the header's button opens it. The
  // choice lasts while the window stays open; nothing stores it, so the
  // next start opens closed again. An approval card doesn't open it: the
  // card says why Meru asks.
  setPanel(false);
  drawScope();
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
