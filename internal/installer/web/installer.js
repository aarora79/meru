// The installer's page. It draws the list of steps and one screen at a
// time, and calls the Bridge (internal/installer/bridge.go) through Wails
// for everything: reading a screen's data, running or skipping a step,
// picking a folder, opening a link. Each step's news arrives as the event
// "installer:progress". The page makes no other request.

import { Call, Events } from "/wails/runtime.js";

// Wails names a bound method by its Go package path, type and method.
const BRIDGE = "github.com/aarora79/meru/internal/installer.Bridge.";
const call = (method, ...args) => Call.ByName(BRIDGE + method, ...args);

const $ = (id) => document.getElementById(id);

// el makes an element with a class and text, and appends the children.
function el(tag, cls, text, ...children) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  for (const c of children) if (c) e.append(c);
  return e;
}

function button(label, cls, onClick) {
  const b = el("button", "button " + (cls || ""), label);
  b.type = "button";
  b.addEventListener("click", onClick);
  return b;
}

function errorText(err) {
  return String((err && err.message) || err || "Something went wrong.");
}

// state holds what the page knows: the steps from the Bridge, which screen
// shows, the screen's data, and what the user typed on each form.
const state = {
  view: null,
  at: -1, // -1 is the welcome screen, steps.length the summary
  screen: null,
  running: false,
  choice: -1,
  folders: [],
  commands: [],
  googlePage: 0,
  google: { email: "", clientID: "", secret: "", adopt: true },
  obsidian: { vault: "", adopt: true },
  profile: null,
  meru: { clearQuarantine: true, addPath: true },
};

const MARKS = { done: "✓", skipped: "–", failed: "!", running: "…", pending: "" };

function steps() {
  return state.view ? state.view.steps : [];
}

function current() {
  return steps()[state.at];
}

// ---- The rail ----

function drawRail() {
  const list = $("step-list");
  list.replaceChildren();
  steps().forEach((s, i) => {
    const mark = el("span", "mark " + s.status, MARKS[s.status] || String(i + 1));
    if (s.status === "pending") mark.textContent = String(i + 1);
    const b = el("button", i === state.at ? "current" : "", null, mark, el("span", "", s.title));
    b.type = "button";
    // A step can be opened again once it has run or been skipped, and the
    // next one in line can be opened too.
    const reachable = s.status !== "pending" || i <= firstOpen();
    b.disabled = state.running || !reachable;
    b.addEventListener("click", () => show(i));
    list.append(el("li", "", null, b));
  });
  $("config-note").textContent = "Your settings live in " + (state.view ? state.view.config : "~/.meru/config.toml") + ".";
}

// firstOpen is the first step not yet done or skipped.
function firstOpen() {
  const i = steps().findIndex((s) => s.status !== "done" && s.status !== "skipped");
  return i < 0 ? steps().length : i;
}

// ---- Screens ----

async function show(i) {
  state.at = i;
  state.screen = null;
  $("progress").hidden = true;
  $("log").textContent = "";
  $("outcome").hidden = true;
  drawRail();
  if (i < 0) return drawWelcome();
  if (i >= steps().length) return drawSummary();
  const s = current();
  try {
    state.screen = await call("Screen", s.id);
  } catch (err) {
    state.screen = { error: errorText(err) };
  }
  drawStep();
}

function drawWelcome() {
  $("step-count").textContent = "";
  $("title").textContent = "Welcome";
  const body = $("body");
  body.replaceChildren(
    el("p", "", "This installer sets up Meru, a personal AI assistant that runs on this Mac. The models run here, and your questions and files stay here."),
    el("p", "", "It takes ten steps. Each one says what it does, what it downloads and how long it takes, and waits for you. You can skip any step but About you, and run this installer again later to finish one."),
    el("p", "", "At the end you have web search, your notes and your mail if you want them, your chosen folders in Meru's search index, the built-in skills and a few commands that only read, and the path of the one file that holds every setting."),
    el("div", "warn", "Meru isn't signed by Apple, because it is a personal project with no Apple developer account. That is why macOS asked before it opened this installer. The second step offers to clear the same warning for Meru.app, so it opens like any other app."),
  );
  const foot = $("foot");
  foot.replaceChildren(el("span", "spacer"), button("Start", "primary", () => show(firstOpen())));
}

