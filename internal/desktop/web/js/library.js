// The Library: the settings screen, with a back link and eight sections.
// Connections, Folders, About you, Skills, Models, Activity and Usage each
// ask the Bridge for their data when they open, and every change goes to
// merud, which writes config.toml or the memory folder and reloads. About,
// the last, says what Meru is and links to the project; the Bridge hands
// it the links, so this file names no web address. The page keeps nothing
// it can't fetch again.

import { bridge, copyText, errorText } from "./api.js";
import { icon } from "./icons.js";
import { el, button, seconds } from "./turns.js";

// SECTIONS are the Library's sections, in the order its tabs show them.
const SECTIONS = [
  { id: "connections", label: "Connections" },
  { id: "folders", label: "Folders" },
  { id: "you", label: "About you" },
  { id: "skills", label: "Skills" },
  { id: "models", label: "Models" },
  { id: "activity", label: "Activity" },
  { id: "usage", label: "Usage" },
  { id: "about", label: "About" },
];

// TOOLS_SHOWN is how many tools a connection card lists before "Show all".
const TOOLS_SHOWN = 6;

// WEB_TOOLS are the built-in tools the Web search card holds; the other
// built-ins go on the "Built into Meru" card.
const WEB_TOOLS = ["web_search", "web_fetch"];

// lib is the open Library: its root element, the section on show, the
// connection to bring into view, and what it calls back into app.js.
const lib = { root: null, section: "connections", focus: "", pages: null, expanded: new Set() };

// openLibrary draws the Library in root at section. focus names a
// connection to scroll to and mark, as "Change what google may do" asks.
export function openLibrary(root, section, focus, pages) {
  lib.root = root;
  lib.section = SECTIONS.some((s) => s.id === section) ? section : "connections";
  lib.focus = focus || "";
  lib.pages = pages;
  if (lib.focus) lib.expanded.add(lib.focus);
  draw();
}

// policyWords says a policy the way the Library's switches do.
export function policyWords(policy) {
  switch (policy) {
    case "off":
      return "Off";
    case "ask":
      return "Ask";
    case "allow":
      return "Allow";
    case "always":
      return "Always asks";
  }
  return policy;
}

// draw draws the header, the tabs and the open section.
function draw() {
  const root = lib.root;
  root.replaceChildren();
  const head = el("header", "page-head");
  const back = button("Back to chat", { className: "text-button back", iconName: "back", onClick: lib.pages.back });
  const title = el("h1", "", "Library");
  title.id = "library-title";
  head.append(back, title);
  root.append(head);

  const tabs = el("div", "tabs");
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "Library sections");
  SECTIONS.forEach((s, i) => {
    const on = s.id === lib.section;
    const t = button(s.label, { className: "tab" });
    t.id = "tab-" + s.id;
    t.setAttribute("role", "tab");
    t.setAttribute("aria-selected", String(on));
    t.setAttribute("aria-controls", "library-section");
    t.tabIndex = on ? 0 : -1;
    t.addEventListener("click", () => {
      lib.section = s.id;
      lib.focus = "";
      draw();
    });
    t.addEventListener("keydown", (e) => {
      const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (!step) return;
      e.preventDefault();
      lib.section = SECTIONS[(i + step + SECTIONS.length) % SECTIONS.length].id;
      draw();
      document.getElementById("tab-" + lib.section).focus();
    });
    tabs.append(t);
  });
  root.append(tabs);

  const body = el("section", "page-body");
  body.id = "library-section";
  body.setAttribute("role", "tabpanel");
  body.setAttribute("aria-labelledby", "tab-" + lib.section);
  root.append(body);
  body.append(el("p", "panel-note", "Loading…"));
  const drawers = { connections, folders, you, skills, models, activity, usage, about };
  drawers[lib.section](body);
}

// fail shows err in body, with a hint when merud isn't running.
function fail(body, err) {
  body.replaceChildren(el("p", "error", errorText(err)));
  body.append(el("p", "panel-note", "The Library needs merud. Start it in a terminal with: merud"));
}

// intro adds a short paragraph that says what a section is for.
function intro(body, text) {
  body.append(el("p", "section-intro", text));
}

// ---- Connections ----

// connections draws a card per tool source, then the catalog.
function connections(body) {
  bridge.connections().then(
    (cv) => drawConnections(body, cv),
    (err) => fail(body, err),
  );
}

