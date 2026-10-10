// Settings: the settings screen, with a back link and seven sections, and
// the Usage screen, which the rail opens beside Settings.
// Connections, Folders, About you, Skills, Models and Activity each
// ask the Bridge for their data when they open, and every change goes to
// merud, which writes config.toml or the memory folder and reloads.
// Connections opens with the connector cards, which connectors.js draws
// from each connector's fields. About,
// the last, says what Meru is and links to the project; the Bridge hands
// it the links, so this file names no web address. The page keeps nothing
// it can't fetch again.

import { bridge, copyText, errorText } from "./api.js";
import { icon } from "./icons.js";
import { el, button, seconds } from "./turns.js";
import { connectorCard } from "./connectors.js";

// SECTIONS are the sections of Settings, in the order its tabs show them.
const SECTIONS = [
  { id: "connections", label: "Connections" },
  { id: "folders", label: "Folders" },
  { id: "you", label: "About you" },
  { id: "skills", label: "Skills" },
  { id: "models", label: "Models" },
  { id: "activity", label: "Activity" },
  { id: "about", label: "About" },
];

// TOOLS_SHOWN is how many tools a connection card lists before "Show all".
const TOOLS_SHOWN = 6;

// WEB_TOOLS are the built-in tools the Web search card holds; the other
// built-ins go on the "Built into Meru" card.
const WEB_TOOLS = ["web_search", "web_fetch"];

// current is the open Settings screen: its root element, the section on
// show, the connection to bring into view, and what it calls back into
// app.js.
const current = { root: null, section: "connections", focus: "", pages: null, expanded: new Set(), connectors: [] };

// openable returns a <dd> holding text as a link-styled button that runs
// open on a click, or as plain text when open is null. className adds a
// class to the button, such as "mono" for a path.
function openable(text, className, open) {
  const dd = el("dd", "");
  if (!open) {
    dd.textContent = text;
    return dd;
  }
  dd.append(button(text, { className: ("link-button " + className).trim(), onClick: open }));
  return dd;
}

// openSettings draws Settings in root at section. focus names a
// connection to scroll to and mark, as "Change what google may do" asks.
export function openSettings(root, section, focus, pages) {
  current.root = root;
  current.section = SECTIONS.some((s) => s.id === section) ? section : "connections";
  current.focus = focus || "";
  current.pages = pages;
  if (current.focus) current.expanded.add(current.focus);
  draw();
}

// policyWords says a policy the way the switches in Settings do.
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
  const root = current.root;
  root.replaceChildren();
  const head = el("header", "page-head");
  const back = button("Back to chat", { className: "text-button back", iconName: "back", onClick: current.pages.back });
  const title = el("h1", "", "Settings");
  title.id = "settings-title";
  head.append(back, title);
  root.append(head);

  const tabs = el("div", "tabs");
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "Settings sections");
  SECTIONS.forEach((s, i) => {
    const on = s.id === current.section;
    const t = button(s.label, { className: "tab" });
    t.id = "tab-" + s.id;
    t.setAttribute("role", "tab");
    t.setAttribute("aria-selected", String(on));
    t.setAttribute("aria-controls", "settings-section");
    t.tabIndex = on ? 0 : -1;
    t.addEventListener("click", () => {
      current.section = s.id;
      current.focus = "";
      draw();
    });
    t.addEventListener("keydown", (e) => {
      const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (!step) return;
      e.preventDefault();
      current.section = SECTIONS[(i + step + SECTIONS.length) % SECTIONS.length].id;
      draw();
      document.getElementById("tab-" + current.section).focus();
    });
    tabs.append(t);
  });
  root.append(tabs);

  const body = el("section", "page-body");
  body.id = "settings-section";
  body.setAttribute("role", "tabpanel");
  body.setAttribute("aria-labelledby", "tab-" + current.section);
  root.append(body);
  body.append(el("p", "panel-note", "Loading…"));
  const drawers = { connections, folders, you, skills, models, activity, about };
  drawers[current.section](body);
}

// openUsage draws the Usage screen in root: a back link, the title, and
// the Charts and Prometheus queries tabs that usage fills.
export function openUsage(root, pages) {
  current.pages = pages;
  root.replaceChildren();
  const head = el("header", "page-head");
  const back = button("Back to chat", { className: "text-button back", iconName: "back", onClick: pages.back });
  const title = el("h1", "", "Usage");
  title.id = "usage-title";
  head.append(back, title);
  const body = el("section", "page-body");
  body.append(el("p", "panel-note", "Loading…"));
  root.append(head, body);
  usage(body);
}

// fail shows err in body, with a hint when merud isn't running.
function fail(body, err) {
  body.replaceChildren(el("p", "error", errorText(err)));
  body.append(el("p", "panel-note", "Settings needs merud. Start it in a terminal with: merud"));
}

// intro adds a short paragraph that says what a section is for.
function intro(body, text) {
  body.append(el("p", "section-intro", text));
}

// ---- Connections ----

