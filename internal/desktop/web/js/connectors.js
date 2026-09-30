// Connector cards: the frame at the top of Settings, Connections, with one
// card per connector merud knows, the model runtime it only checks
// included. A card shows the
// connector's state on a pill with merud's sentence, an on/off switch, a
// form, Save, and a Fix button. The form comes from the fields the
// connector's manifest declares, never from code that knows the
// connector: INPUTS draws one input per field type, and this file names
// no connector. A card for a connector set up by hand shows Adopt, which
// shows merud's plan in the page's own dialog before it changes anything.
// merud checks and writes every change; a secret goes to merud and never
// comes back.

import { bridge, errorText } from "./api.js";
import { el, button } from "./turns.js";

// PILLS says each connector state on a card's pill, with the pill's
// colour: ok is green, the rest amber.
const PILLS = {
  ok: ["ok", "Ready"],
  starting: ["down", "Starting"],
  needs_config: ["down", "Needs setup"],
  failed: ["down", "Failed"],
  off: ["down", "Off"],
  by_hand: ["down", "Set up by hand"],
};

// KINDS says where each kind of connector runs, under the card's title.
const KINDS = {
  stdio: "A program merud installs and starts on this Mac",
  http: "A server merud installs and starts on this Mac",
  container: "A container merud runs in Docker",
  dependency: "You run it; Meru checks that it answers",
};

// live maps a connector's ID to the function that shows the next step of
// its save or fix on its card, while one runs. app.js hands each step to
// connectorProgress.
const live = new Map();

// connectorProgress shows one step merud reported, such as "Meru is
// installing it and checking it.", on the card that waits for it.
export function connectorProgress(status) {
  const show = status && live.get(status.id);
  if (show) show(status);
}

// askFields returns the fields Fix asks for, as rpc.AskFields does: the
// ones merud's Fix names; for a connector that is off, every field but
// the sign-in; none otherwise, when Fix only runs the check again.
export function askFields(c) {
  return c.fields.filter((f) => f.type !== "oauth" &&
    ((c.state === "needs_config" && c.fix.includes(f.id)) || c.state === "off"));
}

// INPUTS draws the input for each field type. Each entry takes the field
// and the card's context and returns { node, input, read }: the element
// to add, the control to focus, and a function that returns what the
// user entered.
const INPUTS = {
  text: (f) => textInput(f, "text"),
  email: (f) => textInput(f, "email"),
  secret: (f) => secretInput(f),
  folder: (f) => folderInput(f),
  choice: (f) => choiceInput(f),
  oauth: (f, c, ctx) => oauthInput(f, c, ctx),
};

// fieldID names the input for field f of connector c, for its label.
function fieldID(c, f) {
  return "connector-" + c.id + "-" + f.id;
}

// textInput is a one-line field, typed text or email, holding the value
// config has now.
function textInput(f, type) {
  const input = el("input", "text-input");
  input.type = type;
  input.value = f.value || "";
  input.placeholder = f.default || "";
  input.autocomplete = "off";
  input.spellcheck = false;
  return { node: input, input, read: () => input.value.trim() };
}

// secretInput is a password field. It never holds the secret, which
// never leaves merud: it says whether one is saved, and an empty field
// keeps that one.
function secretInput(f) {
  const input = el("input", "text-input");
  input.type = "password";
  input.autocomplete = "off";
  input.placeholder = f.saved ? "Saved. Type a new one to replace it." : "Paste it here";
  const wrap = el("div", "field-row");
  wrap.append(input);
  if (f.saved) wrap.append(el("span", "pill ok", "saved"));
  return { node: wrap, input, read: () => input.value.trim() };
}

// folderInput is a path field with a Choose button, which opens the
// system's folder dialog through the Bridge.
function folderInput(f) {
  const input = el("input", "text-input grow");
  input.value = f.value || "";
  input.placeholder = f.default || "~/…";
  input.spellcheck = false;
  const wrap = el("div", "field-row");
  wrap.append(input, button("Choose…", {
    className: "button secondary",
    iconName: "folder",
    onClick: () => bridge.chooseFolder().then((path) => {
      if (path) input.value = path;
    }, () => {}),
  }));
  return { node: wrap, input, read: () => input.value.trim() };
}