function drawStep() {
  const s = current();
  $("step-count").textContent = `Step ${state.at + 1} of ${steps().length}`;
  $("title").textContent = s.title;
  const body = $("body");
  body.replaceChildren(
    el("p", "", s.what),
    el("p", "dim", s.why + " " + s.cost),
  );
  if (s.found) body.append(el("p", "found", "Already done: " + s.found));
  if (state.screen && state.screen.error) body.append(el("p", "error", state.screen.error));
  const form = FORMS[s.id];
  if (form && state.screen && !state.screen.error) form(body, state.screen);
  drawOutcome();
  drawFoot();
}

function drawOutcome() {
  const s = current();
  const out = $("outcome");
  if (!s || !s.detail || (s.status !== "done" && s.status !== "failed")) {
    out.hidden = true;
    return;
  }
  out.hidden = false;
  out.className = "outcome " + s.status;
  out.textContent = (s.status === "failed" ? "This step failed: " : "") + s.detail;
}

function drawFoot() {
  const s = current();
  const foot = $("foot");
  foot.replaceChildren();
  if (s.id === "google" && state.googlePage > 0 && s.status !== "done") {
    foot.append(button("Back", "secondary", () => { state.googlePage--; drawStep(); }));
  } else if (state.at > 0) {
    foot.append(button("Back", "secondary", () => show(state.at - 1)));
  }
  foot.append(el("span", "spacer"));
  if (s.id === "google" && state.googlePage < 2 && s.status !== "done") {
    if (s.skippable) foot.append(button("Skip", "secondary", skip));
    foot.append(button("Next", "primary", () => { state.googlePage++; drawStep(); }));
    return;
  }
  if (s.status === "done") {
    foot.append(button("Run again", "secondary", run));
    foot.append(button(state.at + 1 < steps().length ? "Next step" : "See the summary", "primary", () => show(state.at + 1)));
    return;
  }
  if (s.skippable) foot.append(button("Skip", "secondary", skip));
  foot.append(button(s.status === "failed" ? "Retry" : "Continue", "primary", run));
}

// setBusy turns every button off while a step runs.
function setBusy(busy) {
  state.running = busy;
  for (const b of document.querySelectorAll("button")) b.disabled = busy;
  if (!busy) drawRail();
}

async function run() {
  const s = current();
  const input = collect(s.id);
  if (input.error) {
    $("outcome").hidden = false;
    $("outcome").className = "outcome failed";
    $("outcome").textContent = input.error;
    return;
  }
  $("progress").hidden = false;
  $("log").textContent = "";
  $("bar").hidden = true;
  $("outcome").hidden = true;
  setBusy(true);
  try {
    state.view = await call("Run", s.id, input);
  } catch (err) {
    $("outcome").hidden = false;
    $("outcome").className = "outcome failed";
    $("outcome").textContent = errorText(err);
  }
  setBusy(false);
  // The screen stays, with what the step did or why it failed, until the
  // user presses Next step, Retry or Skip.
  drawStep();
  $("progress").hidden = $("log").textContent === "";
}

async function skip() {
  try {
    state.view = await call("Skip", current().id);
    show(state.at + 1);
  } catch (err) {
    $("outcome").hidden = false;
    $("outcome").className = "outcome failed";
    $("outcome").textContent = errorText(err);
  }
}

// collect gathers what the current form holds into the Bridge's Input.
function collect(id) {
  const input = { choice: state.choice, meru: state.meru, folders: [], commands: [], obsidian: state.obsidian, google: state.google, profile: state.profile || {} };
  if (id === "folders") input.folders = state.folders.filter((f) => f.chosen).map((f) => f.path);
  if (id === "skills") input.commands = state.commands.filter((c) => c.chosen && !c.on).map((c) => c.name);
  if (id === "profile") {
    const p = state.profile || {};
    if (!p.name || !p.name.trim()) return { error: "Type your name to go on. Meru needs it to tell you apart from the people in your files." };
    if (state.screen && state.screen.emailRequired && !(p.email || "").trim()) {
      return { error: "Type your email address to go on. The Google tools need it." };
    }
  }
  return input;
}