// connections draws the connector cards, a card per tool source, then the
// catalog. A merud from before the connectors op has none to show.
function connections(body) {
  Promise.all([bridge.connections(), bridge.connectors().catch(() => [])]).then(
    ([cv, conns]) => {
      current.connectors = conns;
      drawConnections(body, cv);
    },
    (err) => fail(body, err),
  );
}

// drawConnections draws the Connections section from cv, with the
// connectors merud reported last.
function drawConnections(body, cv) {
  body.replaceChildren();
  if (current.connectors.length) {
    intro(body, "The programs Meru installs, starts and checks for you. Each card asks for what its " +
      "connector needs; Fix asks again for what is missing, or checks it again.");
    const frame = el("div", "cards connectors");
    const ctx = {
      notice: current.pages.notice,
      done: (status, text) => {
        if (text) current.pages.notice(text);
        current.focus = status ? "connector:" + status.id : "";
        connections(body);
        current.pages.refreshStatus();
      },
    };
    for (const c of current.connectors) frame.append(connectorCard(c, ctx));
    body.append(frame);
    body.append(el("h2", "section-head", "Tools"));
  }
  intro(body, "What Meru may reach, tool by tool. Off: the model never sees the tool. " +
    "Ask: Meru asks you before each call. Allow: it runs without asking. A new tool starts Off.");
  const grid = el("div", "cards");
  for (const c of cv.connections) {
    if (c.kind === "builtin") {
      // The Web search card shows the SearXNG connector's state and
      // sentence; the built-in card stays "Connected".
      const web = { ...c, tools: c.tools.filter((t) => WEB_TOOLS.includes(t.name)), connector: c.web, sentence: c.web_sentence };
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

  const focused = current.focus && body.querySelector('[data-connection="' + CSS.escape(current.focus) + '"]');
  if (focused) {
    focused.classList.add("focused");
    focused.scrollIntoView({ block: "center" });
    const first = focused.querySelector("[role=radio][aria-checked=true]");
    if (first) first.focus();
  }
}

// CONNECTOR_PILLS says each connector state on a card's pill, with the
// pill's colour: ok is green, the rest amber.
const CONNECTOR_PILLS = {
  ok: ["ok", "Ready"],
  starting: ["down", "Starting"],
  needs_config: ["down", "Needs setup"],
  failed: ["down", "Failed"],
  off: ["down", "Off"],
};

// connectorManaged reports whether merud's supervisor runs connection c,
// rather than a server the user added by hand.
function connectorManaged(c) {
  return Boolean(c.connector) && c.connector !== "by_hand";
}

// connectionCard draws one source: its state, how many tools are on, and
// a switch per tool. key names the card for "Change what … may do". A
// connector merud runs shows its own state and sentence, and for one
// that isn't ready, a link to its connector card.
function connectionCard(body, c, title, key) {
  const card = el("article", "card connection");
  card.dataset.connection = key;
  const head = el("div", "card-head");
  head.append(el("h3", "", title));
  const up = c.state === "connected";
  let [look, words] = [up ? "ok" : "down", up ? "Connected" : "Not connected"];
  if (connectorManaged(c)) [look, words] = CONNECTOR_PILLS[c.connector] || ["down", c.connector];
  head.append(el("span", "pill " + look, words));
  card.append(head);

  const where = [c.kind === "mcp" ? "MCP server" : c.kind === "a2a" ? "Agent" : c.kind === "command" ? "Programs on this Mac" : "Inside merud"];
  if (connectorManaged(c)) where[0] = c.kind === "builtin" ? "Inside merud, through SearXNG" : "Connector";
  if (c.connector === "by_hand") where.push("set up by hand");
  if (c.transport) where.push(c.transport === "http" ? "connects to " + c.url : "merud starts it");
  if (c.remote) where.push("on another machine");
  card.append(el("p", "card-sub", where.join(" · ")));
  if (connectorManaged(c)) {
    card.append(el("p", c.connector === "ok" ? "card-note" : "card-error", c.sentence));
    // The connector's own card, above, holds its form, its sign-in and
    // its Fix button; this one links to it.
    const id = c.kind === "builtin" ? "searxng" : c.name;
    if (c.connector !== "ok" && current.connectors.some((k) => k.id === id)) {
      card.append(button("Set it up", {
        className: "link-button",
        onClick: () => {
          const target = document.getElementById("connector-" + id);
          if (target) target.scrollIntoView({ block: "center" });
        },
      }));
    }
  } else if (!up && c.err) {
    card.append(el("p", "card-error", c.err));
  }
  // A server set up by hand in a connector's place says it needs nothing,
  // and how Meru could run it instead: "… Optional: meru mcp adopt
  // obsidian lets Meru run and restart it."
  if (c.connector === "by_hand" && c.sentence) card.append(el("p", "card-note", c.sentence));

  const on = c.tools.filter((t) => t.policy !== "off").length;
  const total = c.offered >= 0 && c.kind !== "builtin" ? Math.max(c.offered, c.tools.length) : c.tools.length;
  card.append(el("p", "card-count", on + " of " + total + " tools on"));
  if (c.note) card.append(el("p", "card-note", c.note));

  const list = el("ul", "tool-list");
  const all = current.expanded.has(key);
  const shown = all ? c.tools : c.tools.slice(0, TOOLS_SHOWN);
  for (const t of shown) list.append(toolRow(body, c, t));
  card.append(list);
  if (c.tools.length > TOOLS_SHOWN) {
    card.append(button(all ? "Show fewer" : "Show all " + c.tools.length + " tools", {
      className: "text-button",
      onClick: () => {
        if (all) current.expanded.delete(key);
        else current.expanded.add(key);
        connections(body);
      },
    }));
  }
  // A connector merud runs has no [[mcp.servers]] entry to remove.
  if (c.kind === "mcp" && !connectorManaged(c)) card.append(removeButton(body, c.name));
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
          current.focus = "";
          drawConnections(body, cv);
          current.pages.notice(t.name + " is now " + policyWords(policy).toLowerCase() + ".");
        },
        (err) => current.pages.notice(errorText(err)),
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
              current.pages.notice("Removed " + name + ".");
            },
            (err) => current.pages.notice(errorText(err)),
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
      current.pages.notice(e.title + " needs its key first.");
      keyInput.focus();
      return;
    }
    add.disabled = true;
    bridge.addConnection(e.name, secret, key).then(
      () => {
        if (keyInput) keyInput.value = "";
        current.pages.notice("Added " + e.title + ".");
        done();
      },
      (err) => {
        add.disabled = false;
        current.pages.notice(errorText(err));
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
      current.pages.notice("Give the server a name of letters, digits, - and _.");
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
        current.focus = n;
        current.expanded.add(n);
        current.pages.notice("Added " + n + ". Its tools start Off; turn on the ones you want.");
        connections(body);
      },
      (err) => {
        add.disabled = false;
        current.pages.notice(errorText(err));
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
          current.pages.notice("Removed " + f.path + ". Its files leave the index in a moment.");
        },
        (err) => current.pages.notice(errorText(err)),
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
    (err) => current.pages && current.pages.notice(errorText(err)),
  );
}