// choiceInput is a list of the field's choices; an optional field may
// stay empty.
function choiceInput(f) {
  const select = el("select", "text-input");
  const options = f.required ? f.choices : [""].concat(f.choices);
  for (const v of options) {
    const o = el("option", "", v || "(none)");
    o.value = v;
    o.selected = v === (f.value || f.default || "");
    select.append(o);
  }
  return { node: select, input: select, read: () => select.value };
}

// oauthInput is the sign-in. It holds no value: the server signs the user
// in. While merud has a sign-in link, the button opens it in the browser;
// otherwise a note says when one comes.
function oauthInput(f, c, ctx) {
  if (c.link) {
    const b = button(f.label, {
      className: "button primary",
      onClick: () => bridge.openURL(c.link).catch((err) => ctx.notice(errorText(err))),
    });
    return { node: b, input: b, read: () => "" };
  }
  const note = el("p", "card-note", c.state === "ok"
    ? "Signed in."
    : "Once the other fields are saved, Meru starts the server and a sign-in button shows here.");
  return { node: note, input: null, read: () => "" };
}

// connectorCard draws connector c. ctx gives notice(text), for the line
// under the page, and done(status, text), which redraws the section after
// a change. mark names fields Fix asks for: the card marks them and
// focuses the first.
export function connectorCard(c, ctx, mark) {
  const card = el("article", "card connector");
  card.dataset.connection = "connector:" + c.id;
  card.id = "connector-" + c.id;
  const head = el("div", "card-head");
  head.append(el("h3", "", c.name));
  const [look, words] = PILLS[c.state] || ["down", c.state];
  head.append(el("span", "pill " + look, words));
  card.append(head);
  card.append(el("p", "card-sub", KINDS[c.kind] || ""));
  card.append(el("p", c.state === "ok" ? "card-note" : "card-error", c.sentence));

  const progress = el("p", "card-note connector-progress");
  progress.setAttribute("aria-live", "polite");
  const buttons = [];
  // run sends one change or fix, shows merud's steps as they come, and
  // hands the result to after. The card's buttons wait meanwhile.
  const run = (promise, after) => {
    for (const b of buttons) b.disabled = true;
    progress.textContent = "Asking merud…";
    live.set(c.id, (st) => (progress.textContent = st.sentence));
    promise.then(
      (st) => {
        live.delete(c.id);
        after(st);
      },
      (err) => {
        live.delete(c.id);
        for (const b of buttons) b.disabled = false;
        progress.textContent = "";
        progress.className = "card-error connector-progress";
        progress.textContent = errorText(err);
      },
    );
  };
  const settled = (st) => ctx.done(st, c.name + ": " + (PILLS[st.state] || ["", st.state])[1].toLowerCase() + ". " + st.sentence);

  // A connector set up by hand runs as the user's [[mcp.servers]] entry:
  // the card offers Adopt and nothing else.
  if (c.state === "by_hand") {
    const adopt = button("Adopt", { className: "button primary", onClick: () => adoptDialog(c, ctx, adopt) });
    buttons.push(adopt);
    const actions = el("div", "card-actions");
    actions.append(adopt);
    card.append(actions, progress);
    return card;
  }

  if (c.kind !== "dependency") {
    const on = c.state !== "off";
    const sw = button(on ? "On" : "Off", { className: "switch" + (on ? " on" : "") });
    sw.setAttribute("role", "switch");
    sw.setAttribute("aria-checked", String(on));
    sw.setAttribute("aria-label", "Turn " + c.name + (on ? " off" : " on"));
    sw.addEventListener("click", () => run(bridge.setConnector(c.id, { enabled: !on }), settled));
    buttons.push(sw);
    const row = el("div", "card-actions");
    row.append(sw, el("span", "dim", on ? "On" : "Off"));
    card.append(row);
  }

  const inputs = [];
  let form = null;
  if (c.fields.length) {
    // A form, so a password field sits in one, and Enter saves.
    form = el("form", "connector-form");
    form.setAttribute("aria-label", c.name + " settings");
    for (const f of c.fields) {
      const draw = INPUTS[f.type] || INPUTS.text;
      const got = draw(f, c, ctx);
      const wrap = el("div", "connector-field");
      if (f.type !== "oauth") {
        const label = el("label", "field-label", f.label + (f.required ? "" : " (optional)"));
        if (got.input) {
          got.input.id = fieldID(c, f);
          label.htmlFor = got.input.id;
        }
        wrap.append(label);
      }
      wrap.append(got.node);
      if (f.help) wrap.append(el("p", "card-note", f.help));
      if (mark && mark.includes(f.id)) {
        wrap.classList.add("needs");
        if (got.input) got.input.setAttribute("aria-invalid", "true");
      }
      form.append(wrap);
      inputs.push({ f, got, wrap });
    }
    card.append(form);
  }

  const actions = el("div", "card-actions");
  if (form) {
    const save = button(c.state === "off" ? "Save and turn on" : "Save", { className: "button primary" });
    save.type = "submit";
    buttons.push(save);
    actions.append(save);
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      const change = { values: {}, secrets: {} };
      for (const { f, got } of inputs) {
        const v = got.read();
        if (f.type === "oauth") continue;
        if (f.type === "secret") {
          if (v) change.secrets[f.id] = v;
        } else if (v !== (f.value || "")) {
          change.values[f.id] = v;
        }
      }
      if (c.state === "off") change.enabled = true;
      run(bridge.setConnector(c.id, change), settled);
    });
  }
  const fix = button("Fix", {
    className: "button secondary",
    onClick: () => run(bridge.fixConnector(c.id), (st) => {
      const ask = askFields(st).map((f) => f.id);
      if (ask.length) {
        const next = connectorCard(st, ctx, ask);
        card.replaceWith(next);
        const first = next.querySelector(".needs input, .needs select");
        if (first) first.focus();
        ctx.notice(st.name + " needs " + (ask.length === 1 ? "one field" : ask.length + " fields") + " set; they're marked on its card.");
        return;
      }
      settled(st);
    }),
  });
  buttons.push(fix);
  actions.append(fix);
  if (form) form.append(actions);
  else card.append(actions);
  card.append(progress);
  return card;
}