// drawConnections draws the Connections section from cv.
function drawConnections(body, cv) {
  body.replaceChildren();
  intro(body, "What Meru may reach, tool by tool. Off: the model never sees the tool. " +
    "Ask: Meru asks you before each call. Allow: it runs without asking. A new tool starts Off.");
  const grid = el("div", "cards");
  for (const c of cv.connections) {
    if (c.kind === "builtin") {
      const web = { ...c, tools: c.tools.filter((t) => WEB_TOOLS.includes(t.name)) };
      const own = { ...c, tools: c.tools.filter((t) => !WEB_TOOLS.includes(t.name)), note: "" };
      grid.append(connectionCard(body, web, "Web search", "web"), connectionCard(body, own, "Built into Meru", "meru"));
      continue;
    }
    grid.append(connectionCard(body, c, c.kind === "command" ? "Local commands" : c.name, c.name));
  }
  body.append(grid);

  const add = cv.catalog.filter((e) => !e.added);
  body.append(el("h2", "section-head", "Add a connection"));
  if (add.length === 0) body.append(el("p", "panel-note", "You have added every server in Meru's catalog."));
  for (const e of add) body.append(catalogCard(e, () => connections(body)));
  body.append(customServer(body));

  const focused = lib.focus && body.querySelector('[data-connection="' + CSS.escape(lib.focus) + '"]');
  if (focused) {
    focused.classList.add("focused");
    focused.scrollIntoView({ block: "center" });
    const first = focused.querySelector("[role=radio][aria-checked=true]");
    if (first) first.focus();
  }
}

// connectionCard draws one source: its state, how many tools are on, and
// a switch per tool. key names the card for "Change what … may do".
function connectionCard(body, c, title, key) {
  const card = el("article", "card connection");
  card.dataset.connection = key;
  const head = el("div", "card-head");
  head.append(el("h3", "", title));
  const up = c.state === "connected";
  const pill = el("span", "pill " + (up ? "ok" : "down"), up ? "Connected" : "Not connected");
  head.append(pill);
  card.append(head);

  const where = [c.kind === "mcp" ? "MCP server" : c.kind === "a2a" ? "Agent" : c.kind === "command" ? "Programs on this Mac" : "Inside merud"];
  if (c.transport) where.push(c.transport === "http" ? "connects to " + c.url : "merud starts it");
  if (c.remote) where.push("on another machine");
  card.append(el("p", "card-sub", where.join(" · ")));
  if (!up && c.err) card.append(el("p", "card-error", c.err));

  const on = c.tools.filter((t) => t.policy !== "off").length;
  const total = c.offered >= 0 && c.kind !== "builtin" ? Math.max(c.offered, c.tools.length) : c.tools.length;
  card.append(el("p", "card-count", on + " of " + total + " tools on"));
  if (c.note) card.append(el("p", "card-note", c.note));

  const list = el("ul", "tool-list");
  const all = lib.expanded.has(key);
  const shown = all ? c.tools : c.tools.slice(0, TOOLS_SHOWN);
  for (const t of shown) list.append(toolRow(body, c, t));
  card.append(list);
  if (c.tools.length > TOOLS_SHOWN) {
    card.append(button(all ? "Show fewer" : "Show all " + c.tools.length + " tools", {
      className: "text-button",
      onClick: () => {
        if (all) lib.expanded.delete(key);
        else lib.expanded.add(key);
        connections(body);
      },
    }));
  }
  if (c.kind === "mcp") card.append(removeButton(body, c.name));
  return card;
}

// toolRow draws one tool with its Off / Ask / Allow switch. A tool that
// always asks offers Off and Always asks, and says why it can't be Allow;
// a local command shows its policy without a switch.
function toolRow(body, c, t) {
  const li = el("li", "tool-row");
  const text = el("div", "tool-text");
  text.append(el("span", "tool-name", t.name));
  if (t.description) text.append(el("span", "tool-desc", firstLine(t.description)));
  if (t.missing) text.append(el("span", "card-error", "The server doesn't offer this tool now."));
  li.append(text);
  if (c.fixed) {
    li.append(el("span", "policy-badge " + t.policy, policyWords(t.policy)));
    return li;
  }
  const always = t.policy === "always" || (t.name === "configure" && c.kind === "builtin");
  const choices = always ? ["off", "always"] : ["off", "ask", "allow"];
  const group = el("div", "segmented");
  group.setAttribute("role", "radiogroup");
  group.setAttribute("aria-label", t.name);
  for (const p of choices) {
    const on = t.policy === p || (always && p === "always" && t.policy !== "off");
    const b = button(policyWords(p), { className: "segment" + (on ? " on" : "") });
    b.setAttribute("role", "radio");
    b.setAttribute("aria-checked", String(on));
    b.tabIndex = on ? 0 : -1;
    b.addEventListener("click", () => {
      if (on) return;
      // Turning an always-asks tool back on makes it ask; merud writes it
      // to confirm, and configure asks every time whatever config says.
      const policy = p === "always" ? "ask" : p;
      bridge.setPolicy(c.kind, c.kind === "builtin" ? "meru" : c.name, t.name, policy).then(
        (cv) => {
          lib.focus = "";
          drawConnections(body, cv);
          lib.pages.notice(t.name + " is now " + policyWords(policy).toLowerCase() + ".");
        },
        (err) => lib.pages.notice(errorText(err)),
      );
    });
    group.append(b);
  }
  li.append(group);
  if (always) {
    li.append(el("span", "tool-why", t.name === "configure"
      ? "Always asks: it changes Meru's own settings, so the model can never grant itself a tool."
      : "Always asks: config lists it in always_confirm, because it runs commands."));
  }
  return li;
}

