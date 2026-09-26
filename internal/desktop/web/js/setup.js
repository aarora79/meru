// The Setup screen: four steps for a first run, and any time after from
// the rail's Setup button. The models, your folders, connections, and
// about you. Each step asks the Bridge for what it shows, and every
// change goes to merud. Meru never pulls a model on its own: the models
// step shows the command, and the user runs it.

import { bridge, copyText, errorText } from "./api.js";
import { icon } from "./icons.js";
import { el, button } from "./turns.js";
import { catalogCard, count } from "./library.js";

// STEPS are the four steps, in order.
const STEPS = ["The models", "Your folders", "Connections", "About you"];

// setup is the open Setup screen.
const setup = { root: null, step: 0, pages: null, picked: new Set(), extra: [] };

// openSetup draws the Setup screen in root at its first step.
export function openSetup(root, pages) {
  setup.root = root;
  setup.pages = pages;
  setup.step = 0;
  setup.picked = new Set();
  setup.extra = [];
  draw();
}

// draw draws the header, the step list and the open step.
function draw() {
  const root = setup.root;
  root.replaceChildren();
  const head = el("header", "page-head");
  head.append(button("Back to chat", { className: "text-button back", iconName: "back", onClick: setup.pages.back }));
  const logo = el("img", "brand-logo");
  logo.src = "img/meru-logo.svg";
  logo.alt = "";
  const title = el("h1", "", "Set up Meru");
  title.id = "setup-title";
  const brand = el("div", "setup-brand");
  brand.append(logo, title);
  head.append(brand);
  root.append(head);
  root.append(el("p", "section-intro", "Meru runs on this Mac and reads only what you point it at. Four short steps; skip any of them."));

  const steps = el("ol", "steps");
  STEPS.forEach((s, i) => {
    const li = el("li", i === setup.step ? "current" : i < setup.step ? "done" : "");
    li.append(el("span", "step-n", String(i + 1)), el("span", "", s));
    if (i === setup.step) li.setAttribute("aria-current", "step");
    steps.append(li);
  });
  root.append(steps);

  const body = el("section", "page-body");
  root.append(body);
  const nav = el("div", "setup-nav");
  if (setup.step > 0) nav.append(button("Back", { className: "button secondary", onClick: () => go(setup.step - 1) }));
  root.append(nav);
  [models, folders, connections, about][setup.step](body, nav);
}

// go opens step i.
function go(i) {
  setup.step = i;
  draw();
}

// next adds the Next button, which runs before first when given.
function next(nav, label, before) {
  const b = button(label || "Next", {
    className: "button primary",
    onClick: () => {
      const run = before ? before() : Promise.resolve();
      run.then(() => go(setup.step + 1), (err) => setup.pages.notice(errorText(err)));
    },
  });
  nav.append(b);
}

// ---- Step 1: the models ----

// models shows the three models and whether Ollama has them. merud warms
// every model when it starts and stops if one is missing, so a running
// merud means they are there; a merud that isn't running gets the pull
// commands for the models config names.
function models(body, nav) {
  const s = setup.pages.status() || {};
  body.append(el("h2", "section-head", "The models"));
  if (!s.up) {
    body.append(el("p", "error", "merud isn't running, so Meru can't check Ollama."));
    body.append(el("p", "", "Start Ollama, fetch the models config.toml names, then start merud in a terminal:"));
    for (const m of [s.model, s.fast, s.embed].filter(Boolean)) body.append(copyLine("ollama pull " + m));
    body.append(copyLine("merud"));
    body.append(el("p", "card-note", "Meru never pulls a model on its own: a model is gigabytes, so you choose when."));
    next(nav);
    return;
  }
  body.append(el("p", "panel-note", "Checking Ollama…"));
  bridge.models().then(
    (m) => {
      body.replaceChildren(el("h2", "section-head", "The models"));
      const loaded = new Set(m.loaded || []);
      const ul = el("ul", "row-list");
      for (const [label, name] of [["Answers", m.main], ["Routes each question", m.fast], ["Searches your files", m.embed]]) {
        const li = el("li", "row");
        li.append(icon("check", 16), el("span", "row-main", name), el("span", "dim", label + (loaded.has(name) ? " · loaded" : "")));
        ul.append(li);
      }
      body.append(ul);
      body.append(el("p", "", "Ollama " + (m.runtime_version || "") + " has every model merud needs; merud checked them when it started."));
      body.append(el("p", "card-note", "To use other models, set them in " + m.config_path + ", fetch each with ollama pull, and restart merud."));
    },
    (err) => body.replaceChildren(el("p", "error", errorText(err))),
  );
  next(nav);
}

// ---- Step 2: your folders ----