// ---- One form per step ----

function checkbox(checked, disabled, onChange) {
  const box = el("input");
  box.type = "checkbox";
  box.checked = checked;
  box.disabled = disabled;
  box.addEventListener("change", () => onChange(box.checked));
  return box;
}

function field(label, value, hint, onInput, type) {
  const input = el("input");
  input.type = type || "text";
  input.value = value || "";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.addEventListener("input", () => onInput(input.value));
  const id = "f-" + label.replace(/\W+/g, "-").toLowerCase();
  input.id = id;
  const l = el("label", "", label);
  l.htmlFor = id;
  return el("div", "field", null, l, input, hint ? el("span", "hint", hint) : null);
}

// choice draws two radio options, yes and no, each [title, text], for a
// step that can adopt a server set up by hand. onChange gets true for yes.
function choice(group, yes, onChange, yesText, noText) {
  const set = el("fieldset", "options");
  for (const [value, [title, text]] of [[true, yesText], [false, noText]]) {
    const radio = el("input");
    radio.type = "radio";
    radio.name = "adopt-" + group;
    radio.checked = value === yes;
    radio.addEventListener("change", () => onChange(value));
    set.append(el("label", "option", null, radio, el("span", "", null, el("strong", "", title), el("p", "", text))));
  }
  return set;
}

function linkButton(label, name) {
  return button(label, "secondary", () => call("OpenLink", name).catch(() => {}));
}