// removeButton asks before it takes an MCP server out of config.toml.
function removeButton(body, name) {
  const box = el("div", "card-actions");
  const ask = button("Remove", {
    className: "text-button",
    onClick: () => {
      box.replaceChildren(el("span", "", "Remove " + name + " from config.toml?"),
        button("Remove", {
          className: "button secondary",
          onClick: () => bridge.removeConnection(name).then(
            (cv) => {
              drawConnections(body, cv);
              lib.pages.notice("Removed " + name + ".");
            },
            (err) => lib.pages.notice(errorText(err)),
          ),
        }),
        button("Keep it", { className: "text-button", onClick: () => box.replaceChildren(ask) }));
    },
  });
  box.append(ask);
  return box;
}

// catalogCard draws a server from Meru's catalog with what it needs and
// the tools it turns on. done runs after the server is added. Setup uses
// it too.
export function catalogCard(e, done) {
  // A form, so the key field sits in one, as a password field should, and
  // Enter in it adds the server.
  const card = el("form", "card catalog");
  card.setAttribute("aria-label", e.title);
  card.append(el("h3", "", e.title));
  card.append(el("p", "", e.description));
  if (e.requires) card.append(el("p", "card-sub", "Needs " + e.requires + "."));

  let keyInput = null;
  let secret = "";
  for (const n of e.needs || []) {
    if (n.kind === "api_key") {
      secret = n.secret;
      const id = "key-" + e.name;
      const label = el("label", "field-label", n.prompt);
      label.htmlFor = id;
      keyInput = el("input", "text-input");
      keyInput.id = id;
      keyInput.type = "password";
      keyInput.autocomplete = "off";
      keyInput.placeholder = n.saved ? "Saved already; paste a new one to replace it" : "Paste the key";
      card.append(label, keyInput);
      if (n.help) card.append(el("p", "card-note", n.help));
      keyInput.dataset.saved = String(!!n.saved);
    } else if (n.help || n.prompt) {
      card.append(el("p", "card-note", n.prompt + (n.help ? " " + n.help : "")));
    }
  }
  if (e.start) {
    card.append(el("p", "card-sub", "You start this server; Meru only connects to it:"));
    card.append(copyBlock(e.start));
  } else if (e.command) {
    card.append(el("p", "card-sub", "merud starts it with: " + e.command));
  }
  const confirm = new Set(e.confirm || []);
  const ask = e.allow.filter((t) => confirm.has(t));
  const allow = e.allow.filter((t) => !confirm.has(t));
  card.append(el("p", "card-note", "Turns on " + allow.join(", ") + (ask.length ? "; asks first for " + ask.join(", ") : "") +
    ". Every other tool it offers stays Off."));

  const add = button("Add " + e.title, { className: "button primary" });
  add.type = "submit";
  card.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const key = keyInput ? keyInput.value : "";
    if (keyInput && !key.trim() && keyInput.dataset.saved !== "true") {
      lib.pages.notice(e.title + " needs its key first.");
      keyInput.focus();
      return;
    }
    add.disabled = true;
    bridge.addConnection(e.name, secret, key).then(
      () => {
        if (keyInput) keyInput.value = "";
        lib.pages.notice("Added " + e.title + ".");
        done();
      },
      (err) => {
        add.disabled = false;
        lib.pages.notice(errorText(err));
      },
    );
  });
  card.append(add);
  return card;
}

// SERVER_NAME is the rule merud and `meru mcp add` apply to a server's
// name: 1 to 64 letters, digits, - and _. The page checks it first only to
// say so sooner; merud checks it again.
const SERVER_NAME = /^[A-Za-z0-9_-]{1,64}$/;