// addFolder adds path through merud, which starts indexing it.
function addFolder(path, done) {
  bridge.addFolder(path).then(
    (next) => {
      done(next);
      current.pages.notice("Added " + path + ". Meru is indexing it now.");
    },
    (err) => current.pages.notice(errorText(err)),
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
    bridge.addMemory(kind.value, text.value).then(() => you(body), (err) => current.pages.notice(errorText(err)));
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
          .then(() => you(body), (err) => current.pages.notice(errorText(err))),
      }), button("Cancel", { className: "text-button", onClick: () => you(body) }));
      input.focus();
    },
  }));
  li.append(button("Forget", {
    className: "text-button",
    onClick: () => bridge.forgetMemory(m.id).then(() => you(body), (err) => current.pages.notice(errorText(err))),
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
      (err) => current.pages.notice(errorText(err)),
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

// CAPABILITIES are the capabilities a model card shows, in this order, and
// the words for each. "completion" is every chat model's, so it says
// nothing.
const CAPABILITIES = [
  ["vision", "Reads pictures"],
  ["tools", "Calls tools"],
  ["thinking", "Thinks first"],
];

// NO_TOOLS says what a model without tools means for Meru.
const NO_TOOLS = "Meru can't use tools with this model; questions that need mail, files or the web will fail. " +
  "Meru answers them from the model alone and says so under the answer.";

// models draws the models config names, what Ollama holds, and the models
// we tried as the answer model.
function models(body) {
  bridge.models().then(
    (m) => drawModels(body, m),
    (err) => fail(body, err),
  );
}

// drawModels draws the Models section from m, merud's models reply.
function drawModels(body, m) {
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

  drawSets(body, m);

  body.append(el("h2", "section-head", "Models you can use for answers"));
  body.append(el("p", "", "We tried these as the answer model in September 2026, on an M4 Max with 64 GB, " +
    "with MiniCPM5-2B as the router. Any of them can write the answers. Use for answers unloads the model that " +
    "answers now, loads this one, and saves it in config.toml, so merud starts with it; the next question uses it, " +
    "with no restart. The router and the embedding model stay as they are."));
  const cards = el("div", "cards");
  for (const c of m.choices || []) cards.append(modelCard(body, c));
  body.append(cards);
  body.append(el("p", "card-note", "We also tried qwen3.8:27b (17 GB, dense). In our benchmark it passed about as " +
    "many tasks as the models above but took about eight times as long per question, so it isn't offered here."));

  body.append(el("h2", "section-head", "To change them by hand"));
  body.append(el("p", "", "Set profile, or the models under [models], in " + m.config_path +
    ", then restart merud. merud asks Ollama for 32,768 tokens of context on every call, " +
    "from [ollama] context_length, so the larger models need no Ollama setting."));
}

// drawSets draws the Model sets part of the Models section: a card per
// [[models.sets]] entry in config.toml, the set in use first marked, with
// Use, which switches until merud stops, as /model <name> does, and Make
// default on the set in use, which saves it, as /model save does.
function drawSets(body, m) {
  body.append(el("h2", "section-head", "Model sets"));
  if (m.sets.length === 0) {
    body.append(el("p", "", "Model sets name the models you switch between, so you can compare them. Add " +
      "[[models.sets]] entries to " + m.config_path + " (config.example.toml shows how), then restart merud."));
    return;
  }
  body.append(el("p", "", "Use switches the answer model until merud stops: merud unloads the one that answers now, " +
    "then loads the set's. Make default saves the set in use in config.toml. In the chat, /model <name> and " +
    "/model save do the same, and /usage by model in meru chat compares them."));
  const cards = el("div", "cards");
  for (const s of m.sets) cards.append(setCard(body, s));
  body.append(cards);
}

// setCard draws one model set: its main model and size, where Ollama has
// it, what it can do, its think setting, any fast or embed model it names,
// and the button that fits: Use, or Make default for the set in use.
function setCard(body, s) {
  const card = el("article", "card model");
  card.setAttribute("aria-label", s.name);
  const head = el("div", "card-head");
  head.append(el("h3", "", s.name));
  let state = s.pulled ? "Downloaded" : "Not downloaded";
  if (s.loaded) state = "Loaded";
  if (s.active) state = "In use";
  head.append(el("span", "pill " + (s.pulled ? "ok" : "down"), state));
  card.append(head);

  const sub = [s.main || "keeps the answer model", s.bytes ? diskSize(s.bytes) : "", s.think_off ? "thinking off" : ""];
  card.append(el("p", "card-sub", sub.filter(Boolean).join(" · ")));
  if (s.capabilities.length) {
    const caps = el("div", "card-actions");
    caps.setAttribute("aria-label", "What it can do");
    for (const [name, words] of CAPABILITIES) {
      if (s.capabilities.includes(name)) caps.append(el("span", "pill ok", words));
    }
    if (!s.capabilities.includes("tools")) caps.append(el("span", "pill down", "No tools"));
    card.append(caps);
  }
  const also = [];
  if (s.fast) also.push("fast model " + s.fast);
  if (s.embed) also.push("embedding model " + s.embed);
  if (also.length) {
    card.append(el("p", "card-note", "Also sets the " + also.join(" and the ") +
      ", which merud moves to only after Make default and a restart."));
  }
  if (!s.pulled && s.main) card.append(copyBlock("ollama pull " + s.main));

  const act = (label, run, done) => {
    const b = button(label, { className: "button primary" });
    b.addEventListener("click", () => {
      b.disabled = true;
      current.pages.notice(label === "Use" ? "Switching to " + s.name + "…" : "Saving…");
      run().then(
        (next) => {
          drawModels(body, next);
          current.pages.refreshStatus();
          current.pages.notice(next.warning ? done + " " + next.warning : done);
        },
        (err) => {
          b.disabled = false;
          current.pages.notice(errorText(err));
        },
      );
    });
    card.append(b);
  };
  if (s.active) {
    act("Make default", () => bridge.saveModels(), s.name + " is the default now; merud starts with it.");
  } else {
    act("Use", () => bridge.useModelSet(s.name, false), s.name + " answers from the next question on.");
  }
  return card;
}

// diskSize writes a model's size as `ollama list` does, in units of 1,000,
// as the chat's /model table does: "38 GB", "8.1 GB".
function diskSize(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 999.5 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return (i > 0 && v < 9.95 ? v.toFixed(1) : v.toFixed(0)) + " " + units[i];
}

// modelCard draws one model we tried: what it does well and badly, what
// it can do, whether Ollama has it, the commands to fetch it and try it,
// and the "Use for answers" button.
function modelCard(body, c) {
  const card = el("article", "card model");
  card.setAttribute("aria-label", c.label);
  const head = el("div", "card-head");
  head.append(el("h3", "", c.label));
  const answering = c.tiers.includes("main");
  let state = c.installed ? "Downloaded" : "Not downloaded";
  if (answering) state = "Answering now";
  head.append(el("span", "pill " + (c.installed ? "ok" : "down"), state));
  card.append(head);

  const sub = [c.name, c.size];
  if (c.tiers.includes("fast")) sub.push("the router uses it");
  card.append(el("p", "card-sub", sub.join(" · ")));

  const caps = el("div", "card-actions");
  caps.setAttribute("aria-label", "What it can do");
  for (const [name, words] of CAPABILITIES) {
    if (c.capabilities.includes(name)) caps.append(el("span", "pill ok", words));
  }
  const tools = c.capabilities.includes("tools");
  if (!tools) caps.append(el("span", "pill down", "No tools"));
  card.append(caps);

  card.append(el("p", "", c.good));
  card.append(el("p", "card-note", c.bad));
  if (!tools) card.append(el("p", "card-error", NO_TOOLS));

  card.append(el("p", "card-sub", c.installed ? "Downloaded. To fetch it again:" : "Download it first:"));
  card.append(copyBlock(c.pull));
  card.append(el("p", "card-sub", "Try it in a terminal:"));
  card.append(copyBlock(c.run));

  if (answering) return card;
  const use = button("Use for answers", { className: "button primary" });
  if (!c.installed) {
    use.disabled = true;
    use.title = "Run " + c.pull + " first";
  }
  use.addEventListener("click", () => {
    use.disabled = true;
    bridge.useModel(c.name).then(
      (next) => {
        drawModels(body, next);
        current.pages.refreshStatus();
        const done = c.label + " writes the answers from the next question on.";
        current.pages.notice(next.warning ? done + " " + next.warning : done);
      },
      (err) => {
        use.disabled = false;
        current.pages.notice(errorText(err));
      },
    );
  });
  card.append(use);
  return card;
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

// USAGE_TABS are the Usage section's two sub-tabs. Charts opens first.
const USAGE_TABS = [
  { id: "charts", label: "Charts" },
  { id: "prometheus", label: "Prometheus queries" },
];

// usage draws the Usage section as two sub-tabs. Charts holds, in the
// order you look at them most, the usage windows, four charts, the
// slowest tasks, tool calls by outcome and use by model. Prometheus
// queries says how to read the same numbers in a Prometheus you run. The
// windows come first and alone, so an older merud, which can't send the
// rest, still fills the table.
function usage(body) {
  bridge.usage().then(
    (windows) => {
      body.replaceChildren();
      const panels = usageTabs(body);
      const charts = panels.charts;
      intro(charts, "Counted from the session transcripts on this Mac. A task is one question or request that Meru answered.");
      charts.append(usageTable(windows || []));
      const more = el("div", "usage-detail");
      more.append(el("p", "panel-note", "Loading the charts…"));
      charts.append(more);
      prometheusSection(panels.prometheus, "");
      bridge.usageDetail().then(
        (d) => {
          drawDetail(more, d);
          panels.prometheus.replaceChildren();
          prometheusSection(panels.prometheus, d.metrics || "");
        },
        (err) => more.replaceChildren(el("p", "panel-note", errorText(err))),
      );
    },
    (err) => fail(body, err),
  );
}

// usageTabs adds the sub-tabs to body and returns their panels by id. The
// first tab shows; the arrow keys move between tabs, as in Settings' own
// tabs.
function usageTabs(body) {
  const bar = el("div", "tabs subtabs");
  bar.setAttribute("role", "tablist");
  bar.setAttribute("aria-label", "Usage views");
  body.append(bar);
  const panels = {};
  const tabs = [];
  const show = (id) => {
    for (const t of tabs) {
      const on = t.dataset.id === id;
      t.setAttribute("aria-selected", String(on));
      t.tabIndex = on ? 0 : -1;
      panels[t.dataset.id].hidden = !on;
    }
  };
  USAGE_TABS.forEach((u, i) => {
    const t = button(u.label, { className: "tab" });
    t.id = "usage-tab-" + u.id;
    t.dataset.id = u.id;
    t.setAttribute("role", "tab");
    t.setAttribute("aria-controls", "usage-panel-" + u.id);
    t.addEventListener("click", () => show(u.id));
    t.addEventListener("keydown", (e) => {
      const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (!step) return;
      e.preventDefault();
      const next = USAGE_TABS[(i + step + USAGE_TABS.length) % USAGE_TABS.length].id;
      show(next);
      document.getElementById("usage-tab-" + next).focus();
    });
    tabs.push(t);
    bar.append(t);
    const panel = el("div", "usage-panel");
    panel.id = "usage-panel-" + u.id;
    panel.setAttribute("role", "tabpanel");
    panel.setAttribute("aria-labelledby", t.id);
    panels[u.id] = panel;
    body.append(panel);
  });
  show(USAGE_TABS[0].id);
  return panels;
}

// usageTable returns the table of usage windows, one row per window.
function usageTable(windows) {
  const table = el("table", "log");
  const head = el("tr");
  for (const h of ["", "Tasks", "Chats", "Tokens in", "Tokens out", "Time answering", "Files read", "Tool calls"]) {
    head.append(el("th", "", h));
  }
  const thead = el("thead");
  thead.append(head);
  const tbody = el("tbody");
  for (const w of windows) {
    const tr = el("tr");
    tr.append(el("th", "", WINDOWS[w.name] || w.name));
    for (const v of [w.turns, w.sessions, w.tokens_in, w.tokens_out]) tr.append(el("td", "num", Number(v).toLocaleString()));
    tr.append(el("td", "num", seconds(w.active_ms || 0)), el("td", "num", String(w.docs)), el("td", "num", String(w.tool_calls)));
    tbody.append(tr);
  }
  table.append(thead, tbody);
  return wrapTable(table);
}

// wrapTable puts a table in a box that scrolls sideways in a narrow window.
function wrapTable(table) {
  const wrap = el("div", "table-wrap");
  wrap.append(table);
  return wrap;
}

// drawDetail fills box with the dashboard d that merud sent: four charts,
// two to a row where the window is wide enough, then the tables. The
// Prometheus queries tab draws d.metrics.
function drawDetail(box, d) {
  box.replaceChildren();
  const days = d.days || [];

  const grid = el("div", "chart-grid");
  box.append(grid);
  const charts = [
    {
      title: "Tasks per day",
      note: "questions and requests Meru answered",
      axis: "tasks",
      series: [{ key: "questions", className: "bar", say: (v) => v + (v === 1 ? " task" : " tasks") }],
      tick: (v) => String(v),
    },
    {
      title: "Time to first token",
      note: "p50, the middle task, over p95, the slow end",
      axis: "seconds",
      series: [
        { key: "ttft_p95_ms", className: "bar p95", say: (v) => "p95 " + seconds(v) },
        { key: "ttft_p50_ms", className: "bar", say: (v) => "p50 " + seconds(v) },
      ],
      tick: (v) => String(Math.round(v / 100) / 10),
    },
    {
      title: "Tokens in per day",
      note: "what the models read: prompts, files and tool results",
      axis: "tokens",
      series: [{ key: "tokens_in", className: "bar", say: (v) => Number(v).toLocaleString() + " tokens in" }],
      tick: shortCount,
    },
    {
      title: "Tokens out per day",
      note: "what the models wrote",
      axis: "tokens",
      series: [{ key: "tokens_out", className: "bar", say: (v) => Number(v).toLocaleString() + " tokens out" }],
      tick: shortCount,
    },
  ];
  // Every cell goes in the grid before any chart draws, so each cell has
  // its final width when its chart measures it. A chart drawn at that width
  // shows its text at its real size; one measured while its cell was
  // alone in the grid would come out twice as wide, then shrink to half.
  const cells = charts.map((c) => {
    const cell = el("section", "chart-cell");
    cell.append(el("h3", "chart-title", c.title));
    cell.append(el("p", "chart-note", c.note));
    grid.append(cell);
    return cell;
  });
  charts.forEach((c, i) => cells[i].append(barChart(c, days, cells[i].clientWidth)));

  box.append(el("h2", "section-head", "Slowest tasks this week"));
  box.append(slowestTable(d.slowest || []));

  box.append(el("h2", "section-head", "Tool calls, last 30 days"));
  box.append(toolsTable(d.tools || []));

  box.append(el("h2", "section-head", "By answer model"));
  box.append(modelsTable(d.models || []));
}

// CHART_H is a chart's height, and the PAD values the room around its plot
// for the axis labels, in pixels.
const CHART_H = 180;
const PAD = { left: 52, right: 8, top: 10, bottom: 24 };

// svg makes an SVG element with attrs. SVG elements need their own
// namespace, so el() can't make them.
function svg(tag, attrs) {
  const node = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs || {})) node.setAttribute(k, String(v));
  return node;
}

// shortDate writes "2026-10-09" as "9 Oct", in the page's language.
function shortDate(date) {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(y, m - 1, d).toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

// shortCount writes a count in a few characters: 950, 18k, 1.4M.
function shortCount(v) {
  if (v >= 1e6) return (v / 1e6).toFixed(v < 1e7 ? 1 : 0).replace(/\.0$/, "") + "M";
  if (v >= 1e3) return (v / 1e3).toFixed(v < 1e4 ? 1 : 0).replace(/\.0$/, "") + "k";
  return String(v);
}

// niceStep returns a round step for about four ticks up to max: 1, 2 or 5
// times a power of ten.
function niceStep(max) {
  const rough = max / 4;
  const p = Math.pow(10, Math.floor(Math.log10(rough)));
  const f = rough / p;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p;
}

// barChart returns a bar chart of days, width pixels wide, for the chart
// c: one group of bars per day, a y axis with round ticks labelled by
// c.tick and titled c.axis, and a date under every seventh day, counting
// back from today. Each series names a field of a day, the CSS class that
// colors its bars, and how to say a value; later series draw over earlier
// ones, so the taller one goes first. Each bar carries a <title>, which
// shows on hover and which screen readers read, and the chart says its
// peak in an aria-label.
function barChart(c, days, width) {
  const W = Math.max(280, Math.round(width || 600));
  const plotW = W - PAD.left - PAD.right;
  const plotH = CHART_H - PAD.top - PAD.bottom;
  const peakValue = Math.max(0, ...days.flatMap((day) => c.series.map((s) => day[s.key] || 0)));
  const step = peakValue > 0 ? niceStep(peakValue) : 1;
  const top = Math.max(step, Math.ceil(peakValue / step) * step);
  const y = (v) => PAD.top + plotH - (v / top) * plotH;

  const chart = svg("svg", { viewBox: "0 0 " + W + " " + CHART_H, role: "img" });
  // The y axis: a grid line and a label at each tick, and the axis title.
  for (let v = 0; v <= top + step / 2; v += step) {
    chart.append(svg("line", { class: v === 0 ? "axis" : "grid", x1: PAD.left, y1: y(v), x2: W - PAD.right, y2: y(v) }));
    chart.append(svg("line", { class: "axis", x1: PAD.left - 4, y1: y(v), x2: PAD.left, y2: y(v) }));
    const label = svg("text", { class: "tick", x: PAD.left - 7, y: y(v) + 4, "text-anchor": "end" });
    label.textContent = c.tick(v);
    chart.append(label);
  }
  chart.append(svg("line", { class: "axis", x1: PAD.left, y1: PAD.top, x2: PAD.left, y2: PAD.top + plotH }));
  const title = svg("text", { class: "axis-title", x: 0, y: 0, "text-anchor": "middle",
    transform: "translate(11 " + (PAD.top + plotH / 2) + ") rotate(-90)" });
  title.textContent = c.axis;
  chart.append(title);

  // The bars, and the x axis's ticks and dates.
  const slot = plotW / Math.max(1, days.length);
  const barW = Math.max(1, slot - 2);
  days.forEach((day, i) => {
    const x = PAD.left + i * slot;
    for (const s of c.series) {
      const v = day[s.key] || 0;
      if (!v) continue;
      const h = Math.max(1, (v / top) * plotH);
      const bar = svg("rect", { class: s.className, x: x + 1, y: PAD.top + plotH - h, width: barW, height: h });
      const t = svg("title");
      t.textContent = shortDate(day.date) + ": " + s.say(v);
      bar.append(t);
      chart.append(bar);
    }
    if ((days.length - 1 - i) % 7 === 0) {
      const mid = x + slot / 2;
      chart.append(svg("line", { class: "axis", x1: mid, y1: PAD.top + plotH, x2: mid, y2: PAD.top + plotH + 4 }));
      const label = svg("text", { class: "tick", x: mid, y: CHART_H - 6, "text-anchor": "middle" });
      label.textContent = shortDate(day.date);
      chart.append(label);
    }
  });

  const first = c.series[0];
  const peak = days.reduce((best, day) => ((day[first.key] || 0) > (best[first.key] || 0) ? day : best), days[0] || {});
  let words = c.title + ", last " + days.length + " days.";
  if (peak && peak[first.key]) words += " Highest " + first.say(peak[first.key]) + " on " + shortDate(peak.date) + ".";
  else words += " Nothing yet.";
  chart.setAttribute("aria-label", words);
  return chart;
}

// slowestTable returns the slowest tasks of the week. A row opens its
// chat; a chat the rail doesn't list goes by its date.
function slowestTable(rows) {
  if (!rows.length) return el("p", "panel-note", "No tasks this week.");
  const table = el("table", "log");
  const head = el("tr");
  for (const h of ["Chat", "Took", "First token", "Tokens in", "Route", "Model"]) head.append(el("th", "", h));
  const thead = el("thead");
  thead.append(head);
  const tbody = el("tbody");
  for (const q of rows) {
    const when = new Date(q.at);
    const stamp = when.toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
    const title = current.pages.chatTitle(q.session) || "A chat from " + stamp;
    const tr = el("tr");
    const cell = el("td");
    cell.append(button(title, { className: "link-button", onClick: () => current.pages.openChat(q.session, title) }));
    tr.append(cell);
    tr.append(el("td", "num", seconds(q.ms || 0)), el("td", "num", q.ttft_ms ? seconds(q.ttft_ms) : "–"));
    tr.append(el("td", "num", Number(q.tokens_in || 0).toLocaleString()), el("td", "", q.route || ""), el("td", "mono", q.model || ""));
    tbody.append(tr);
  }
  table.append(thead, tbody);
  return wrapTable(table);
}

// toolsTable returns each tool's calls by outcome; the outcomes other than
// ok show in the danger color, as the Activity section shows them.
function toolsTable(tools) {
  if (!tools.length) return el("p", "panel-note", "No tool calls in the last 30 days.");
  const table = el("table", "log");
  const head = el("tr");
  for (const h of ["Tool", "Kind", "Calls", "Outcomes"]) head.append(el("th", "", h));
  const thead = el("thead");
  thead.append(head);
  const tbody = el("tbody");
  for (const t of tools) {
    const tr = el("tr");
    tr.append(el("td", "mono", t.tool), el("td", "", t.kind || ""), el("td", "num", String(t.calls)));
    const cell = el("td");
    const names = Object.keys(t.outcomes || {}).sort((a, b) => t.outcomes[b] - t.outcomes[a]);
    names.forEach((name, i) => {
      if (i) cell.append(document.createTextNode(" · "));
      cell.append(el("span", "outcome " + name, name + " " + t.outcomes[name]));
    });
    tr.append(cell);
    tbody.append(tr);
  }
  table.append(thead, tbody);
  return wrapTable(table);
}

// modelsTable returns use by answer model, as /usage by model shows it in
// meru chat.
function modelsTable(models) {
  if (!models.length) return el("p", "panel-note", "No finished tasks yet.");
  const table = el("table", "log");
  const head = el("tr");
  for (const h of ["Model", "Tasks", "Tokens in", "Tokens out", "First token (p50)", "Tokens a second"]) head.append(el("th", "", h));
  const thead = el("thead");
  thead.append(head);
  const tbody = el("tbody");
  for (const m of models) {
    const tr = el("tr");
    tr.append(el("td", "mono", m.model || "(before model switches were logged)"));
    for (const v of [m.turns, m.tokens_in, m.tokens_out]) tr.append(el("td", "num", Number(v || 0).toLocaleString()));
    tr.append(el("td", "num", m.ttft_p50_ms ? seconds(m.ttft_p50_ms) : "–"));
    tr.append(el("td", "num", m.eval_ms ? (m.tokens_out / (m.eval_ms / 1000)).toFixed(1) : "–"));
    tbody.append(tr);
  }
  table.append(thead, tbody);
  return wrapTable(table);
}

// QUERIES are the PromQL queries the Usage section offers, each with what
// it answers. The names are the ones merud's /metrics page writes.
const QUERIES = [
  ["Tasks per day", "sum(increase(meru_turn_duration_seconds_count[1d]))"],
  ["Time to first token, p95 over the last hour", "histogram_quantile(0.95, sum by (le) (rate(gen_ai_server_time_to_first_token_seconds_bucket[1h])))"],
  ["Turn time by route, p95", "histogram_quantile(0.95, sum by (le, meru_route) (rate(meru_turn_duration_seconds_bucket[1h])))"],
  ["Tool calls that failed today", 'sum by (gen_ai_tool_name, meru_outcome) (increase(meru_tool_calls_total{meru_outcome!="ok"}[1d]))'],
  ["Tokens per model today", "sum by (gen_ai_request_model, gen_ai_token_type) (increase(gen_ai_client_token_usage_sum[1d]))"],
  ["Model loads today", "sum(increase(meru_engine_load_duration_seconds_count[1d]))"],
];

// prometheusSection tells the user how to read the same numbers in a
// Prometheus they run: the address when merud serves /metrics, or the
// line that turns it on, then the queries, each with a Copy button.
function prometheusSection(box, metrics) {
  intro(box, "merud can serve the numbers behind the charts to a Prometheus you run on this Mac, for longer history and your own queries.");
  if (metrics) {
    box.append(el("p", "panel-note", "merud serves its metrics at this address, on this Mac only. Point a Prometheus you run at it:"));
    box.append(copyBlock(metrics));
  } else {
    box.append(el("p", "panel-note", "To read these numbers in a Prometheus you run, add this line under [observability] in ~/.meru/config.toml and restart merud:"));
    box.append(copyBlock('prometheus = "127.0.0.1:9464"'));
  }
  box.append(el("p", "panel-note", "Counts start again from zero when merud restarts; increase() and rate() allow for that."));
  for (const [what, q] of QUERIES) {
    box.append(el("p", "query-label", what));
    box.append(copyBlock(q));
  }
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
      body.append(el("p", "", "In some Eastern traditions, Meru (मेरु) is the cosmic mountain that the sun, moon and stars turn around. " +
        "The assistant takes the name because it works the same way: it stays in one place, on your computer, " +
        "and your notes, tools and daily routine turn around it."));

      body.append(el("h2", "section-head", "What it does"));
      body.append(list(DOES));
      body.append(el("h2", "section-head", "Why it runs on your computer"));
      body.append(list(WHY));

      body.append(el("h2", "section-head", "This copy"));
      const dl = el("dl", "facts");
      dl.append(el("dt", "", "Version"), el("dd", "", a.version));
      // The license, the settings file and Meru's folder open on a click:
      // the license on GitHub, the file in its default app, the folder in
      // Finder. Each goes through the Bridge, which checks the URL first.
      const fail = (err) => current.pages.notice(errorText(err));
      const license = a.links.find((l) => l.id === "license");
      dl.append(el("dt", "", "License"), openable(a.license, "",
        license ? () => bridge.openURL(license.url).catch(fail) : null));
      dl.append(el("dt", "", "Settings"), openable(a.config_path, "mono",
        () => bridge.openSource(a.config_path).catch(fail)));
      dl.append(el("dt", "", "Meru's folder"), openable(a.data_dir, "mono",
        () => bridge.openSource(a.data_dir).catch(fail)));
      body.append(dl);
      body.append(el("p", "card-note", "Meru's folder holds the settings, your keys in secrets.toml, " +
        "every chat, what Meru remembers and the search index."));
      // Setup opens on its own only on a first run, so this link is the
      // one way back to its four steps later.
      const again = el("p", "");
      again.append(button("Run setup again", { className: "text-button link", onClick: current.pages.openSetup }));
      body.append(again);

      body.append(el("h2", "section-head", "The project"));
      const links = el("div", "about-links");
      for (const l of a.links) {
        links.append(button(l.label, {
          className: "button secondary",
          iconName: "globe",
          onClick: () => bridge.openURL(l.url).catch((err) => current.pages.notice(errorText(err))),
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

// settingsPanel fills the side panel while Settings shows: the models,
// the search index, and the line that says nothing leaves this Mac.
export function settingsPanel(body, status) {
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