const FORMS = {
  check(body, screen) {
    const m = screen.machine;
    body.append(el("div", "facts", null,
      fact("Chip", m.chip || "unknown"),
      fact("macOS", m.macos || "unknown"),
      fact("Memory", m.memoryGB + " GB"),
      fact("Free disk", m.freeDiskGB ? m.freeDiskGB + " GB" : "unknown"),
    ));
    for (const w of m.warnings || []) body.append(el("p", "warn", w));
    if (state.choice < 0) state.choice = m.recommended;
    const set = el("fieldset", "options");
    set.append(el("legend", "", "Models for this Mac"));
    m.choices.forEach((c, i) => {
      const radio = el("input");
      radio.type = "radio";
      radio.name = "choice";
      radio.checked = i === state.choice;
      radio.addEventListener("change", () => { state.choice = i; });
      const label = el("label", "option", null, radio, el("span", "", null,
        el("strong", "", c.label + (i === m.recommended ? " (suggested)" : "")),
        el("p", "", c.why),
        el("p", "", "Models: " + c.models.join(", ") + ". Download: " + c.download + "."),
      ));
      set.append(label);
    });
    body.append(set);
  },

  meru(body) {
    body.append(el("p", "", "meru and merud go in ~/.local/bin, and Meru.app in " + state.view.apps + "."));
    body.append(el("label", "option", null,
      checkbox(state.meru.clearQuarantine, false, (v) => { state.meru.clearQuarantine = v; }),
      el("span", "", null,
        el("strong", "", "Clear the quarantine mark"),
        el("p", "", "macOS marks files that came from the internet and checks them the first time they open. Meru isn't signed by Apple, so macOS would refuse Meru.app and stop merud from starting. Clearing the mark on what this step installs lets both run. Leave it and you right-click Meru.app and choose Open the first time."),
      )));
    body.append(el("label", "option", null,
      checkbox(state.meru.addPath, false, (v) => { state.meru.addPath = v; }),
      el("span", "", null,
        el("strong", "", "Add ~/.local/bin to PATH in ~/.zshrc"),
        el("p", "", "So you can type meru in Terminal. The step adds one line, and only when ~/.zshrc doesn't have it."),
      )));
  },

  ollama(body, screen) {
    if (screen.ollamaAt) {
      body.append(el("p", "", "Ollama.app is in " + screen.ollamaAt + ". The step starts it if it isn't running."));
    } else if (screen.brew) {
      body.append(el("p", "", "If Ollama isn't installed, the step installs it with Homebrew: brew install ollama."));
    } else {
      body.append(el("p", "", "If Ollama isn't installed, the step downloads Ollama.app from Ollama's own site, puts it in your Applications folder and checks its signature. The download comes from:"));
      body.append(el("code", "", screen.download));
    }
    const list = el("ul", "summary");
    for (const m of screen.models || []) {
      list.append(el("li", "", null, el("span", "mark " + (m.have ? "done" : "pending"), m.have ? "✓" : ""),
        el("span", "", m.name + (m.have ? ": Ollama has it" : ": to download"))));
    }
    body.append(el("p", "", "The models:"), list);
  },

  folders(body, screen) {
    state.folders = screen.folders || [];
    const set = el("div", "options");
    const draw = () => {
      set.replaceChildren();
      for (const f of state.folders) {
        const count = f.files + (f.more ? "+" : "") + " files";
        set.append(el("label", "option", null,
          checkbox(f.chosen, false, (v) => { f.chosen = v; }),
          el("span", "", null, el("strong", "", f.path), el("p", "", count))));
      }
      if (!state.folders.length) set.append(el("p", "dim", "No folders yet. Add one below."));
    };
    draw();
    body.append(set);
    body.append(el("div", "", null, button("Add a folder…", "secondary", async () => {
      try {
        const f = await call("PickFolder");
        if (f && f.path && !state.folders.some((x) => x.path === f.path)) state.folders.push(f);
        draw();
      } catch (err) {
        body.append(el("p", "error", errorText(err)));
      }
    })));
    body.append(el("p", "dim", screen.skipNote));
    body.append(el("p", "dim", "macOS may ask whether the installer, and later merud, may read Documents or Desktop. Say OK, or Meru can't read them."));
  },

  web(body, screen) {
    body.append(el("p", "", "SearXNG is a search engine that runs on your own computer. It holds no index of its own: it passes your search words to engines such as DuckDuckGo and Wikipedia, drops what identifies you, and merges the results. It needs no account and no key."));
    body.append(el("p", "dim", "What leaves this Mac: the search words, to those engines. Your question, your files and the answer stay here."));
    if (!screen.docker) {
      body.append(el("p", "warn", "SearXNG runs in Docker, and this Mac has no Docker. Install Docker Desktop, OrbStack or colima, open it, then press Continue. Or skip web search for now and run this installer again later."));
      body.append(el("div", "", null, linkButton("Get Docker Desktop", "docker")));
      return;
    }
    body.append(el("p", "", "The step turns web search on and points Meru at 127.0.0.1:8888. When Meru starts, in the last step, merud downloads SearXNG at the version pinned in this release and runs it in Docker as the container meru-searxng, where no other computer can reach it. merud checks it every minute and starts it again when it stops. A SearXNG that already answers there stays as it is: Meru uses it and never touches it."));
  },

  obsidian(body, screen) {
    const o = state.obsidian;
    if (screen.byHand) {
      body.append(el("p", "", "config.toml has an obsidian entry you set up yourself, and it keeps working as it is. Meru can run it for you instead: merud installs its own copy of the Obsidian server, keeps your vault and the tools your entry allows, and comments your entry out, so meru mcp unadopt obsidian puts it back."));
      body.append(choice("obsidian", o.adopt, (v) => { o.adopt = v; },
        ["Let Meru run it (Adopt)", "merud moves the entry over when Meru starts, in the last step."],
        ["Keep my own setup", "Nothing changes."]));
      return;
    }
    if (!o.vault && screen.vault) o.vault = screen.vault;
    body.append(el("p", "", "Pick the folder that holds your Obsidian notes, your vault. When Meru starts, in the last step, merud installs the Obsidian server at the version pinned in this release, with its own copy of Node, and reads your notes from that folder. Obsidian itself needn't run."));
    const shown = el("p", "", o.vault ? "Vault: " + o.vault : "No vault picked yet.");
    body.append(shown);
    body.append(el("div", "", null, button("Choose the vault folder…", "secondary", async () => {
      try {
        const f = await call("PickFolder");
        if (f && f.path) {
          o.vault = f.path;
          shown.textContent = "Vault: " + f.path;
        }
      } catch (err) {
        body.append(el("p", "error", errorText(err)));
      }
    })));
    body.append(el("p", "dim", "Meru reads, lists and searches your notes. Writing to a note asks you first."));
  },

  skills(body, screen) {
    body.append(el("p", "", "The built-in skills, all on after this step:"));
    const skills = el("ul", "summary");
    for (const s of screen.skills || []) {
      skills.append(el("li", "", null, el("span", "mark " + (s.on ? "done" : "pending"), s.on ? "✓" : ""), el("span", "", s.name)));
    }
    body.append(skills);
    body.append(el("p", "", "Sample commands. Each runs one program with no shell and only reads. Tick the ones Meru may run; the ticked ones go into config.toml."));
    state.commands = screen.commands || [];
    const set = el("div", "options");
    for (const c of state.commands) {
      const off = !!c.missing;
      set.append(el("label", "option" + (off ? " off" : ""), null,
        checkbox(c.chosen, off || c.on, (v) => { c.chosen = v; }),
        el("span", "", null,
          el("strong", "", c.name + (c.on ? " (on already)" : "")),
          el("p", "", c.description + (off ? ". Can't turn on: it " + c.missing + "." : "")),
          el("code", "", c.argv.join(" ")))));
    }
    body.append(set);
  },

  google(body, screen) {
    const g = state.google;
    if (state.googlePage === 0) {
      body.append(el("p", "", "Meru reaches Gmail, Calendar, Drive and Docs through workspace-mcp, a small server that runs on this Mac with a Google sign-in you make yourself. Your password never reaches Meru, and the keys stay on this Mac."));
      body.append(el("p", "", "You need a Google account and about 20 minutes. The next page walks through Google's console; the page after that takes the client ID and secret it gives you."));
      body.append(el("div", "", null, linkButton("Open the full guide", "google-guide")));
      return;
    }
    if (state.googlePage === 1) {
      const steps = [
        ["Make a project called meru.", "google-project", "Open"],
        ["Turn on the Gmail API.", "gmail-api", "Open"],
        ["Turn on the Google Calendar API.", "calendar-api", "Open"],
        ["Turn on the Google Drive API.", "drive-api", "Open"],
        ["Turn on the Google Docs API.", "docs-api", "Open"],
        ["Set up the sign-in screen: Get started, app name Meru, your email, audience External, then Create.", "google-auth", "Open"],
        ["Add yourself as a test user under Audience, Test users.", "google-audience", "Open"],
        ["Under Clients, create a client of type Desktop app named Meru desktop. Copy its client ID and client secret.", "google-clients", "Open"],
      ];
      const list = el("ol", "options");
      for (const [text, link, label] of steps) {
        list.append(el("li", "option", null, el("span", "", text), el("span", "spacer"), linkButton(label, link)));
      }
      body.append(list);
      body.append(el("p", "dim", "Check that the project picker at the top of each page says meru. Google doesn't charge for this."));
      return;
    }
    if (screen.byHand) {
      body.append(el("p", "", "config.toml has a google entry you set up yourself, and it keeps working as it is. Meru can run the server for you instead: merud reads your address, client ID and secret from ~/.config/workspace-mcp/start.sh, stops the launchd job that runs it, and runs its own copy on the same port, so your sign-in still works."));
      body.append(choice("google", g.adopt, (v) => { g.adopt = v; },
        ["Let Meru run it (Adopt)", "merud moves the entry over when Meru starts, in the last step. If you have no start.sh, fill in the three fields below."],
        ["Keep my own setup", "Nothing changes."]));
    } else {
      g.adopt = false;
    }
    body.append(field("Your Google address", g.email, "The account whose mail Meru reads.", (v) => { g.email = v; }));
    body.append(field("Client ID", g.clientID, "It ends in .apps.googleusercontent.com.", (v) => { g.clientID = v; }));
    body.append(field("Client secret", "", "It starts with GOCSPX-. It goes to merud, which keeps it in ~/.meru/secrets.toml where only you can read it, and it never shows here again.", (v) => { g.secret = v; }, "password"));
    body.append(el("p", "dim", "When Meru starts, in the last step, merud installs the Google server at the version pinned in this release, with its own uv and Python, and starts it on 127.0.0.1:8000. Then it gives you a Google sign-in link; the last screen has a button that opens it. Sign in there once."));
  },

  profile(body, screen) {
    if (!state.profile) state.profile = Object.assign({ name: "", email: "", answers: "" }, screen.profile || {});
    if (!state.profile.email && state.google.email) state.profile.email = state.google.email;
    const p = state.profile;
    body.append(el("p", "", "Meru reads your files and mail, and they name many people. Knowing your name tells it who \"I\" and \"my\" mean, so it never mixes you up with someone in your files."));
    body.append(field("Your name (needed)", p.name, "", (v) => { p.name = v; }));
    body.append(field(screen.emailRequired ? "Your email address (needed for the Google tools)" : "Your email address (optional)", p.email, "The Google tools use it on every call.", (v) => { p.email = v; }));
    body.append(field("How you like answers (optional)", p.answers, "For example: short, with bullet points.", (v) => { p.answers = v; }));
    body.append(el("p", "dim", "The answers stay on this Mac, as files you can edit in ~/.meru/memory/. Change them later in Meru.app's Settings, About you, or with /me in meru chat."));
  },

  start(body) {
    body.append(el("p", "", "The step writes ~/Library/LaunchAgents/com.meru.merud.plist, loads it with launchctl, and waits for merud to answer. Then it hands merud the connectors you turned on, web search, Obsidian and Google, and shows each line merud reports while it downloads, installs and checks them. merud then scans your folders by itself; this screen follows the scan for two minutes and leaves the rest to merud."));
  },
};