// customServer draws the "Add your own MCP server" link and the form it
// opens: a name, then either a program merud starts, with its arguments
// and environment variables, or the URL of a server the user runs. Each
// argument has a field of its own, so an argument with a space in it
// needs no quotes and nothing splits it. merud checks everything and
// writes the server with every tool Off; the card then lists its tools.
function customServer(body) {
  const box = el("div", "custom-server");
  const form = el("form", "card custom-form");
  form.id = "custom-server-form";
  form.hidden = true;
  form.setAttribute("aria-label", "Your own MCP server");
  const open = button("Add your own MCP server", { className: "text-button link", iconName: "plus" });
  open.setAttribute("aria-expanded", "false");
  open.setAttribute("aria-controls", form.id);
  open.addEventListener("click", () => {
    form.hidden = !form.hidden;
    open.setAttribute("aria-expanded", String(!form.hidden));
    if (!form.hidden) name.focus();
  });
  box.append(open, form);

  form.append(el("h3", "", "Your own MCP server"));
  form.append(el("p", "card-note", "For a server outside the catalog. Meru adds it with every tool Off; " +
    "once it connects, its card lists the tools and you turn on the ones you want."));

  const name = field(form, "custom-name", "Name", "notes");
  name.autocomplete = "off";
  form.append(el("p", "card-note", "Letters, digits, - and _. Meru puts it before each tool's name, as in notes.search."));

  // The two kinds of server, as radio buttons: a program merud starts
  // (stdio) or a server at a URL (Streamable HTTP).
  const kinds = el("fieldset", "choice-row");
  kinds.append(el("legend", "field-label", "How Meru reaches it"));
  const stdio = radio(kinds, "custom-kind", "stdio", "A program merud starts", true);
  radio(kinds, "custom-kind", "http", "A server at a URL", false);
  form.append(kinds);

  // The program, its arguments and its environment.
  const prog = el("div", "custom-part");
  const command = field(prog, "custom-command", "Program", "npx");
  command.spellcheck = false;
  prog.append(el("p", "field-label", "Arguments"));
  const args = el("div", "rows");
  prog.append(args, el("p", "card-note", "One argument in each field, as the program should get it. " +
    "A space stays inside its field, so nothing needs quotes."));
  prog.append(button("Add an argument", { className: "text-button", iconName: "plus", onClick: () => argRow(args).focus() }));
  argRow(args);
  prog.append(el("p", "field-label", "Environment variables"));
  const env = el("div", "rows");
  prog.append(env, el("p", "card-note", "Tick Secret for a key or a token: merud saves it in secrets.toml, " +
    "config.toml holds only a reference to it, and Meru never shows it again."));
  prog.append(button("Add a variable", { className: "text-button", iconName: "plus", onClick: () => envRow(env).focus() }));
  form.append(prog);

  // The URL, and the tick for a server on another computer.
  const web = el("div", "custom-part");
  web.hidden = true;
  const url = field(web, "custom-url", "URL", "The address the server prints when it starts");
  url.type = "url";
  url.spellcheck = false;
  const remoteLabel = el("label", "check-row");
  const remote = el("input");
  remote.type = "checkbox";
  remoteLabel.append(remote, document.createTextNode(" This server is on another computer"));
  const warning = el("p", "card-error", "Each tool call sends its data to that computer. Tick this only for a server you trust.");
  warning.hidden = true;
  remote.addEventListener("change", () => (warning.hidden = !remote.checked));
  web.append(remoteLabel, warning);
  web.append(el("p", "card-note", "Without the tick, Meru accepts only an address on this computer."));
  form.append(web);

  kinds.addEventListener("change", () => {
    prog.hidden = !stdio.checked;
    web.hidden = stdio.checked;
  });

  const add = button("Add server", { className: "button primary" });
  add.type = "submit";
  const cancel = button("Cancel", {
    className: "text-button",
    onClick: () => {
      form.hidden = true;
      open.setAttribute("aria-expanded", "false");
      open.focus();
    },
  });
  const actions = el("div", "card-actions");
  actions.append(add, cancel);
  form.append(actions);

  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const n = name.value.trim();
    if (!SERVER_NAME.test(n)) {
      lib.pages.notice("Give the server a name of letters, digits, - and _.");
      name.focus();
      return;
    }
    const server = { name: n, command: "", args: [], url: "", remote: false, env: [] };
    if (stdio.checked) {
      server.command = command.value.trim();
      // A blank argument field is one the user added and left empty.
      server.args = [...args.querySelectorAll("input")].map((i) => i.value).filter((v) => v !== "");
      for (const row of env.children) {
        const [key, value, secret] = row.querySelectorAll("input");
        if (!key.value.trim() && !value.value) continue;
        server.env.push({ name: key.value.trim(), value: value.value, secret: secret.checked });
      }
    } else {
      server.url = url.value.trim();
      server.remote = remote.checked;
    }
    add.disabled = true;
    bridge.addCustomServer(server).then(
      () => {
        lib.focus = n;
        lib.expanded.add(n);
        lib.pages.notice("Added " + n + ". Its tools start Off; turn on the ones you want.");
        connections(body);
      },
      (err) => {
        add.disabled = false;
        lib.pages.notice(errorText(err));
      },
    );
  });
  return box;
}