// folders offers the usual folders that exist here, with how many files
// each holds, as checkboxes, and a folder dialog for any other.
function folders(body, nav) {
  body.append(el("h2", "section-head", "Your folders"));
  body.append(el("p", "", "Meru searches only the folders you pick. It skips hidden files, secrets and build folders."));
  const list = el("div", "checks");
  body.append(list, el("p", "panel-note", "Loading…"));
  bridge.folders().then(
    (fv) => {
      body.lastChild.remove();
      for (const f of fv.folders) list.append(check(f.path, count(f.files, false) + " in the index", true, true));
      for (const f of fv.suggested) list.append(check(f.path, count(f.files, f.more), setup.picked.has(f.path), false));
      for (const p of setup.extra) list.append(check(p, "", true, false));
      if (fv.folders.length + fv.suggested.length + setup.extra.length === 0) {
        list.append(el("p", "panel-note", "No usual folders here; choose one below."));
      }
    },
    (err) => (body.lastChild.textContent = errorText(err)),
  );
  body.append(button("Choose another folder…", {
    className: "button secondary",
    iconName: "folder",
    onClick: () => bridge.chooseFolder().then((p) => {
      if (!p) return;
      setup.extra.push(p);
      setup.picked.add(p);
      go(setup.step);
    }, (err) => setup.pages.notice(errorText(err))),
  }));
  // Adding happens on Next, one folder after another.
  next(nav, "Next", () => addPicked());
}

// check draws one folder as a checkbox. An indexed folder shows checked
// and can't be unticked here; the Library removes it.
function check(path, note, on, fixed) {
  const id = "folder-" + path.replace(/[^a-z0-9]/gi, "-");
  const row = el("div", "check");
  const box = el("input");
  box.type = "checkbox";
  box.id = id;
  box.checked = on;
  box.disabled = fixed;
  if (!fixed) {
    if (on) setup.picked.add(path);
    box.addEventListener("change", () => (box.checked ? setup.picked.add(path) : setup.picked.delete(path)));
  }
  const label = el("label", "", path);
  label.htmlFor = id;
  row.append(box, label);
  if (note) row.append(el("span", "dim", note));
  return row;
}

// addPicked adds each ticked folder through merud, in order.
function addPicked() {
  let chain = Promise.resolve();
  for (const p of setup.picked) {
    chain = chain.then(() => bridge.addFolder(p)).then(() => setup.picked.delete(p));
  }
  return chain;
}

// ---- Step 3: connections ----

// connections offers the catalog servers not added yet. All optional.
function connections(body, nav) {
  body.append(el("h2", "section-head", "Connections"));
  body.append(el("p", "", "Optional. A connection lets Meru use your mail, calendar or notes. " +
    "Each tool starts as the catalog sets it, and you can change any of them in the Library."));
  bridge.connections().then(
    (cv) => {
      const open = cv.catalog.filter((e) => !e.added);
      const added = cv.catalog.filter((e) => e.added);
      for (const e of added) body.append(el("p", "dim", e.title + " is added."));
      for (const e of open) body.append(catalogCard(e, () => go(setup.step)));
    },
    (err) => body.append(el("p", "error", errorText(err))),
  );
  next(nav);
}

// ---- Step 4: about you ----

// QUESTIONS are the step's questions, with the kind and label each answer
// is saved under, as `meru setup user` saves them.
const QUESTIONS = [
  { id: "name", prompt: "Your name", kind: "me", label: "Name", placeholder: "Dana Reyes" },
  { id: "email", prompt: "Your email address", kind: "me", label: "Email", placeholder: "dana@example.com" },
  { id: "answers", prompt: "How you like answers", kind: "preferences", label: "Answers", placeholder: "short, with bullet points" },
];

// about asks the three questions and saves each answer as a memory.
function about(body, nav) {
  body.append(el("h2", "section-head", "About you"));
  body.append(el("p", "", "What you write here goes into every answer, and stays in ~/.meru/memory on this Mac. Leave any blank."));
  const inputs = {};
  for (const q of QUESTIONS) {
    const label = el("label", "field-label", q.prompt);
    label.htmlFor = "about-" + q.id;
    const input = el(q.id === "answers" ? "textarea" : "input", "text-input");
    input.id = "about-" + q.id;
    input.placeholder = q.placeholder;
    inputs[q.id] = input;
    body.append(label, input);
  }
  const finish = button("Finish", {
    className: "button primary",
    onClick: () => {
      let chain = Promise.resolve();
      for (const q of QUESTIONS) {
        const v = inputs[q.id].value.trim();
        if (v) chain = chain.then(() => bridge.addMemory(q.kind, q.label + ": " + v));
      }
      chain.then(() => {
        setup.pages.notice("Meru is set up. Ask it anything.");
        setup.pages.back();
      }, (err) => setup.pages.notice(errorText(err)));
    },
  });
  nav.append(finish);
}

// copyLine shows a command with a Copy button.
function copyLine(text) {
  const box = el("div", "copy-line");
  box.append(el("code", "", text));
  const b = button("Copy", {
    className: "text-button",
    iconName: "copy",
    onClick: () => copyText(text).then(() => (b.lastChild.textContent = " Copied")),
  });
  box.append(b);
  return box;
}