function fact(label, value) {
  return el("div", "fact", null, el("h2", "", label), el("p", "", value));
}

// ---- The summary ----

function drawSummary() {
  $("step-count").textContent = "";
  $("title").textContent = "Meru is set up";
  const body = $("body");
  const list = el("ul", "summary");
  for (const s of steps()) {
    const word = { done: "Done", skipped: "Skipped", failed: "Failed", pending: "Not run", running: "Running" }[s.status];
    list.append(el("li", "", null, el("span", "mark " + s.status, MARKS[s.status]),
      el("span", "", null, el("strong", "", s.title + ": " + word), s.detail ? el("p", "", s.detail) : null)));
  }
  body.replaceChildren(
    list,
    el("div", "card", null,
      el("strong", "", "Your settings: " + state.view.config),
      el("p", "", "Every setting sits in this one file, with a comment on each. Meru.app's Settings and /help in meru chat change most of them for you. After you edit the file by hand, restart merud: launchctl kickstart -k gui/$(id -u)/com.meru.merud"),
    ),
    el("p", "dim", "Run this installer again at any time to finish a step you skipped."),
  );
  if (state.view.signIn) {
    body.append(el("div", "card", null,
      el("strong", "", "Sign in to Google"),
      el("p", "", "The Google server waits for you to sign in once. The button opens Google's sign-in page in your browser. Google shows \"Google hasn't verified this app\": it is your own project, so click Advanced, then Go to Meru."),
      button("Sign in to Google", "primary", () => call("OpenSignIn").catch(() => {})),
    ));
  }
  const foot = $("foot");
  foot.replaceChildren(
    button("Open config.toml", "secondary", () => call("OpenConfig").catch(() => {})),
    el("span", "spacer"),
    button("Quit", "secondary", () => call("Quit")),
    button("Open Meru", "primary", () => call("OpenMeru").catch(() => {})),
  );
}

// ---- Progress ----

Events.On("installer:progress", (event) => {
  const p = event.data;
  const s = current();
  if (!s || p.step !== s.id) return;
  $("progress").hidden = false;
  const log = $("log");
  log.textContent += p.line + "\n";
  log.scrollTop = log.scrollHeight;
  if (p.fraction >= 0) {
    $("bar").hidden = false;
    $("bar-fill").style.width = Math.round(p.fraction * 100) + "%";
  }
});

// ---- Start ----

async function start() {
  try {
    state.view = await call("Detect");
  } catch (err) {
    state.view = await call("State");
  }
  show(-1);
}

start();