// field adds a labelled text field to parent and returns the input.
function field(parent, id, label, placeholder) {
  const l = el("label", "field-label", label);
  l.htmlFor = id;
  const input = el("input", "text-input");
  input.id = id;
  input.placeholder = placeholder;
  parent.append(l, input);
  return input;
}

// radio adds a radio button with its label to parent and returns it.
function radio(parent, group, value, label, checked) {
  const l = el("label", "check-row");
  const r = el("input");
  r.type = "radio";
  r.name = group;
  r.value = value;
  r.checked = checked;
  l.append(r, document.createTextNode(" " + label));
  parent.append(l);
  return r;
}

// argRow adds one argument field, with a button that takes it away, to
// rows and returns the field.
function argRow(rows) {
  const row = el("div", "field-row");
  const input = el("input", "text-input grow");
  input.spellcheck = false;
  input.setAttribute("aria-label", "Argument " + (rows.children.length + 1));
  row.append(input, button("", { className: "icon-button", iconName: "close", ariaLabel: "Remove this argument", onClick: () => row.remove() }));
  rows.append(row);
  return input;
}

// envRow adds one environment variable, a name, a value and a Secret
// tick, to rows and returns the name field. A ticked value hides as a
// password field does.
function envRow(rows) {
  const row = el("div", "field-row");
  const key = el("input", "text-input");
  key.placeholder = "NAME";
  key.spellcheck = false;
  key.setAttribute("aria-label", "Variable name");
  const value = el("input", "text-input grow");
  value.placeholder = "value";
  value.autocomplete = "off";
  value.setAttribute("aria-label", "Value");
  const tick = el("label", "check-row");
  const secret = el("input");
  secret.type = "checkbox";
  secret.addEventListener("change", () => (value.type = secret.checked ? "password" : "text"));
  tick.append(secret, document.createTextNode(" Secret"));
  row.append(key, value, tick,
    button("", { className: "icon-button", iconName: "close", ariaLabel: "Remove this variable", onClick: () => row.remove() }));
  rows.append(row);
  return key;
}

// copyBlock shows a command with a Copy button.
function copyBlock(text) {
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

// firstLine returns the first line of a tool's description.
function firstLine(text) {
  const line = String(text).split("\n").find((l) => l.trim()) || "";
  return line.length > 160 ? line.slice(0, 159) + "…" : line.trim();
}

// ---- Folders ----

// SKIP_RULES says what the indexer leaves out, as ARCHITECTURE.md does.
const SKIP_RULES = "Meru skips hidden files and folders, secrets such as .env files and keys, build folders such as " +
  "node_modules, anything a .gitignore or .meruignore lists, and files over the size cap. It never follows a symlink.";

// folders draws the [index] folders and the suggested ones.
function folders(body) {
  bridge.folders().then(
    (fv) => drawFolders(body, fv),
    (err) => fail(body, err),
  );
}

// drawFolders draws the Folders section from fv.
function drawFolders(body, fv) {
  body.replaceChildren();
  intro(body, "The folders Meru reads into its search index. Only these, and nothing by default.");
  const ul = el("ul", "row-list");
  if (fv.folders.length === 0) body.append(el("p", "panel-note", "No folders yet."));
  for (const f of fv.folders) {
    const li = el("li", "row");
    li.append(icon("folder", 16), el("span", "row-main", f.path));
    li.append(el("span", "dim", f.exists ? count(f.files, false) + " in the index" : "not on this Mac now"));
    li.append(button("Remove", {
      className: "text-button",
      onClick: () => bridge.removeFolder(f.path).then(
        (next) => {
          drawFolders(body, next);
          lib.pages.notice("Removed " + f.path + ". Its files leave the index in a moment.");
        },
        (err) => lib.pages.notice(errorText(err)),
      ),
    }));
    ul.append(li);
  }
  body.append(ul);
  const add = button("Add a folder…", {
    className: "button secondary",
    iconName: "plus",
    onClick: () => chooseAndAdd((next) => drawFolders(body, next)),
  });
  body.append(add);
  if (fv.suggested.length) {
    body.append(el("h2", "section-head", "Suggested"));
    const sl = el("ul", "row-list");
    for (const f of fv.suggested) {
      const li = el("li", "row");
      li.append(icon("folder", 16), el("span", "row-main", f.path), el("span", "dim", count(f.files, f.more)));
      li.append(button("Add", {
        className: "text-button",
        onClick: () => addFolder(f.path, (next) => drawFolders(body, next)),
      }));
      sl.append(li);
    }
    body.append(sl);
  }
  body.append(el("p", "card-note", SKIP_RULES));
}

// chooseAndAdd shows the folder dialog and adds the folder picked.
function chooseAndAdd(done) {
  bridge.chooseFolder().then(
    (path) => {
      if (path) addFolder(path, done);
    },
    (err) => lib.pages && lib.pages.notice(errorText(err)),
  );
}

// addFolder adds path through merud, which starts indexing it.
function addFolder(path, done) {
  bridge.addFolder(path).then(
    (next) => {
      done(next);
      lib.pages.notice("Added " + path + ". Meru is indexing it now.");
    },
    (err) => lib.pages.notice(errorText(err)),
  );
}

// count writes a file count: "1 file", "1,234 files", "2,000+ files".
export function count(n, more) {
  return n.toLocaleString() + (more ? "+" : "") + (n === 1 && !more ? " file" : " files");
}

// ---- About you ----

// KINDS are the memory kinds that go into every prompt, with the words
// the section uses for them.
const KINDS = [
  { kind: "me", label: "About you" },
  { kind: "preferences", label: "How you like things done" },
];

// you draws what Meru knows about the user.
function you(body) {
  bridge.memories().then(
    (mems) => drawYou(body, mems),
    (err) => fail(body, err),
  );
}

// drawYou draws the profile memories, each with Edit and Forget, and a
// form to add one.
function drawYou(body, mems) {
  body.replaceChildren();
  intro(body, "What Meru knows about you. All of it goes into every answer, and stays in ~/.meru/memory on this Mac.");
  for (const k of KINDS) {
    body.append(el("h2", "section-head", k.label));
    const ul = el("ul", "row-list");
    const rows = mems.filter((m) => m.kind === k.kind);
    if (rows.length === 0) body.append(el("p", "panel-note", "Nothing yet."));
    for (const m of rows) ul.append(memoryRow(body, m));
    body.append(ul);
  }
  const form = el("form", "add-form");
  const kind = el("select", "text-input");
  kind.setAttribute("aria-label", "Kind");
  for (const k of KINDS) {
    const o = el("option", "", k.label);
    o.value = k.kind;
    kind.append(o);
  }
  const text = el("input", "text-input grow");
  text.placeholder = "Name: Dana Reyes";
  text.setAttribute("aria-label", "What Meru should know");
  form.append(kind, text, button("Add", { className: "button primary" }));
  form.lastChild.type = "submit";
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    bridge.addMemory(kind.value, text.value).then(() => you(body), (err) => lib.pages.notice(errorText(err)));
  });
  body.append(form);
}