// adoptDialog asks merud what adopting connector c would change and
// shows the plan in the page's dialog, a <dialog> element, never
// window.confirm. Adopt there makes the changes; Cancel changes nothing.
// focus is the button to give the focus back to.
function adoptDialog(c, ctx, focus) {
  const d = document.getElementById("dialog");
  const close = () => {
    if (d.open) d.close();
    focus.focus();
  };
  bridge.adoptConnector(c.id, false).then(
    (plan) => {
      if (plan.nothing) {
        ctx.notice(plan.changes.join(" "));
        return;
      }
      d.replaceChildren();
      const head = el("h2", "dialog-title", "Adopt " + c.name + "?");
      head.id = "dialog-title";
      d.append(head, el("p", "dialog-note", "merud will make these changes, in this order:"));
      const list = el("ol", "adopt-plan");
      for (const line of plan.changes) {
        const li = el("li");
        const [first, ...rest] = line.split("\n");
        li.append(el("span", "", first));
        if (rest.length) li.append(el("pre", "adopt-lines", rest.join("\n")));
        list.append(li);
      }
      d.append(list);
      const error = el("p", "dialog-error");
      d.append(error);
      const bar = el("div", "dialog-actions");
      const cancel = button("Cancel", { className: "button secondary", onClick: close });
      const go = button("Adopt", {
        className: "button primary",
        onClick: () => {
          go.disabled = true;
          bridge.adoptConnector(c.id, true).then(
            () => {
              close();
              ctx.done(null, "Adopted " + c.name + ". meru mcp unadopt " + c.id + " puts the old entry back.");
            },
            (err) => {
              go.disabled = false;
              error.textContent = errorText(err);
            },
          );
        },
      });
      bar.append(cancel, go);
      d.append(bar);
      if (!d.open) d.showModal();
      cancel.focus();
    },
    (err) => ctx.notice(errorText(err)),
  );
}