// memoryRow draws one memory with Edit and Forget. An edit forgets the
// old memory and saves the new text in its place.
function memoryRow(body, m) {
  const li = el("li", "row");
  li.append(el("span", "row-main", m.text));
  li.append(button("Edit", {
    className: "text-button",
    onClick: () => {
      const input = el("input", "text-input grow");
      input.value = m.text;
      input.setAttribute("aria-label", "Edit: " + m.text);
      li.replaceChildren(input, button("Save", {
        className: "button primary",
        onClick: () => bridge.addMemory(m.kind, input.value)
          .then(() => bridge.forgetMemory(m.id))
          .then(() => you(body), (err) => lib.pages.notice(errorText(err))),
      }), button("Cancel", { className: "text-button", onClick: () => you(body) }));
      input.focus();
    },
  }));
  li.append(button("Forget", {
    className: "text-button",
    onClick: () => bridge.forgetMemory(m.id).then(() => you(body), (err) => lib.pages.notice(errorText(err))),
  }));
  return li;
}

// ---- Skills ----

// skills draws each skill with its on and off switch.
function skills(body) {
  bridge.skills().then(
    (sv) => drawSkills(body, sv),
    (err) => fail(body, err),
  );
}

// drawSkills draws the Skills section from sv.
function drawSkills(body, sv) {
  body.replaceChildren();
  intro(body, "Instructions Meru loads when a question needs them. The ones Meru ships are marked; " +
    "your own live in ~/.meru/skills. A skill that is off never loads.");
  const ul = el("ul", "row-list");
  for (const s of sv.skills) {
    const li = el("li", "row");
    const text = el("div", "row-main");
    text.append(el("span", "tool-name", s.name));
    if (s.description) text.append(el("span", "tool-desc", firstLine(s.description)));
    const marks = [s.builtin ? "built in" : "", s.edited ? "edited" : ""].filter(Boolean).join(" · ");
    if (marks) text.append(el("span", "dim", marks));
    li.append(text);
    const sw = button(s.disabled ? "Off" : "On", { className: "switch" + (s.disabled ? "" : " on") });
    sw.setAttribute("role", "switch");
    sw.setAttribute("aria-checked", String(!s.disabled));
    sw.setAttribute("aria-label", s.name);
    sw.addEventListener("click", () => bridge.setSkill(s.name, !!s.disabled).then(
      (next) => drawSkills(body, next),
      (err) => lib.pages.notice(errorText(err)),
    ));
    li.append(sw);
    ul.append(li);
  }
  body.append(ul);
  if (sv.warnings.length) {
    body.append(el("h2", "section-head", "Skipped"));
    for (const w of sv.warnings) body.append(el("p", "card-error", w));
  }
}

// ---- Models ----

// models draws the models config names and what Ollama holds.
function models(body) {
  bridge.models().then(
    (m) => {
      body.replaceChildren();
      intro(body, "The open-weight models Meru runs in Ollama on this Mac. Nothing goes to a model anywhere else.");
      const loaded = new Set(m.loaded || []);
      const dl = el("dl", "facts");
      const fact = (label, value, note) => {
        dl.append(el("dt", "", label));
        const dd = el("dd", "", value);
        if (note) dd.append(el("span", "dim", " · " + note));
        dl.append(dd);
      };
      const state = (name) => (loaded.has(name) ? "loaded" : "not loaded now; Ollama loads it when needed");
      fact("Profile", m.profile);
      fact("Answer model", m.main, state(m.main));
      fact("Fast model (router)", m.fast, state(m.fast));
      fact("Embedding model", m.embed, state(m.embed));
      fact("Ollama", m.runtime_version ? m.runtime_version : "didn't answer", m.err || "");
      body.append(dl);
      body.append(el("h2", "section-head", "To change them"));
      body.append(el("p", "", "Set profile, or the models under [models], in " + m.config_path +
        ", then restart merud. Fetch a model first with ollama pull, for example:"));
      body.append(copyBlock("ollama pull " + m.main));
    },
    (err) => fail(body, err),
  );
}

// ---- Activity ----

// activity draws the tool log: one row per call, newest first, with its
// arguments and result behind a button.
function activity(body) {
  bridge.activity().then(
    (rows) => {
      body.replaceChildren();
      intro(body, "Every tool call, allowed or not, from the tool_calls log. Arguments and results stay folded until you open a row.");
      body.append(button("Refresh", { className: "text-button", iconName: "retry", onClick: () => activity(body) }));
      if (rows.length === 0) {
        body.append(el("p", "panel-note", "No tool calls yet."));
        return;
      }
      const table = el("table", "log");
      const head = el("tr");
      for (const h of ["Time", "Tool", "Outcome", "Took", ""]) head.append(el("th", "", h));
      const thead = el("thead");
      thead.append(head);
      const tbody = el("tbody");
      rows.forEach((r, i) => {
        const tr = el("tr");
        const tool = r.server && r.server !== "meru" ? r.server + "." + r.tool : r.tool;
        tr.append(el("td", "", new Date(r.time).toLocaleString()), el("td", "mono", tool),
          el("td", "outcome " + r.outcome, r.outcome + (r.approval ? " · " + r.approval : "")),
          el("td", "", seconds(r.duration_ms || 0)));
        const detail = el("tr", "log-detail");
        detail.hidden = true;
        detail.id = "log-" + i;
        const cell = el("td");
        cell.colSpan = 5;
        cell.append(el("p", "dim", "Session " + r.session));
        if (r.args) cell.append(el("pre", "args", JSON.stringify(r.args, null, 2)));
        if (r.result) cell.append(el("pre", "args", r.result));
        detail.append(cell);
        const open = button("Details", { className: "text-button" });
        open.setAttribute("aria-expanded", "false");
        open.setAttribute("aria-controls", detail.id);
        open.addEventListener("click", () => {
          detail.hidden = !detail.hidden;
          open.setAttribute("aria-expanded", String(!detail.hidden));
        });
        const td = el("td");
        td.append(open);
        tr.append(td);
        tbody.append(tr, detail);
      });
      table.append(thead, tbody);
      const wrap = el("div", "table-wrap");
      wrap.append(table);
      body.append(wrap);
    },
    (err) => fail(body, err),
  );
}

// ---- Usage ----

// WINDOWS names the usage windows, as merud sends them.
const WINDOWS = { "1h": "Last hour", today: "Today", week: "This week", month: "This month", "30d": "Last 30 days", all: "All time" };

// usage draws how much Meru has been used, window by window, as the
// chat's /usage box does.
function usage(body) {
  bridge.usage().then(
    (windows) => {
      body.replaceChildren();
      intro(body, "Counted from the session transcripts on this Mac: answered questions only.");
      const table = el("table", "log");
      const head = el("tr");
      for (const h of ["", "Questions", "Chats", "Tokens in", "Tokens out", "Time answering", "Files read", "Tool calls"]) {
        head.append(el("th", "", h));
      }
      const thead = el("thead");
      thead.append(head);
      const tbody = el("tbody");
      for (const w of windows || []) {
        const tr = el("tr");
        tr.append(el("th", "", WINDOWS[w.name] || w.name));
        for (const v of [w.turns, w.sessions, w.tokens_in, w.tokens_out]) tr.append(el("td", "num", Number(v).toLocaleString()));
        tr.append(el("td", "num", seconds(w.active_ms || 0)), el("td", "num", String(w.docs)), el("td", "num", String(w.tool_calls)));
        tbody.append(tr);
      }
      table.append(thead, tbody);
      const wrap = el("div", "table-wrap");
      wrap.append(table);
      body.append(wrap);
    },
    (err) => fail(body, err),
  );
}

// ---- About ----

// DOES lists what Meru does, as README.md and ARCHITECTURE.md say it.
const DOES = [
  "Answers your questions with open-weight models that Ollama runs on your computer.",
  "Searches the folders you choose, and names the files each answer came from.",
  "Uses the tools you allow, such as mail, calendar, notes and web search, and asks you before a call that changes anything.",
  "Remembers what you tell it about yourself.",
  "Keeps every chat as a file on your computer.",
];

// WHY lists why Meru runs on your own computer, from README.md's "Why
// your own machine".
const WHY = [
  "Your files, mail and questions stay on your computer, so none of it ends up in anyone's training set.",
  "Meru needs no account, calls no cloud model and sends no usage reports.",
  "You can read every setting in one file, and each memory and skill is a Markdown file you can edit or delete.",
];

// about draws the About section: the tagline, the name, what Meru does and
// why, this copy's version and folders, and the project's links. The
// Bridge supplies the tagline, the version, the paths and the links, so it
// works while merud is down.
function about(body) {
  bridge.about().then(
    (a) => {
      body.replaceChildren();
      body.append(el("p", "about-tagline", a.tagline + "."));

      body.append(el("h2", "section-head", "The name"));
      body.append(el("p", "", "Meru (मेरु) is the cosmic mountain that the sun, moon and stars turn around. " +
        "The assistant takes the name because it works the same way: it stays in one place, on your computer, " +
        "and your notes, tools and daily routine turn around it."));

      body.append(el("h2", "section-head", "What it does"));
      body.append(list(DOES));
      body.append(el("h2", "section-head", "Why it runs on your computer"));
      body.append(list(WHY));

      body.append(el("h2", "section-head", "This copy"));
      const dl = el("dl", "facts");
      dl.append(el("dt", "", "Version"), el("dd", "", a.version));
      dl.append(el("dt", "", "License"), el("dd", "", a.license));
      dl.append(el("dt", "", "Settings"), el("dd", "mono", a.config_path));
      dl.append(el("dt", "", "Meru's folder"), el("dd", "mono", a.data_dir));
      body.append(dl);
      body.append(el("p", "card-note", "Meru's folder holds the settings, your keys in secrets.toml, " +
        "every chat, what Meru remembers and the search index."));

      body.append(el("h2", "section-head", "The project"));
      const links = el("div", "about-links");
      for (const l of a.links) {
        links.append(button(l.label, {
          className: "button secondary",
          iconName: "globe",
          onClick: () => bridge.openURL(l.url).catch((err) => lib.pages.notice(errorText(err))),
        }));
      }
      body.append(links);
      body.append(el("p", "card-note", "To ask for a feature or report a problem, open an issue on GitHub. " +
        "Say what you tried, what you expected and what happened, and leave out anything private."));
    },
    (err) => fail(body, err),
  );
}

// list makes a bulleted list of lines.
function list(lines) {
  const ul = el("ul", "about-list");
  for (const line of lines) ul.append(el("li", "", line));
  return ul;
}

// ---- The side panel: On this Mac ----

// libraryPanel fills the side panel while the Library shows: the models,
// the search index, and the line that says nothing leaves this Mac.
export function libraryPanel(body, status) {
  const s = status || {};
  const dl = el("dl", "facts");
  const fact = (label, value) => dl.append(el("dt", "", label), el("dd", "", value || "unknown"));
  fact("Answer model", s.model);
  fact("Router model", s.fast);
  fact("Search index", s.up ? count(s.documents || 0, false) + " · " + Number(s.chunks || 0).toLocaleString() + " pieces" : "merud isn't running");
  body.append(dl);
  const p = el("p", "privacy");
  p.append(icon("lock", 14), document.createTextNode(" No account and no cloud. Meru runs on " + (s.machine || "this Mac") +
    ", and its settings live in " + configPath(s.socket) + "."));
  body.append(p);
}

// configPath names config.toml beside merud's socket.
function configPath(socket) {
  if (!socket) return "~/.meru/config.toml";
  return socket.replace(/merud\.sock$/, "config.toml");
}
